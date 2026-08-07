// Package campaigns holds the campaign domain logic: slug generation
// (slug.go) and the DB-backed CRUD data layer (store.go) — create, list,
// fetch, update, archive, and delete campaigns, each scoped to an owning
// user. It mirrors the structure of internal/links.
//
// Campaign membership on links (#0099), click attribution (#0100), and stats
// (#0102) are layered on top of this package by later issues and are not
// implemented here.
package campaigns
