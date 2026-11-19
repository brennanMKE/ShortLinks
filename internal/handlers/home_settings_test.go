package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// TestAdminSettings_PatchHomePageSettings covers set, reject (row unchanged)
// and clear for home_mode, home_title and home_about (#0174).
func TestAdminSettings_PatchHomePageSettings(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	patch := func(key, value string) int {
		t.Helper()
		b, _ := json.Marshal(map[string]string{"key": key, "value": value})
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings", jsonBody(string(b)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(withCookie(req, "admin-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := patch("home_mode", "activity"); got != http.StatusOK {
		t.Fatalf("set mode status = %d, want 200", got)
	}
	if v := settingValue(t, pool, "home_mode"); v != "activity" {
		t.Errorf("home_mode = %q, want activity", v)
	}
	if got := patch("home_mode", "everything"); got != http.StatusBadRequest {
		t.Fatalf("bad mode status = %d, want 400", got)
	}
	if v := settingValue(t, pool, "home_mode"); v != "activity" {
		t.Errorf("home_mode after rejected = %q, want unchanged", v)
	}

	// Surrounding whitespace is trimmed before validation and storage.
	if got := patch("home_title", "  Maker links \n"); got != http.StatusOK {
		t.Fatalf("set title status = %d, want 200", got)
	}
	if v := settingValue(t, pool, "home_title"); v != "Maker links" {
		t.Errorf("home_title = %q, want trimmed", v)
	}
	if got := patch("home_title", strings.Repeat("x", 81)); got != http.StatusBadRequest {
		t.Fatalf("long title status = %d, want 400", got)
	}

	about := "First paragraph.\n\nSee https://example.org/a"
	if got := patch("home_about", about); got != http.StatusOK {
		t.Fatalf("set about status = %d, want 200", got)
	}
	if v := settingValue(t, pool, "home_about"); v != about {
		t.Errorf("home_about = %q, want %q", v, about)
	}
	if got := patch("home_about", "evil\u202etxt"); got != http.StatusBadRequest {
		t.Fatalf("bidi override status = %d, want 400", got)
	}
	if got := patch("home_about", "zero\u200bwidth"); got != http.StatusBadRequest {
		t.Fatalf("zero-width status = %d, want 400", got)
	}
	// CRLF from a browser textarea is stored as LF.
	if got := patch("home_about", "one\r\ntwo\r\n\r\nthree"); got != http.StatusOK {
		t.Fatalf("crlf status = %d, want 200", got)
	}
	if v := settingValue(t, pool, "home_about"); v != "one\ntwo\n\nthree" {
		t.Errorf("home_about = %q, want CRLF normalised to LF", v)
	}
	if got := patch("home_about", "bad\x07bell"); got != http.StatusBadRequest {
		t.Fatalf("control char status = %d, want 400", got)
	}
	if got := patch("home_about", ""); got != http.StatusOK {
		t.Fatalf("clear about status = %d, want 200", got)
	}
}

// TestAudit_HomePageSettingsUpdated proves each new home page setting writes a
// settings.updated row with its key and old/new values (#0174).
func TestAudit_HomePageSettingsUpdated(t *testing.T) {
	pool := filterTestPool(t)
	srv := httptest.NewServer(auditAdminMux(t, pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settings (key, value, updated_at) VALUES
		   ('home_mode','default', now()), ('home_title','', now()), ('home_about','', now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	for _, c := range []struct{ key, old, new string }{
		{"home_mode", "default", "about"},
		{"home_title", "", "My site"},
		{"home_about", "", "About us"},
	} {
		body := `{"key":"` + c.key + `","value":"` + c.new + `"}`
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings", jsonBody(body))
		resp, err := srv.Client().Do(withCookie(req, "admin-token"))
		if err != nil {
			t.Fatalf("%s request: %v", c.key, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", c.key, resp.StatusCode)
		}
		row := lastAuditFor(t, pool, audit.ActionSettingsUpdated)
		if row.ActorID == nil || *row.ActorID != admin {
			t.Errorf("%s actor_id = %v, want %d", c.key, row.ActorID, admin)
		}
		if row.Metadata["key"] != c.key || row.Metadata["old_value"] != c.old || row.Metadata["new_value"] != c.new {
			t.Errorf("%s metadata = %v, want old=%q new=%q", c.key, row.Metadata, c.old, c.new)
		}
	}
}
