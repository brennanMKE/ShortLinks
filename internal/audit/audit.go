// Package audit provides the append-only audit log write path.
//
// Every significant action in the system — authentication events, link
// lifecycle, admin actions — writes a row to the audit_log table through this
// package. Rows are only ever inserted; they are never updated or deleted.
//
// # Write semantics and the audit-failure policy
//
// An audit write must NEVER break the user's request: the action it records has
// already succeeded by the time we try to log it, so a failed insert is logged
// and swallowed rather than propagated. Two helpers encode this:
//
//   - Write writes through the shared pool. It returns the insert error so a
//     caller may inspect it (the audit integration test does), but callers in the
//     request path use Record instead, which logs-and-continues. This is the
//     fire-and-forget path used by the API/admin handlers, whose store call has
//     already committed by the time the audit entry is written.
//
//   - WriteTx writes through an already-open pgx.Tx. The auth ceremonies
//     (registration finish, login finish, recovery finish) open a transaction to
//     make account/credential/session creation atomic; their audit entries are
//     written inside that same transaction so a committed action can never lose
//     its audit row, and a rolled-back action never leaves a stray one. Because
//     the failure of an in-transaction audit insert would abort the surrounding
//     transaction (poisoning the pgx connection state), WriteTx returns its error
//     and the caller treats it like any other step of the ceremony.
//
// In short: handlers whose action is already committed log-and-continue
// (Record); ceremonies that own a transaction write the audit row in-band
// (WriteTx) so the row commits or rolls back atomically with the action.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier is the subset of pgx the writer uses. *pgxpool.Pool and pgx.Tx both
// satisfy it, so the same INSERT runs against the pool directly or inside a
// transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Entry is one audit_log row to be written. Pointer fields are nullable: a nil
// ActorID/UserID/TargetID inserts SQL NULL (e.g. pre-auth events carry a nil
// ActorID). Metadata is marshalled to JSONB; a nil Metadata stores SQL NULL.
// IP is the actor's client IP as a string (e.g. from the X-Forwarded-For →
// RemoteAddr boundary helper); an empty IP stores NULL.
type Entry struct {
	ActorID    *int64
	UserID     *int64
	Action     string
	TargetType string
	TargetID   *int64
	Metadata   any
	IP         string
}

// Logger is the append-only audit writer over the shared pgx pool. It is safe
// for concurrent use (the pool is). Construct it once in main and inject it into
// every service/handler that records actions.
type Logger struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New constructs a Logger over the shared connection pool. A nil slog logger
// falls back to the default logger so Record always has somewhere to report a
// swallowed write failure.
func New(pool *pgxpool.Pool) *Logger {
	return &Logger{pool: pool, log: slog.Default()}
}

// Write inserts the entry through the shared pool and returns any insert error.
// The action being recorded has already succeeded, so request-path callers
// should prefer Record (which logs-and-continues); Write exists for callers —
// including tests — that want to observe the result.
func (l *Logger) Write(ctx context.Context, e Entry) error {
	return insert(ctx, l.pool, e)
}

// WriteTx inserts the entry through an already-open transaction so the audit row
// commits or rolls back atomically with the action that opened the tx. It
// returns the insert error; the caller (an auth ceremony) treats a failure like
// any other step and rolls the whole transaction back. Use this only from code
// that owns the transaction.
func (l *Logger) WriteTx(ctx context.Context, tx pgx.Tx, e Entry) error {
	return insert(ctx, tx, e)
}

// Record writes the entry through the pool and swallows any error, logging it at
// WARN. This is the fire-and-forget path for request handlers: the action has
// already committed, so a failed audit insert must not surface to the user. The
// returned value is intentionally none — callers cannot (and should not) act on
// the outcome.
func (l *Logger) Record(ctx context.Context, e Entry) {
	if err := insert(ctx, l.pool, e); err != nil {
		l.log.Warn("audit: write failed (action recorded, audit row lost)",
			"action", e.Action, "target_type", e.TargetType, "err", err)
	}
}

// insert runs the single parameterized INSERT against any querier. Metadata is
// JSON-marshalled into the JSONB column; nullable pointer columns and an empty
// IP map to SQL NULL. created_at defaults to now() at the database.
func insert(ctx context.Context, q querier, e Entry) error {
	var metaJSON []byte
	if e.Metadata != nil {
		b, err := json.Marshal(e.Metadata)
		if err != nil {
			return fmt.Errorf("audit: marshalling metadata for %q: %w", e.Action, err)
		}
		metaJSON = b
	}

	// An empty IP string would be rejected by the INET column, so map it to NULL.
	var ip any
	if e.IP != "" {
		ip = e.IP
	}

	// metaJSON is nil when Metadata was nil, which pgx encodes as SQL NULL for the
	// JSONB column.
	var meta any
	if metaJSON != nil {
		meta = metaJSON
	}

	_, err := q.Exec(ctx,
		`INSERT INTO audit_log
		     (actor_id, user_id, action, target_type, target_id, metadata, ip_address)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ActorID, e.UserID, e.Action, nullIfEmpty(e.TargetType), e.TargetID, meta, ip,
	)
	if err != nil {
		return fmt.Errorf("audit: inserting %q: %w", e.Action, err)
	}
	return nil
}

// nullIfEmpty maps an empty string to nil so an unset target_type stores SQL
// NULL rather than an empty string.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
