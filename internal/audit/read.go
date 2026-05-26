package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Record is one audit_log row read back for the admin audit view (GET
// /admin/audit). It mirrors the table's columns. Pointer fields are nullable so
// the handler can serialize an unset actor_id/user_id/target_id/ip_address as
// JSON null rather than a zero value. Metadata is the raw JSONB bytes
// (json.RawMessage) so it round-trips to the client as a JSON object, never a
// string; a nil Metadata is SQL NULL and serializes as JSON null.
//
// This is the read counterpart to Entry (the write shape). It lives in the
// audit package so the column semantics — actor_id (who acted) vs user_id
// (whose resource was affected) — stay defined in one place alongside the write
// path.
type Record struct {
	ID         int64
	ActorID    *int64
	UserID     *int64
	Action     string
	TargetType *string
	TargetID   *int64
	Metadata   json.RawMessage
	IP         *string
	CreatedAt  time.Time
}

// Reader reads audit_log rows over the shared pgx pool for the admin audit
// view. It is separate from Logger (the write path) but takes the same pool;
// construct it in main alongside the Logger.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader constructs a Reader over the shared connection pool.
func NewReader(pool *pgxpool.Pool) *Reader {
	return &Reader{pool: pool}
}

// ListAuditLog returns a page of audit_log rows newest-first (created_at DESC,
// id DESC as a stable tiebreak) together with the total row count matching the
// filter. When userID is non-nil only rows for that user_id are returned (using
// the (user_id, created_at DESC) index); when nil the whole log is returned
// (using the (created_at DESC) index). limit/offset page the result; the caller
// is responsible for clamping limit to a sane maximum.
//
// ip_address is read as host(ip_address) so an INET like 203.0.113.7/32 comes
// back as the bare address string. metadata is read as raw JSONB bytes so it
// reaches the client as a JSON object, not a quoted string.
func (r *Reader) ListAuditLog(ctx context.Context, userID *int64, limit, offset int) (rows []Record, total int64, err error) {
	// Total count first so the handler can report it for pagination. The filter
	// (user_id) is applied identically to the count and the page query.
	if userID != nil {
		err = r.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_log WHERE user_id = $1`, *userID,
		).Scan(&total)
	} else {
		err = r.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM audit_log`,
		).Scan(&total)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("audit: counting audit_log: %w", err)
	}

	// $1 = limit, $2 = offset; the optional user_id filter shifts the parameter
	// positions, so build the query/args together to keep them in step.
	var (
		sql  string
		args []any
	)
	if userID != nil {
		sql = `SELECT id, actor_id, user_id, action, target_type, target_id,
		              metadata, host(ip_address), created_at
		         FROM audit_log
		        WHERE user_id = $3
		        ORDER BY created_at DESC, id DESC
		        LIMIT $1 OFFSET $2`
		args = []any{limit, offset, *userID}
	} else {
		sql = `SELECT id, actor_id, user_id, action, target_type, target_id,
		              metadata, host(ip_address), created_at
		         FROM audit_log
		        ORDER BY created_at DESC, id DESC
		        LIMIT $1 OFFSET $2`
		args = []any{limit, offset}
	}

	queryRows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: querying audit_log: %w", err)
	}
	defer queryRows.Close()

	for queryRows.Next() {
		var rec Record
		var metaRaw []byte
		if err := queryRows.Scan(
			&rec.ID, &rec.ActorID, &rec.UserID, &rec.Action, &rec.TargetType,
			&rec.TargetID, &metaRaw, &rec.IP, &rec.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("audit: scanning audit_log row: %w", err)
		}
		// metaRaw is nil for a SQL NULL metadata column; leave Metadata nil so it
		// serializes as JSON null. Otherwise hand the raw JSONB bytes straight
		// through so the client receives a JSON object.
		if metaRaw != nil {
			rec.Metadata = json.RawMessage(metaRaw)
		}
		rows = append(rows, rec)
	}
	if err := queryRows.Err(); err != nil {
		return nil, 0, fmt.Errorf("audit: iterating audit_log rows: %w", err)
	}
	return rows, total, nil
}
