// Package links holds the link domain logic: key generation (keygen.go) and the
// DB-backed CRUD data layer (store.go) — create, list, fetch, update, and
// deactivate links, each scoped to an owning user. The handlers package builds
// the /api/links endpoints on top of Store; deduplication (#0023), URL filtering
// (#0024), audit (#0025), and SSE (#0026) layer onto the create path via the
// seams documented in Store.CreateLink and handlers.LinksHandler.Create.
package links
