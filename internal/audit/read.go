package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	// ActorEmail and UserEmail are the emails of the actor and the affected
	// user, resolved from users when the row is read (nil when the id is NULL).
	ActorEmail *string
	UserEmail  *string
}

// Filter narrows an audit log listing. The zero value matches every row.
type Filter struct {
	// UserID matches rows whose user_id (the affected account) is this id.
	UserID *int64
	// Email matches rows where either the actor or the affected user has this
	// email (case-insensitive, exact).
	Email string
	// Actions matches rows whose action is any of these values.
	Actions []string
	// From is inclusive and To is exclusive, both on created_at.
	From, To *time.Time
}

// where builds the WHERE clause (with a leading space, or empty) and its
// positional args, numbering placeholders from first.
func (f Filter) where(first int) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$N", fmt.Sprintf("$%d", first+len(args)-1)))
	}
	if f.UserID != nil {
		add("a.user_id = $N", *f.UserID)
	}
	if f.Email != "" {
		add(`(a.actor_id IN (SELECT id FROM users WHERE lower(email) = lower($N))
		   OR a.user_id  IN (SELECT id FROM users WHERE lower(email) = lower($N)))`, f.Email)
	}
	if len(f.Actions) > 0 {
		add("a.action = ANY($N)", f.Actions)
	}
	if f.From != nil {
		add("a.created_at >= $N", *f.From)
	}
	if f.To != nil {
		add("a.created_at < $N", *f.To)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
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
// filter. limit/offset page the result; the caller is responsible for clamping
// limit to a sane maximum.
//
// ip_address is read as host(ip_address) so an INET like 203.0.113.7/32 comes
// back as the bare address string. metadata is read as raw JSONB bytes so it
// reaches the client as a JSON object, not a quoted string.
func (r *Reader) ListAuditLog(ctx context.Context, f Filter, limit, offset int) (rows []Record, total int64, err error) {
	const from = ` FROM audit_log a`
	const join = `
	   LEFT JOIN users ua ON ua.id = a.actor_id
	   LEFT JOIN users uu ON uu.id = a.user_id`

	// The same filter feeds the count and the page query; the page query's
	// $1/$2 are limit/offset, so its filter placeholders start at $3.
	countWhere, countArgs := f.where(1)
	if err = r.pool.QueryRow(ctx, `SELECT COUNT(*)`+from+countWhere, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("audit: counting audit_log: %w", err)
	}

	where, fargs := f.where(3)
	args := append([]any{limit, offset}, fargs...)
	queryRows, err := r.pool.Query(ctx,
		`SELECT a.id, a.actor_id, a.user_id, a.action, a.target_type, a.target_id,
		        a.metadata, host(a.ip_address), a.created_at, ua.email, uu.email`+
			from+join+where+`
		  ORDER BY a.created_at DESC, a.id DESC
		  LIMIT $1 OFFSET $2`, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("audit: querying audit_log: %w", err)
	}
	defer queryRows.Close()

	for queryRows.Next() {
		var rec Record
		var metaRaw []byte
		if err := queryRows.Scan(
			&rec.ID, &rec.ActorID, &rec.UserID, &rec.Action, &rec.TargetType,
			&rec.TargetID, &metaRaw, &rec.IP, &rec.CreatedAt, &rec.ActorEmail, &rec.UserEmail,
		); err != nil {
			return nil, 0, fmt.Errorf("audit: scanning audit_log row: %w", err)
		}
		// metaRaw is nil for a SQL NULL metadata column; leave Metadata nil so it
		// serializes as JSON null.
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
