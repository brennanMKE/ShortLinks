package reports

import (
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// ContentKey returns the canonical key of a destination URL, the identity two
// links share when they point at the same content: the scheme, every utm_*
// parameter, the fragment and any trailing slash are dropped and the host is
// lower-cased. Other query parameters are kept, sorted by name, because they
// can select different content (?id=7). Two links share a group if and only if
// their keys match. A value that does not parse as a URL is returned trimmed.
//
//	https://WWW.Example.com/guides/x/?utm_source=a#top -> www.example.com/guides/x
func ContentKey(dest string) string {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return ""
	}
	u, err := url.Parse(dest)
	if err == nil && u.Opaque != "" && !strings.HasPrefix(u.Opaque, "//") && !startsWithDigit(u.Opaque) {
		return dest // mailto:, tel: and similar have no host or path to group on
	}
	if err != nil || u.Host == "" {
		// Scheme-less ("example.com/x") values parse with the host in Path.
		u2, err2 := url.Parse("//" + dest)
		if err2 != nil || u2.Host == "" {
			return dest
		}
		u = u2
	}
	host := strings.ToLower(u.Host)
	switch strings.ToLower(u.Scheme) {
	case "https":
		host = strings.TrimSuffix(host, ":443")
	case "http":
		host = strings.TrimSuffix(host, ":80")
	}
	key := host + strings.TrimRight(u.EscapedPath(), "/")
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") {
			q.Del(k)
		}
	}
	if len(q) > 0 {
		key += "?" + q.Encode() // Encode sorts by key
	}
	return key
}

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

// ContentPath returns the destination's path for display: "/guides/x" with the
// trailing slash removed, or "/" for the site root.
func ContentPath(dest string) string {
	if u, err := url.Parse(strings.TrimSpace(dest)); err == nil && u.Opaque != "" && !startsWithDigit(u.Opaque) {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil || u.Host == "" {
		u2, err2 := url.Parse("//" + strings.TrimSpace(dest))
		if err2 != nil || u2.Host == "" {
			return ""
		}
		u = u2
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return "/"
	}
	return p
}

// titleSeparators are tried at their last occurrence in the title.
var titleSeparators = []string{" — ", " – ", " | ", " - "}

// channelPlatforms are the words that mark a title tail as a channel label.
var channelPlatforms = []string{
	"mastodon", "bluesky", "instagram", "facebook", "threads", "x", "twitter",
	"linkedin", "newsletter", "email", "poster", "flyer", "qr",
}

// channelWords are generic channel/format words allowed in a label tail.
var channelWords = map[string]bool{
	"guide": true, "post": true, "posts": true, "flyer": true, "poster": true, "qr": true,
	"newsletter": true, "link": true, "story": true, "reel": true, "thread": true,
	"issue": true, "edition": true,
}

// dateWords are month names (full and abbreviated) allowed in a label tail.
var dateWords = map[string]bool{
	"jan": true, "january": true, "feb": true, "february": true, "mar": true, "march": true,
	"apr": true, "april": true, "may": true, "jun": true, "june": true, "jul": true, "july": true,
	"aug": true, "august": true, "sep": true, "sept": true, "september": true, "oct": true,
	"october": true, "nov": true, "november": true, "dec": true, "december": true,
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 && unicode.IsLetter(r))
	})
}

func isNumber(t string) bool {
	for _, c := range t {
		if c < '0' || c > '9' {
			return false
		}
	}
	return t != ""
}

// looksLikeChannel is true when every token of the tail is a channel token (a
// platform word, the link's own utm_source, a channel word, a number, "no", a
// month name) and at least one is a platform/source or channel word.
func looksLikeChannel(tail, utmSource string) bool {
	src := map[string]bool{}
	for _, t := range tokens(utmSource) {
		src[t] = true
	}
	plat := map[string]bool{}
	for _, p := range channelPlatforms {
		plat[p] = true
	}
	strong := false
	toks := tokens(tail)
	for _, t := range toks {
		switch {
		case plat[t] || src[t] || channelWords[t]:
			strong = true
		case isNumber(t) || t == "no" || dateWords[t]:
		default:
			return false
		}
	}
	return strong && len(toks) > 0
}

// LinkLabel cuts a link title at its last " — ", " – ", " | " or " - " when the
// whole tail is channel tokens: platform words, the link's own utmSource,
// channel words (guide, post, flyer, ...), numbers, "No" and month names, with
// at least one platform/source or channel word. It returns the head (the
// content title) and the cut-off tail (the link's label). A title with no such
// tail is returned whole with an empty label. It depends only on the link's own
// fields, never on the window or filters.
func LinkLabel(title, utmSource string) (head, label string) {
	title = strings.TrimRight(title, " \t\r\n")
	best, sepLen := -1, 0
	for _, sep := range titleSeparators {
		if i := strings.LastIndex(title, sep); i > best {
			best, sepLen = i, len(sep)
		}
	}
	if best < 0 {
		return strings.TrimSpace(title), ""
	}
	tail := strings.TrimSpace(title[best+sepLen:])
	if !looksLikeChannel(tail, utmSource) {
		return strings.TrimSpace(title), ""
	}
	return strings.TrimSpace(title[:best]), tail
}

// TitleInput is one member link's contribution to a content title.
type TitleInput struct {
	Title     string
	UTMSource string
}

// ContentTitle returns the display title of a content group: the most common
// non-empty head of the members' titles (ties go to the lexically smaller
// head, so the result is deterministic). When no member has a head it falls
// back to the last path segment of destination with dashes and underscores
// turned into spaces, then to the host.
func ContentTitle(members []TitleInput, destination string) string {
	counts := map[string]int{}
	for _, m := range members {
		if h, _ := LinkLabel(m.Title, m.UTMSource); h != "" {
			counts[h]++
		}
	}
	if len(counts) > 0 {
		heads := make([]string, 0, len(counts))
		for h := range counts {
			heads = append(heads, h)
		}
		sort.Slice(heads, func(i, j int) bool {
			if counts[heads[i]] != counts[heads[j]] {
				return counts[heads[i]] > counts[heads[j]]
			}
			return heads[i] < heads[j]
		})
		return heads[0]
	}
	return pathTitle(destination)
}

// pathTitle humanises the last path segment of dest, or returns its host.
func pathTitle(dest string) string {
	u, err := url.Parse(strings.TrimSpace(dest))
	if err != nil || u.Host == "" {
		u, err = url.Parse("//" + strings.TrimSpace(dest))
		if err != nil {
			return ""
		}
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if seg := segs[len(segs)-1]; seg != "" {
		return strings.TrimSpace(strings.NewReplacer("-", " ", "_", " ").Replace(seg))
	}
	return u.Host
}
