package handlers

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── QRZip keys filter (#0191) ───────────────────────────────────────────

// qrZipNames fetches the zip at path and returns its status and, for a 200,
// its entry names.
func qrZipNames(t *testing.T, srv *httptest.Server, token, path string) (int, []string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return resp.StatusCode, names
}

func TestCampaignsHandler_QRZip_KeysFilter(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bobID := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bobID, "bob-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Keys Zip"}`)
	other := createCampaign(t, srv, "alice-token", `{"name":"Other Zip"}`)
	seedLinkWithCampaignAndPlacement(t, pool, alice, c.ID, "ka", "https://a.example.com", "")
	seedLinkWithCampaignAndPlacement(t, pool, alice, c.ID, "kb", "https://b.example.com", "")
	seedLinkWithCampaignAndPlacement(t, pool, alice, c.ID, "kc", "https://c.example.com", "")
	seedLinkWithCampaignAndPlacement(t, pool, alice, other.ID, "kx", "https://x.example.com", "")
	base := "/api/campaigns/" + c.Slug + "/qr.zip"

	t.Run("comma separated", func(t *testing.T) {
		status, names := qrZipNames(t, srv, "alice-token", base+"?keys=ka,kc")
		if status != 200 || len(names) != 4 {
			t.Fatalf("status %d, names %v; want 200 and 4 entries", status, names)
		}
		for _, n := range names {
			if strings.HasPrefix(n, "kb") {
				t.Errorf("unexpected entry %q", n)
			}
		}
	})
	t.Run("repeated", func(t *testing.T) {
		status, names := qrZipNames(t, srv, "alice-token", base+"?keys=kb&keys=kc")
		if status != 200 || len(names) != 4 {
			t.Fatalf("status %d, names %v; want 200 and 4 entries", status, names)
		}
	})
	t.Run("no filter is every link", func(t *testing.T) {
		_, names := qrZipNames(t, srv, "alice-token", base)
		if len(names) != 6 {
			t.Fatalf("names %v; want 6", names)
		}
	})
	t.Run("a link in another campaign is refused", func(t *testing.T) {
		status, _ := qrZipNames(t, srv, "alice-token", base+"?keys=ka,kx")
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", status)
		}
	})
	t.Run("an unknown key is refused", func(t *testing.T) {
		status, _ := qrZipNames(t, srv, "alice-token", base+"?keys=nope")
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", status)
		}
	})
	t.Run("another user's link is refused", func(t *testing.T) {
		bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Zip"}`)
		seedLinkWithCampaignAndPlacement(t, pool, bobID, bobCampaign.ID, "bobk", "https://bob.example.com", "")
		status, _ := qrZipNames(t, srv, "alice-token", base+"?keys=ka,bobk")
		if status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", status)
		}
	})
	t.Run("an empty keys param is refused", func(t *testing.T) {
		for _, q := range []string{"?keys=", "?keys=,", "?keys=%20"} {
			status, _ := qrZipNames(t, srv, "alice-token", base+q)
			if status != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", q, status)
			}
		}
	})
	t.Run("capped", func(t *testing.T) {
		var keys []string
		for i := 0; i <= maxQRZipKeys; i++ {
			keys = append(keys, fmt.Sprintf("k%d", i))
		}
		status, _ := qrZipNames(t, srv, "alice-token", base+"?keys="+strings.Join(keys, ","))
		if status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})
}
