package reports

import "testing"

func TestContentKey(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "https://www.opencircuitsf.com/guides/measuring-current", "www.opencircuitsf.com/guides/measuring-current"},
		{"utm stripped", "https://www.opencircuitsf.com/guides/measuring-current?utm_source=mastodon&utm_medium=social&utm_campaign=g&utm_content=a&utm_term=b", "www.opencircuitsf.com/guides/measuring-current"},
		{"utm uppercase", "https://x.com/a?UTM_Source=m", "x.com/a"},
		{"trailing slash", "https://x.com/guides/a/", "x.com/guides/a"},
		{"fragment", "https://x.com/guides/a#section-2", "x.com/guides/a"},
		{"host case", "HTTPS://WWW.X.COM/Guides/A", "www.x.com/Guides/A"},
		{"scheme ignored", "http://x.com/a", "x.com/a"},
		{"other query kept and sorted", "https://x.com/p?b=2&utm_source=s&a=1", "x.com/p?a=1&b=2"},
		{"root", "https://x.com/", "x.com"},
		{"root utm", "https://x.com/?utm_source=a", "x.com"},
		{"port kept", "https://x.com:8080/a/", "x.com:8080/a"},
		{"schemeless", "X.com/a/", "x.com/a"},
		{"https default port", "https://x.com:443/a", "x.com/a"},
		{"http default port", "http://x.com:80/a/", "x.com/a"},
		{"other port on https", "https://x.com:80/a", "x.com:80/a"},
		{"mailto opaque", "mailto:Someone@Example.com?subject=Hi", "mailto:Someone@Example.com?subject=Hi"},
		{"tel opaque", " tel:+15551234 ", "tel:+15551234"},
		{"host port schemeless", "localhost:8080/a/", "localhost:8080/a"},
		{"empty", "", ""},
		{"whitespace", "  ", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContentKey(tc.in); got != tc.want {
				t.Errorf("ContentKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	a := ContentKey("https://x.com/g/a/?utm_source=mastodon#t")
	b := ContentKey("http://X.com/g/a?utm_source=bluesky")
	if a != b {
		t.Errorf("same content got different keys %q %q", a, b)
	}
	if ContentKey("https://x.com/g/a") == ContentKey("https://x.com/g/b") {
		t.Error("different content shares a key")
	}
}

func TestContentPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.com/guides/a/?utm_source=m": "/guides/a",
		"https://x.com/":                       "/",
		"https://x.com":                        "/",
		"":                                     "",
	} {
		if got := ContentPath(in); got != want {
			t.Errorf("ContentPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinkLabel(t *testing.T) {
	tests := []struct {
		name, title, src, head, label string
	}{
		// Every title from the screenshots.
		{"shot 84 mastodon", "Measuring current without blowing the fuse — Mastodon guide post 84", "mastodon", "Measuring current without blowing the fuse", "Mastodon guide post 84"},
		{"shot 84 bluesky", "Measuring current without blowing the fuse — Bluesky guide post 84", "bluesky", "Measuring current without blowing the fuse", "Bluesky guide post 84"},
		{"shot 81", "Soldering header pins onto the ESP32-C3 SuperMini — Bluesky guide post 81", "bluesky", "Soldering header pins onto the ESP32-C3 SuperMini", "Bluesky guide post 81"},
		{"shot 78", "Transistor or MOSFET: which to use — Mastodon guide post 78", "mastodon", "Transistor or MOSFET: which to use", "Mastodon guide post 78"},
		{"shot 75", "Deep sleep: make a battery last months — Bluesky guide post 75", "bluesky", "Deep sleep: make a battery last months", "Bluesky guide post 75"},
		{"shot 80", "Updating your ESP32 over WiFi — Mastodon guide post 80", "mastodon", "Updating your ESP32 over WiFi", "Mastodon guide post 80"},
		{"shot 74", "Flyback diodes for motors and relays — Mastodon guide post 74", "mastodon", "Flyback diodes for motors and relays", "Mastodon guide post 74"},
		// Source rules and separators.
		{"own utm_source", "Guide — Fediverse 3", "fediverse", "Guide", "Fediverse 3"},
		{"bluesky only", "Guide — Bluesky", "", "Guide", "Bluesky"},
		{"bluesky pipe", "Guide | Bluesky", "", "Guide", "Bluesky"},
		{"month newsletter", "Guide — November newsletter", "", "Guide", "November newsletter"},
		{"poster qr", "Guide - Poster QR 3", "", "Guide", "Poster QR 3"},
		{"number sign", "Guide - Newsletter No. 12 2026", "", "Guide", "Newsletter No. 12 2026"},
		// False positives the whole-tail rule must not cut.
		{"threads in go", "Concurrency - Threads in Go", "", "Concurrency - Threads in Go", ""},
		{"email marketing", "Tips - Email marketing for makers", "", "Tips - Email marketing for makers", ""},
		{"x marks", "Rust vs Go - X marks the spot", "", "Rust vs Go - X marks the spot", ""},
		{"src x xylophone", "Guide - xylophone", "x", "Guide - xylophone", ""},
		{"src a apartment", "Title - About my apartment", "a", "Title - About my apartment", ""},
		{"post inside prose", "Guide - A post about fuses", "", "Guide - A post about fuses", ""},
		{"only numbers", "Guide - 2026", "", "Guide - 2026", ""},
		{"not a label", "Why this — that matters", "mastodon", "Why this — that matters", ""},
		{"en dash", "Guide – Bluesky", "", "Guide", "Bluesky"},
		{"pipe", "Guide | Newsletter 12", "", "Guide", "Newsletter 12"},
		{"hyphen", "Guide - QR 7", "", "Guide", "QR 7"},
		{"keyword anywhere", "Guide - October poster", "", "Guide", "October poster"},
		{"last separator only", "A - B - Mastodon post 3", "", "A - B", "Mastodon post 3"},
		{"x is a whole word", "Guide - xylophone club", "", "Guide - xylophone club", ""},
		{"x platform", "Guide - X thread", "", "Guide", "X thread"},
		{"hyphen without spaces", "ESP32-C3 intro", "", "ESP32-C3 intro", ""},
		// No suffix, empty.
		{"no suffix", "Measuring current", "mastodon", "Measuring current", ""},
		{"empty", "", "mastodon", "", ""},
		{"spaces", "   ", "", "", ""},
		{"whole title is label", " — Mastodon post 1", "", "", "Mastodon post 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, l := LinkLabel(tc.title, tc.src)
			if h != tc.head || l != tc.label {
				t.Errorf("LinkLabel(%q, %q) = (%q, %q), want (%q, %q)", tc.title, tc.src, h, l, tc.head, tc.label)
			}
		})
	}
}

func TestContentTitle(t *testing.T) {
	const head = "Measuring current without blowing the fuse"
	tests := []struct {
		name    string
		members []TitleInput
		dest    string
		want    string
	}{
		{"channel suffixes removed", []TitleInput{
			{head + " — Mastodon guide post 84", "mastodon"},
			{head + " — Bluesky guide post 84", "bluesky"},
		}, "https://x.com/guides/measuring-current", head},
		{"most common head wins", []TitleInput{
			{"Old title — Mastodon post 1", "mastodon"},
			{"New title — Bluesky post 1", "bluesky"},
			{"New title", ""},
		}, "https://x.com/a", "New title"},
		{"tie is deterministic", []TitleInput{{"B", ""}, {"A", ""}}, "https://x.com/a", "A"},
		{"empty titles use path", nil, "https://x.com/guides/measuring-current/?utm_source=m", "measuring current"},
		{"only label title falls back", []TitleInput{{" — Mastodon post 1", "mastodon"}}, "https://x.com/guides/deep_sleep", "deep sleep"},
		{"empty title", []TitleInput{{"", ""}}, "https://x.com/guides/esp32-ota", "esp32 ota"},
		{"root falls back to host", []TitleInput{{"", ""}}, "https://x.com/", "x.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContentTitle(tc.members, tc.dest); got != tc.want {
				t.Errorf("ContentTitle = %q, want %q", got, tc.want)
			}
		})
	}
}
