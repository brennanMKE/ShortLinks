package idp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeriveDatabaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://u:p@localhost:5432/db?sslmode=disable":         "postgres://u:p@localhost:5432/db?sslmode=disable&search_path=portiko",
		"postgres://u:p@localhost/db":                              "postgres://u:p@localhost/db?search_path=portiko",
		"postgres://u:p@localhost/db?search_path=public&sslmode=x": "postgres://u:p@localhost/db?sslmode=x&search_path=portiko",
		"host=localhost dbname=db":                                 "host=localhost dbname=db search_path=portiko",
	} {
		got, err := DeriveDatabaseURL(in)
		if err != nil || got != want {
			t.Errorf("DeriveDatabaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := DeriveDatabaseURL(""); err == nil {
		t.Error("an empty DATABASE_URL should be an error")
	}
}

func TestMountAtRoutesOnlyThePrefix(t *testing.T) {
	label := func(s string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(s + " " + r.URL.Path)) })
	}
	h := mountAt(label("provider"), label("next"))
	for path, want := range map[string]string{
		"/id":                                  "provider /id",
		"/id/":                                 "provider /id/",
		"/id/.well-known/openid-configuration": "provider /id/.well-known/openid-configuration",
		"/idx":                                 "next /idx",
		"/health":                              "next /health",
		"/u/abc":                               "next /u/abc",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Body.String() != want {
			t.Errorf("%s -> %q; want %q", path, rec.Body.String(), want)
		}
	}
}
