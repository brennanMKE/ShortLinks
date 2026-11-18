package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// nowFunc is a package var so tests can pin time.
var nowFunc = time.Now

const (
	// Header is the request header carrying the key.
	Header = "Idempotency-Key"
	// ReplayHeader is set to "true" on a replayed response.
	ReplayHeader = "Idempotent-Replayed"

	maxKeyLen = 255
	// maxBodyBytes matches the handlers' JSON body cap.
	maxBodyBytes = 1 << 20
)

// ValidKey reports whether k is 1-255 printable ASCII characters.
func ValidKey(k string) bool {
	if len(k) < 1 || len(k) > maxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x20 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// Middleware returns a wrapper to apply to the create routes AFTER
// authentication (it keys on the user in the request context).
func Middleware(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vals := r.Header.Values(Header)
			if len(vals) == 0 {
				next.ServeHTTP(w, r)
				return
			}
			user, ok := middleware.UserFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if len(vals) != 1 || !ValidKey(vals[0]) {
				writeErr(w, http.StatusBadRequest, "invalid_idempotency_key",
					"Idempotency-Key must be 1-255 printable ASCII characters")
				return
			}
			key := vals[0]

			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid_request", "could not read request body")
				return
			}
			if len(body) > maxBodyBytes {
				writeErr(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			h := sha256.New()
			h.Write([]byte(r.Method))
			h.Write([]byte{0})
			h.Write([]byte(r.URL.Path))
			h.Write([]byte{0})
			h.Write(body)

			claim, err := store.Claim(r.Context(), user.ID, key, r.Method, r.URL.Path, h.Sum(nil), nowFunc())
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "internal_error", "internal server error")
				return
			}
			switch claim.Outcome {
			case Reused:
				writeErr(w, http.StatusUnprocessableEntity, "idempotency_key_reused",
					"this Idempotency-Key was already used with a different request")
				return
			case InProgress:
				w.Header().Set("Retry-After", "1")
				writeErr(w, http.StatusConflict, "idempotency_key_in_progress",
					"a request with this Idempotency-Key is still being processed")
				return
			case Replay:
				st := claim.Stored
				if st.ContentType != "" {
					w.Header().Set("Content-Type", st.ContentType)
				}
				if st.Location != "" {
					w.Header().Set("Location", st.Location)
				}
				w.Header().Set(ReplayHeader, "true")
				w.WriteHeader(st.Status)
				_, _ = w.Write(st.Body)
				return
			}

			// Owned: run the handler against a buffer so a 5xx can be discarded,
			// under a deadline shorter than StalePending. Cleanup uses a context
			// detached from the request, which a client disconnect may cancel.
			bg := context.WithoutCancel(r.Context())
			release := func() {
				ok, err := store.Release(bg, user.ID, key, claim.ClaimID)
				if err != nil {
					slog.Error("idempotency: release failed", slog.Any("error", err))
				} else if !ok {
					slog.Warn("idempotency: release skipped, key was reclaimed by another request")
				}
			}
			done := false
			defer func() {
				if !done { // panic
					release()
				}
			}()
			hctx, cancel := context.WithTimeout(r.Context(), HandlerTimeout)
			defer cancel()
			rec := &recorder{header: http.Header{}, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(hctx))
			done = true

			if rec.status >= 500 || rec.status < 200 {
				release()
			} else {
				st := Stored{
					Status:      rec.status,
					Body:        rec.body.Bytes(),
					ContentType: rec.header.Get("Content-Type"),
					Location:    rec.header.Get("Location"),
				}
				ok, err := store.Complete(bg, user.ID, key, claim.ClaimID, st)
				if err != nil {
					ok, err = store.Complete(bg, user.ID, key, claim.ClaimID, st) // retry once
				}
				switch {
				case err != nil:
					// Leave the row pending: it is reclaimable after StalePending.
					slog.Error("idempotency: storing response failed, leaving key pending",
						slog.Any("error", err))
				case !ok:
					slog.Warn("idempotency: complete skipped, key was reclaimed by another request")
				}
			}

			for k, v := range rec.header {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.status)
			_, _ = w.Write(rec.body.Bytes())
		})
	}
}

// recorder buffers a handler's response.
type recorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }
func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": msg})
}
