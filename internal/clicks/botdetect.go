package clicks

import "strings"

// BotUserAgentSubstrings is the maintained list of substrings checked against
// a click's User-Agent header to classify it as automated traffic — crawlers,
// chat-app link-preview fetchers, monitoring/headless browsers, and common
// scripting HTTP clients — rather than a human following the link.
//
// This is the ONE place the list lives (#0101's explicit requirement): it is
// exported so it is unit-testable on its own, and separated from the recorder
// so editing it as crawlers evolve is a small, obvious, low-risk diff rather
// than a change buried inside SQL-adjacent plumbing. IsBot below is the only
// thing that reads it; nothing else should duplicate or inline a copy.
//
// Entries are matched case-insensitively as plain substrings (see IsBot), so
// casing here is cosmetic — it just documents each token's conventional form.
// A few entries (e.g. "Slackbot") are already covered by the bare "bot"
// substring; they are kept anyway so the list documents each known crawler by
// name rather than relying on readers to notice they're subsumed, and so a
// future removal of the generic "bot" entry does not silently stop catching
// them.
var BotUserAgentSubstrings = []string{
	"bot",
	"crawler",
	"spider",
	"preview",
	"facebookexternalhit",
	"Slackbot",
	"Twitterbot",
	"Discordbot",
	"WhatsApp",
	"TelegramBot",
	"LinkedInBot",
	"headless",
	"curl",
	"wget",
	"python-requests",
}

// NotBotUserAgentSubstrings is a small, maintained exception list checked
// BEFORE BotUserAgentSubstrings: a User-Agent matching any entry here is
// never classified as a bot, regardless of what else it contains.
//
// It exists because the generic "bot" substring — which is load-bearing for
// catching unenumerated/future crawlers by name (bingbot, AhrefsBot,
// SemrushBot, ...) and cannot simply be removed — collides with real device
// model names that happen to contain the letters "bot". CUBOT is a real,
// shipping budget Android phone brand whose model name appears in the
// device field of an entirely ordinary Chrome-on-Android User-Agent, e.g.
// "Mozilla/5.0 (Linux; Android 10; CUBOT_X19 Build/QP1A.190711.020)
// AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Mobile
// Safari/537.36". A word-boundary rule does not fix this either, since
// legitimate bot UAs like "Googlebot" also have a letter immediately before
// "bot" — the collision is genuinely a substring, not a tokenization
// problem, and the fix is a name-specific exception, not a smarter matcher.
//
// The asymmetry that justifies erring toward this list rather than toward
// the bot list: a false positive here permanently erases a real human
// click from every stat with no way to recover it, while a false negative
// (an unenumerated crawler slipping through) only leaves the residual
// inflation the issue already documents as an accepted limitation of
// substring-based filtering.
var NotBotUserAgentSubstrings = []string{
	"cubot",
}

// IsBot reports whether userAgent identifies automated/crawler traffic,
// matched case-insensitively as a substring against BotUserAgentSubstrings —
// unless it first matches NotBotUserAgentSubstrings, which wins regardless
// of what else the UA contains (see its doc comment for why).
//
// An empty/absent user agent (userAgent == "") is deliberately classified as
// NOT a bot. This is a considered policy choice, not an oversight: an absent
// UA is missing evidence, not evidence of automation. Treating "no data" as
// "bot" would misclassify any legitimate client that omits or strips the
// header — some minimal HTTP libraries and privacy-conscious proxies do —
// while the crawlers this list actually targets (chat-app link previewers,
// social-platform fetchers, curl/wget/python-requests) all send a
// self-identifying UA by default. A bot sophisticated enough to send no UA at
// all, or to impersonate a browser UA, already evades substring matching
// regardless of this choice, so the empty-string case is not where the real
// detection gap is. This is also the same false-positive-is-worse-than-
// false-negative asymmetry that justifies NotBotUserAgentSubstrings above: a
// misclassified human click is unrecoverable, while under-catching a bot
// only leaves known, documented residual inflation.
func IsBot(userAgent string) bool {
	if userAgent == "" {
		return false
	}
	lower := strings.ToLower(userAgent)
	for _, s := range NotBotUserAgentSubstrings {
		if strings.Contains(lower, strings.ToLower(s)) {
			return false
		}
	}
	for _, s := range BotUserAgentSubstrings {
		if strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}
