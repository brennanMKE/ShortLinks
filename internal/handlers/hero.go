package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/webp"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/siteassets"
)

// Hero image rules (#0176). The bounds are part of the contract the admin UI
// and docs tell the image author about; keep them in sync with docs/.
const (
	heroMaxBytes   = 2 << 20 // 2 MB, applied to the upload and to the re-encoded result
	heroMinWidth   = 1200
	heroMinHeight  = 400
	heroMaxWidth   = 4000
	heroMaxHeight  = 2000
	heroMaxAltLen  = 200
	heroJPEGQual   = 85
	heroFormSlack  = 64 << 10 // multipart framing and the alt field
	heroAltMaxRead = 4 << 10
	// heroVersionLen is how many hex characters of the sha256 go in ?v=.
	heroVersionLen = 16

	// Cache lifetimes. A request carrying the current ?v= is content-addressed
	// and can be cached for a year; anything else revalidates daily.
	heroCacheImmutable = "public, max-age=31536000, immutable"
	heroCacheDefault   = "public, max-age=86400"
)

// heroStore is what the hero handlers need from the data layer.
// *siteassets.Store and *siteassets.MemStore satisfy it.
type heroStore interface {
	HeroMeta(ctx context.Context) (siteassets.Asset, error)
	Hero(ctx context.Context) (siteassets.Asset, error)
	PutHero(ctx context.Context, a siteassets.Asset) (oldSHA string, err error)
	DeleteHero(ctx context.Context) (siteassets.Asset, error)
}

// HeroHandler serves the public hero image and the admin upload/delete routes.
//
//	GET    /public/hero       public, no auth
//	PUT    /admin/home/hero   admin cookie session only
//	DELETE /admin/home/hero   admin cookie session only
//
// The admin routes MUST be mounted behind RequireSession + RequireAdmin, which
// refuse API tokens (403).
type HeroHandler struct {
	store   heroStore
	auditor *audit.Logger
}

// NewHeroHandler constructs a HeroHandler. A nil auditor disables audit writes.
func NewHeroHandler(store heroStore, auditor *audit.Logger) *HeroHandler {
	return &HeroHandler{store: store, auditor: auditor}
}

// heroVersion is the cache-busting query value for a stored hero.
func heroVersion(sha string) string {
	if len(sha) > heroVersionLen {
		return sha[:heroVersionLen]
	}
	return sha
}

// heroInfoFor builds the public description of a stored hero.
func heroInfoFor(a siteassets.Asset) *heroInfo {
	return &heroInfo{
		URL:    "/public/hero?v=" + heroVersion(a.SHA256),
		Alt:    a.Alt,
		Width:  a.Width,
		Height: a.Height,
	}
}

