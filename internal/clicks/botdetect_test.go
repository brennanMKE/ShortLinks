package clicks

import "testing"

// TestIsBot_EachSubstringMatchesRealisticUA asserts every entry in
// BotUserAgentSubstrings actually classifies a click when it appears inside a
// realistic full user-agent string — not the bare token in isolation, which
// would pass even if the matching logic were broken (e.g. requiring an exact
// match rather than a substring). Each case below is a real or
// real-shaped UA string a crawler/bot of that kind actually sends.
func TestIsBot_EachSubstringMatchesRealisticUA(t *testing.T) {
	cases := []struct {
		substring string
		ua        string
	}{
		{"bot", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"},
		{"crawler", "Mozilla/5.0 (compatible; ExampleCrawler/2.3; +https://example.com/info)"},
		{"spider", "Mozilla/5.0 (compatible; YodaoSpider; +http://www.yodao.com/help/webmaster/spider/)"},
		{"preview", "Mozilla/5.0 (compatible; OpenGraphPreviewFetcher/1.0; +https://example.com/about)"},
		{"facebookexternalhit", "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"},
		{"Slackbot", "Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)"},
		{"Twitterbot", "Twitterbot/1.0"},
		{"Discordbot", "Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)"},
		{"WhatsApp", "WhatsApp/2.19.81 A"},
		{"TelegramBot", "TelegramBot (like TwitterBot)"},
		{"LinkedInBot", "LinkedInBot/1.0 (compatible; Mozilla/5.0; Jakarta Commons-HttpClient/3.1 +http://www.linkedin.com)"},
		{"headless", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/109.0.5414.74 Safari/537.36"},
		{"curl", "curl/7.79.1"},
		{"wget", "Wget/1.21.1 (linux-gnu)"},
		{"python-requests", "python-requests/2.28.1"},
	}

	// Sanity: this test must actually cover the full list, so a substring
	// added to BotUserAgentSubstrings without a matching case here fails
	// loudly instead of silently going untested.
	if len(cases) != len(BotUserAgentSubstrings) {
		t.Fatalf("test cases = %d, BotUserAgentSubstrings = %d — every list entry needs a realistic-UA case",
			len(cases), len(BotUserAgentSubstrings))
	}

	for _, c := range cases {
		t.Run(c.substring, func(t *testing.T) {
			if !IsBot(c.ua) {
				t.Errorf("IsBot(%q) = false, want true (realistic UA for substring %q)", c.ua, c.substring)
			}
		})
	}
}

// TestIsBot_CaseInsensitive is the explicit acceptance-criterion case:
// SlackBot, slackbot, SLACKBOT must all match regardless of the casing used
// by BotUserAgentSubstrings itself, using full realistic UA strings (not the
// bare token) in each casing.
func TestIsBot_CaseInsensitive(t *testing.T) {
	cases := []string{
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)", // as stored
		"slackbot-linkexpanding 1.0 (+https://api.slack.com/robots)", // all lower
		"SLACKBOT-LINKEXPANDING 1.0 (+HTTPS://API.SLACK.COM/ROBOTS)", // all upper
		"sLaCkBoT-LinkExpanding 1.0 (+https://api.slack.com/robots)", // mixed
	}
	for _, ua := range cases {
		t.Run(ua, func(t *testing.T) {
			if !IsBot(ua) {
				t.Errorf("IsBot(%q) = false, want true (case-insensitive match on Slackbot)", ua)
			}
		})
	}
}

// TestIsBot_EmptyOrAbsentUserAgentIsNotBot asserts the deliberate policy
// documented on IsBot: an empty/absent User-Agent is missing evidence, not
// evidence of automation, and must NOT classify as a bot.
func TestIsBot_EmptyOrAbsentUserAgentIsNotBot(t *testing.T) {
	if IsBot("") {
		t.Error("IsBot(\"\") = true, want false — absent UA must not classify as a bot")
	}
}

// TestIsBot_RealBrowsersNotFlagged asserts ordinary human traffic — the
// actual clients this app sees per the issue's explicit callout — is never
// misclassified as a bot. The three CUBOT cases are the review's confirmed
// false positive: CUBOT is a real, shipping budget Android phone brand whose
// model name (containing "bot") appears in the device field of an otherwise
// completely ordinary Chrome-on-Android UA, and would be caught by the bare
// "bot" substring without NotBotUserAgentSubstrings.
func TestIsBot_RealBrowsersNotFlagged(t *testing.T) {
	cases := map[string]string{
		"Safari on iOS":                        "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
		"Chrome on macOS":                      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Android System WebView":               "Mozilla/5.0 (Linux; Android 10; SM-G960F Build/QP1A.190711.020; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/79.0.3945.116 Mobile Safari/537.36",
		"iOS in-app WebView (no Safari token)": "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148",
		"CUBOT X19 Chrome on Android":          "Mozilla/5.0 (Linux; Android 10; CUBOT_X19 Build/QP1A.190711.020) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Mobile Safari/537.36",
		"CUBOT MAGIC Chrome on Android":        "Mozilla/5.0 (Linux; Android 9; CUBOT MAGIC) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/70.0.3538.110 Mobile Safari/537.36",
		"CUBOT NOTE 7 Chrome on Android":       "Mozilla/5.0 (Linux; Android 11; CUBOT NOTE 7 Build/RP1A.201005.001) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/95.0.4638.74 Mobile Safari/537.36",
	}
	for name, ua := range cases {
		t.Run(name, func(t *testing.T) {
			if IsBot(ua) {
				t.Errorf("IsBot(%q) = true, want false for a real browser UA (%s)", ua, name)
			}
		})
	}
}
