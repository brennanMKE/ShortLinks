package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"runtime/debug"
)

// BuildToken is the placeholder in web/index.html that StampBuild replaces
// with the running build id, producing <meta name="sl-build" content="…"> (#0185).
const BuildToken = "%%SL_BUILD%%"

// VersionHandler serves GET /api/version: the running build's id, which open
// SPA tabs compare with the sl-build meta stamped into their page to notice a
// newer deploy (#0185). It is public, touches nothing and is never cached.
type VersionHandler struct{ build string }

// NewVersionHandler returns a VersionHandler that reports build.
func NewVersionHandler(build string) *VersionHandler { return &VersionHandler{build: build} }

func (h *VersionHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Build string `json:"build"`
	}{h.build})
}

// BuildID returns the id to stamp for this binary: override (set at build
// time with -ldflags "-X main.buildID=…", typically the git commit) when
// non-empty, otherwise a short hash of dist's index.html. That file names the
// content-hashed JS and CSS bundles, so the hash changes whenever the SPA does.
//
// The hash also folds in the Go toolchain's vcs.revision (and vcs.modified),
// which `go build` records inside a git worktree, so a Go-only deploy that
// leaves the SPA bundle unchanged still gets a new id.
func BuildID(dist fs.FS, override string) string {
	return buildID(dist, override, vcsStamp())
}

func buildID(dist fs.FS, override, vcs string) string {
	if override != "" {
		return override
	}
	data, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return "unknown"
	}
	h := sha256.New()
	h.Write(data)
	h.Write([]byte("\x00" + vcs))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// vcsStamp returns the vcs.revision and vcs.modified build settings, or "".
func vcsStamp() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	return rev + "/" + modified
}

// StampBuild wraps dist so that index.html has BuildToken replaced by the
// (escaped) build id. Every other file passes through untouched. The shell is
// stamped once, here, so SPAHandler needs no knowledge of it.
func StampBuild(dist fs.FS, build string) fs.FS {
	data, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		return dist
	}
	stamped := bytes.ReplaceAll(data, []byte(BuildToken), []byte(html.EscapeString(build)))
	return &stampedFS{FS: dist, index: stamped}
}

type stampedFS struct {
	fs.FS
	index []byte
}

func (s *stampedFS) Open(name string) (fs.File, error) {
	if name != "index.html" {
		return s.FS.Open(name)
	}
	real, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	defer real.Close()
	info, err := real.Stat()
	if err != nil {
		return nil, err
	}
	return &memFile{Reader: bytes.NewReader(s.index), info: memInfo{FileInfo: info, size: int64(len(s.index))}}, nil
}

// ReadFile lets fs.ReadFile skip Open/Stat/Read for the stamped shell.
func (s *stampedFS) ReadFile(name string) ([]byte, error) {
	if name == "index.html" {
		return append([]byte(nil), s.index...), nil
	}
	return fs.ReadFile(s.FS, name)
}

type memFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Close() error               { return nil }

// memInfo reports the stamped length, which differs from the embedded file's.
type memInfo struct {
	fs.FileInfo
	size int64
}

func (i memInfo) Size() int64 { return i.size }