// Serve handles GET /public/hero.
func (h *HeroHandler) Serve(w http.ResponseWriter, r *http.Request) {
	a, err := h.store.Hero(r.Context())
	if errors.Is(err, siteassets.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", a.ContentType)
	hd.Set("X-Content-Type-Options", "nosniff")
	// Defence in depth: if the URL is ever opened as a document, nothing runs.
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("ETag", `"`+a.SHA256+`"`)
	if v := r.URL.Query().Get("v"); v != "" && v == heroVersion(a.SHA256) {
		hd.Set("Cache-Control", heroCacheImmutable)
	} else {
		hd.Set("Cache-Control", heroCacheDefault)
	}
	// ServeContent answers If-None-Match with 304 (ETag is set) and Range.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(a.Bytes))
}

// heroResponse is the PUT body: the same object GET /api/public/home returns.
type heroResponse struct {
	Hero *heroInfo `json:"hero"`
}

// Put handles PUT /admin/home/hero.
func (h *HeroHandler) Put(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	raw, alt, msg := readHeroForm(w, r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	proc, msg := processHero(raw)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	sum := sha256.Sum256(proc.bytes)
	asset := siteassets.Asset{
		ContentType: proc.contentType,
		Bytes:       proc.bytes,
		Width:       proc.width,
		Height:      proc.height,
		SHA256:      hex.EncodeToString(sum[:]),
		Alt:         alt,
	}
	oldSHA, err := h.store.PutHero(r.Context(), asset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	h.record(r, u.ID, audit.ActionHomeHeroUpdated, map[string]any{
		"size":         len(asset.Bytes),
		"width":        asset.Width,
		"height":       asset.Height,
		"content_type": asset.ContentType,
		"sha256":       asset.SHA256,
		"old_sha256":   oldSHA,
		"alt_length":   utf8.RuneCountInString(alt),
	})
	writeJSON(w, http.StatusOK, heroResponse{Hero: heroInfoFor(asset)})
}

// Delete handles DELETE /admin/home/hero.
func (h *HeroHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	old, err := h.store.DeleteHero(r.Context())
	if errors.Is(err, siteassets.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no hero image is set")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	h.record(r, u.ID, audit.ActionHomeHeroRemoved, map[string]any{
		"width":        old.Width,
		"height":       old.Height,
		"content_type": old.ContentType,
		"sha256":       old.SHA256,
	})
	w.WriteHeader(http.StatusNoContent)
}

// record writes an audit entry (never the image bytes).
func (h *HeroHandler) record(r *http.Request, actorID int64, action string, meta map[string]any) {
	if h.auditor == nil {
		return
	}
	actor := actorID
	h.auditor.Record(r.Context(), audit.Entry{
		ActorID:    &actor,
		Action:     action,
		TargetType: audit.TargetSiteAsset,
		Metadata:   meta,
		IP:         clientIP(r),
	})
}

// readHeroForm streams the multipart body without spooling to disk and returns
// the raw image bytes and validated alt text, or a user-facing message.
func readHeroForm(w http.ResponseWriter, r *http.Request) (raw []byte, alt string, msg string) {
	r.Body = http.MaxBytesReader(w, r.Body, heroMaxBytes+heroFormSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, "", "Send the image as multipart/form-data with fields image and alt"
	}
	var haveImage, haveAlt bool
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", tooBigOr(err, "Could not read the upload")
		}
		switch part.FormName() {
		case "image":
			if haveImage {
				return nil, "", "Send exactly one image"
			}
			b, err := io.ReadAll(io.LimitReader(part, heroMaxBytes+1))
			if err != nil {
				return nil, "", tooBigOr(err, "Could not read the image")
			}
			if len(b) > heroMaxBytes {
				return nil, "", "Image is larger than 2 MB; use a smaller or more compressed file"
			}
			raw, haveImage = b, true
		case "alt":
			if haveAlt {
				return nil, "", "Send exactly one alt field"
			}
			b, err := io.ReadAll(io.LimitReader(part, heroAltMaxRead))
			if err != nil {
				return nil, "", tooBigOr(err, "Could not read the alt text")
			}
			alt, haveAlt = string(b), true
		default:
			_, _ = io.Copy(io.Discard, io.LimitReader(part, heroAltMaxRead))
		}
		_ = part.Close()
	}
	if !haveImage || len(raw) == 0 {
		return nil, "", "Choose an image to upload (field image)"
	}
	if !haveAlt {
		return nil, "", "Alt text is required: describe the image in at most 200 characters"
	}
	alt = strings.TrimSpace(alt)
	if alt == "" {
		return nil, "", "Alt text is required: describe the image in at most 200 characters"
	}
	if n := utf8.RuneCountInString(alt); n > heroMaxAltLen {
		return nil, "", fmt.Sprintf("Alt text is %d characters; use at most %d", n, heroMaxAltLen)
	}
	if !utf8.ValidString(alt) || strings.IndexFunc(alt, unicode.IsControl) >= 0 {
		return nil, "", "Alt text must be plain text on one line"
	}
	return raw, alt, ""
}

