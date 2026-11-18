// Package idempotency implements the Idempotency-Key request header (#0147)
// for the three create endpoints (POST /api/links, POST /api/campaigns and
// POST /api/campaigns/{slug}/links/batch), so an agent that retries after a
// timeout does not create duplicates.
//
// Rules, per authenticated user (keys are never shared between users):
//
//   - No header: the request passes through untouched.
//   - First use of a key: a pending row is claimed with INSERT ... ON CONFLICT
//     DO NOTHING, the handler runs, and a 2xx or 4xx response is stored (status,
//     body, Content-Type and Location). A 5xx or a panic deletes the pending row
//     so the client can retry the same key.
//   - Same key, same method, path and body: the stored response is replayed with
//     "Idempotent-Replayed: true". The handler does not run.
//   - Same key, different method, path or body: 422 idempotency_key_reused.
//   - Same key while the first request is still running: 409
//     idempotency_key_in_progress.
//   - A pending row older than StalePending (60 s) is assumed to belong to a
//     crashed request and is reclaimed by the next request with the same key and
//     body, which then runs the handler itself.
//   - Records expire after TTL (24 h): an expired row is treated as absent and
//     is deleted by the retention job (Store.Prune).
//   - Keys are 1-255 printable ASCII characters, otherwise 400
//     invalid_idempotency_key.
//
// Dev mode (STORAGE=json) has no database, so the middleware is not mounted
// there and the header is ignored.
package idempotency
