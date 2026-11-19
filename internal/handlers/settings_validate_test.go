package handlers

import (
	"strings"
	"testing"
)

func TestValidateSettingValue(t *testing.T) {
	const base = "https://go.example.com"
	long := "https://example.org/" + strings.Repeat("a", 2049)

	ok := []struct{ key, value string }{
		{"registrations_enabled", "true"},
		{"registrations_enabled", "false"},
		{"home_redirect_url", ""},
		{"home_redirect_url", "https://www.example.com/"},
		{"home_redirect_url", "http://example.org/a?b=c"},
		{"home_redirect_url", "HTTPS://Example.ORG"},
		{"some_unknown_key", "anything"},
	}
	for _, c := range ok {
		if err := validateSettingValue(c.key, c.value, base); err != nil {
			t.Errorf("validateSettingValue(%q, %q) = %v, want nil", c.key, c.value, err)
		}
	}

	bad := []struct{ key, value string }{
		{"registrations_enabled", "maybe"},
		{"home_redirect_url", "javascript:alert(1)"},
		{"home_redirect_url", "data:text/html,x"},
		{"home_redirect_url", "//example.org"},
		{"home_redirect_url", "/relative"},
		{"home_redirect_url", "example.org"},
		{"home_redirect_url", "ftp://example.org"},
		{"home_redirect_url", "https://user:pw@example.org/"},
		{"home_redirect_url", "https://exa mple.org"},
		{"home_redirect_url", long},
		{"home_redirect_url", "https://go.example.com/"},
		{"home_redirect_url", "https://GO.EXAMPLE.COM/anything"},
		{"home_redirect_url", "http://go.example.com:8080/"},
		{"home_redirect_url", "https://go.example.com./"}, // trailing-dot FQDN is the same host
		{"home_mode", ""},
		{"home_mode", "Activity"},
		{"home_mode", "hero"},
		{"home_title", strings.Repeat("é", 81)},
		{"home_title", "two\nlines"},
		{"home_title", "tab\there"},
		{"home_title", "nul\x00"},
		{"home_about", strings.Repeat("a", 2001)},
		{"home_about", "bell\x07"},
		{"home_about", "tab\there"},
		{"home_about", "del\x7f"},
		{"home_about", "c1\u0085"},
		{"home_about", "evil\u202egnp.exe"},   // right-to-left override
		{"home_about", "zero\u200bwidth"},     // zero-width space
		{"home_title", "bom\ufeff"},           // byte order mark
		{"home_title", "iso\u2066late\u2069"}, // bidi isolates
	}
	for _, c := range bad {
		if err := validateSettingValue(c.key, c.value, base); err == nil {
			t.Errorf("validateSettingValue(%q, %q) = nil, want error", c.key, c.value)
		}
	}

	if err := validateSettingValue("home_redirect_url", "https://go.example.com/", base); err == nil ||
		!strings.Contains(err.Error(), "must not point at this instance") {
		t.Errorf("self error = %v, want 'must not point at this instance'", err)
	}
}

func TestNormalizeSettingValue_Newlines(t *testing.T) {
	for _, c := range []struct{ key, in, want string }{
		{"home_about", "a\r\nb\r\n\r\nc", "a\nb\n\nc"},
		{"home_about", "a\rb", "a\nb"},
		{"home_title", "x\r\ny", "x\ny"},
		{"home_mode", "a\r\nb", "a\r\nb"}, // only free-text keys are touched
	} {
		if got := normalizeSettingValue(c.key, c.in); got != c.want {
			t.Errorf("normalizeSettingValue(%q, %q) = %q, want %q", c.key, c.in, got, c.want)
		}
	}
}

// U+200D (ZWJ) joins emoji sequences, so it is the one Cf character allowed.
func TestValidHomeText_AllowsZWJEmoji(t *testing.T) {
	family := "family \U0001F468\u200D\U0001F469\u200D\U0001F467"
	if err := validHomeText("home_about", family, 2000, true); err != nil {
		t.Errorf("ZWJ emoji sequence rejected: %v", err)
	}
	if err := validHomeText("home_about", "zero\u200cwidth", 2000, true); err == nil {
		t.Error("ZWNJ (U+200C) accepted, want rejected")
	}
}