func tooBigOr(err error, fallback string) string {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return "Image is larger than 2 MB; use a smaller or more compressed file"
	}
	return fallback
}

// processedHero is the validated, re-encoded image.
type processedHero struct {
	bytes         []byte
	contentType   string
	width, height int
}

type heroFormat int

const (
	fmtUnknown heroFormat = iota
	fmtPNG
	fmtJPEG
	fmtWebP
)

// sniffHeroFormat identifies the format from magic bytes only. The declared
// content type and the filename are never consulted. SVG and everything else
// is unknown.
func sniffHeroFormat(b []byte) heroFormat {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return fmtPNG
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return fmtJPEG
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return fmtWebP
	}
	return fmtUnknown
}

// processHero sniffs, checks dimensions from the header BEFORE decoding pixels
// (so a decompression bomb is refused cheaply), decodes, and re-encodes so no
// metadata survives. It returns a user-facing message on failure.
func processHero(raw []byte) (processedHero, string) {
	const unsupported = "Image must be a PNG, JPEG or WebP file"
	format := sniffHeroFormat(raw)
	if format == fmtUnknown {
		return processedHero{}, unsupported
	}
	var cfg image.Config
	var err error
	switch format {
	case fmtPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(raw))
	case fmtJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(raw))
	case fmtWebP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(raw))
	}
	if err != nil {
		return processedHero{}, "Image could not be read; export it again as PNG, JPEG or WebP"
	}
	if m := checkHeroDimensions(cfg.Width, cfg.Height); m != "" {
		return processedHero{}, m
	}
	var img image.Image
	switch format {
	case fmtPNG:
		img, err = png.Decode(bytes.NewReader(raw))
	case fmtJPEG:
		img, err = jpeg.Decode(bytes.NewReader(raw))
	case fmtWebP:
		img, err = webp.Decode(bytes.NewReader(raw))
	}
	if err != nil {
		return processedHero{}, "Image could not be read; export it again as PNG, JPEG or WebP"
	}
	b := img.Bounds()
	out := processedHero{width: b.Dx(), height: b.Dy()}
	if out.width != cfg.Width || out.height != cfg.Height {
		return processedHero{}, "Image could not be read; export it again as PNG, JPEG or WebP"
	}
	var buf bytes.Buffer
	if hasTransparency(img) {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		err = enc.Encode(&buf, img)
		out.contentType = "image/png"
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: heroJPEGQual})
		out.contentType = "image/jpeg"
	}
	if err != nil {
		return processedHero{}, "Image could not be processed; export it again as PNG, JPEG or WebP"
	}
	if buf.Len() > heroMaxBytes {
		return processedHero{}, fmt.Sprintf("Image is %.1f MB after processing; use a simpler image or a smaller size to stay under 2 MB", float64(buf.Len())/(1<<20))
	}
	out.bytes = buf.Bytes()
	return out, ""
}

// checkHeroDimensions returns "" when w x h is acceptable, otherwise a message
// saying exactly what to change.
func checkHeroDimensions(w, h int) string {
	var fixes []string
	if w < heroMinWidth || h < heroMinHeight {
		fixes = append(fixes, fmt.Sprintf("at least %d×%d px", heroMinWidth, heroMinHeight))
	}
	if w > heroMaxWidth || h > heroMaxHeight {
		fixes = append(fixes, fmt.Sprintf("at most %d×%d px", heroMaxWidth, heroMaxHeight))
	}
	// Aspect ratio between 2:1 and 4:1, in integers: 2h <= w <= 4h.
	if w < 2*h || w > 4*h {
		fixes = append(fixes, "a 2:1 to 4:1 shape")
	}
	if len(fixes) == 0 {
		return ""
	}
	return fmt.Sprintf("Image is %d×%d; use %s", w, h, strings.Join(fixes, " and "))
}

// hasTransparency reports whether any pixel is not fully opaque.
func hasTransparency(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}
