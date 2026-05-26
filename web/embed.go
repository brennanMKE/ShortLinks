// Package web embeds the built Svelte SPA so the Go binary can serve it without
// any external files. The production build (`npm run build`) writes hashed
// assets into dist/; this package embeds that directory at compile time.
//
// //go:embed paths are relative to this source file and cannot use "..", which
// is why the embed lives here in web/ rather than in cmd/shortlinks/. A minimal
// dist/index.html placeholder is committed so `go build ./...` compiles from a
// clean checkout before any npm build has run; the real npm build overwrites it
// and emits the hashed dist/assets/* (which stay gitignored).
package web

import (
	"embed"
	"io/fs"
)

// distFS embeds the entire built SPA directory. The all: prefix ensures files
// whose names begin with "." or "_" are included too, matching whatever Vite
// emits.
//
//go:embed all:dist
var distFS embed.FS

// DistFS returns an fs.FS rooted at the built SPA directory (the contents of
// dist/), so callers see index.html and assets/* at the FS root. It panics only
// if the embedded layout is malformed, which is a compile-time guarantee in
// practice.
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist subdirectory missing from embedded FS: " + err.Error())
	}
	return sub
}
