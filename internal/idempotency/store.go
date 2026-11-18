package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// TTL is how long a key is remembered.
	TTL = 24 * time.Hour
	// HandlerTimeout bounds how long the wrapped handler may run (it gets a
	// context deadline). It is deliberately shorter than StalePending so a live
	// owner has normally finished, or been cancelled, before its row can be
	// reclaimed. The create endpoints are fast (a few DB writes), so 90 s is
	// generous.
	HandlerTimeout = 90 * time.Second
	// StalePending is how long a pending row may sit before it is assumed to
	// belong to a crashed request and can be reclaimed. The 30 s margin over
	// HandlerTimeout covers the Complete write; claim_id fencing makes a late
	// owner harmless even if the margin is exceeded.
	StalePending = 120 * time.Second
)

// Outcome says what a Claim decided.
type Outcome int

const (
	// Owned: the caller holds the key and must run the handler, then Complete
	// or Release.
	Owned Outcome = iota
	// Replay: a completed response is stored; see Claim.Stored.
	Replay
	// InProgress: the first request is still running.
	InProgress
	// Reused: the key was used with a different method, path or body.
	Reused
)

// Stored is a completed response.
type Stored struct {
	Status      int
	Body        []byte
	ContentType string
	Location    string
}

// Claim is the result of Store.Claim.
type Claim struct {
	Outcome Outcome
	// ClaimID is set when Outcome is Owned; Complete and Release require it.
	ClaimID string
	Stored  Stored
}

// Store persists idempotency keys in PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Claim decides what to do with (userID, key) at time now.
func (s *Store) Claim(ctx context.Context, userID int64, key, method, path string, hash []byte, now time.Time) (Claim, error) {
	// An expired row is as good as absent.
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE user_id = $1 AND key = $2 AND created_at < $3`,
		userID, key, now.Add(-TTL)); err != nil {
		return Claim{}, err
	}

	// A concurrent Release can delete the row between our insert and select;
	// loop a few times rather than report a spurious state.
	claimID := newClaimID()
	for attempt := 0; attempt < 3; attempt++ {
		tag, err := s.pool.Exec(ctx,
			`INSERT INTO idempotency_keys (user_id, key, method, path, request_sha256, claim_id, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (user_id, key) DO NOTHING`,
			userID, key, method, path, hash, claimID, now)
		if err != nil {
			return Claim{}, err
		}
		if tag.RowsAffected() == 1 {
			return Claim{Outcome: Owned, ClaimID: claimID}, nil
		}

		var (
			m, p      string
			h         []byte
			status    *int
			body      []byte
			ct, loc   *string
			createdAt time.Time
		)
		err = s.pool.QueryRow(ctx,
			`SELECT method, path, request_sha256, status, response_body,
			        response_content_type, response_location, created_at
			   FROM idempotency_keys WHERE user_id = $1 AND key = $2`,
			userID, key).Scan(&m, &p, &h, &status, &body, &ct, &loc, &createdAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return Claim{}, err
		}

		if m != method || p != path || string(h) != string(hash) {
			return Claim{Outcome: Reused}, nil
		}
		if status != nil {
			st := Stored{Status: *status, Body: body}
			if ct != nil {
				st.ContentType = *ct
			}
			if loc != nil {
				st.Location = *loc
			}
			return Claim{Outcome: Replay, Stored: st}, nil
		}
		if createdAt.Before(now.Add(-StalePending)) {
			// Reclaim atomically: only one racing request sees RowsAffected 1.
			tag, err := s.pool.Exec(ctx,
				`UPDATE idempotency_keys SET created_at = $3, claim_id = $5
				  WHERE user_id = $1 AND key = $2 AND status IS NULL AND created_at = $4`,
				userID, key, now, createdAt, claimID)
			if err != nil {
				return Claim{}, err
			}
			if tag.RowsAffected() == 1 {
				return Claim{Outcome: Owned, ClaimID: claimID}, nil
			}
		}
		return Claim{Outcome: InProgress}, nil
	}
	return Claim{Outcome: InProgress}, nil
}

// Complete stores the response for a key, but only if claimID still owns the
// pending row. It reports false (and writes nothing) when ownership was lost to
// a reclaim.
func (s *Store) Complete(ctx context.Context, userID int64, key, claimID string, r Stored) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE idempotency_keys
		    SET status = $4, response_body = $5, response_content_type = $6, response_location = $7
		  WHERE user_id = $1 AND key = $2 AND claim_id = $3 AND status IS NULL`,
		userID, key, claimID, r.Status, r.Body, nullable(r.ContentType), nullable(r.Location))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Release deletes a pending row so the client can retry the same key, but only
// if claimID still owns it. It reports false when nothing was deleted.
func (s *Store) Release(ctx context.Context, userID int64, key, claimID string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys
		  WHERE user_id = $1 AND key = $2 AND claim_id = $3 AND status IS NULL`,
		userID, key, claimID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func newClaimID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return hex.EncodeToString(b)
}

// Prune deletes every row older than TTL relative to now and returns the count.
// It is called from the hourly retention job.
func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE created_at < $1`, now.Add(-TTL))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
