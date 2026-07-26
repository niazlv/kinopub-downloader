package domain

import (
	"net/url"
	"regexp"
	"strings"
)

// The service changes domains (kino.pub → kino.watch) and is reachable through
// mirrors, so no host is baked into the request path. The Site a run targets is
// derived from the URL the user passes and carried through feed construction,
// the Referer header, and cookie scoping.

// DefaultSiteHost is the site assumed when there is no URL to derive one from,
// e.g. `kinopub login` or a run driven entirely by --feed-file.
const DefaultSiteHost = "kino.watch"

// KnownSiteHosts lists hosts the service is known to serve from, newest first.
// It only seeds defaults for commands that have no URL to work from (cookie
// loading, cookie scoping in library callers) — any other host, including a
// mirror never seen before, is accepted just as well.
var KnownSiteHosts = []string{"kino.watch", "kino.pub"}

// Site is the origin a run targets: the host whose pages, podcast feeds and
// session cookies the run works with. The zero Site means DefaultSiteHost over
// https.
type Site struct {
	Scheme string // "https" when empty
	Host   string // host, optionally with port; DefaultSiteHost when empty
}

// SiteFromURL derives the Site from a page or feed URL. A URL with no usable
// host yields the zero Site, i.e. the default site.
func SiteFromURL(rawURL string) Site {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return Site{}
	}
	return Site{Scheme: u.Scheme, Host: u.Host}
}

// SiteFromHost builds a Site from a bare host such as a --site value. A full
// URL is tolerated so users can paste either form.
func SiteFromHost(host string) Site {
	host = strings.TrimSpace(host)
	if strings.Contains(host, "://") {
		return SiteFromURL(host)
	}
	return Site{Host: strings.Trim(host, "/")}
}

// IsZero reports whether the Site carries no host, meaning the caller never
// learned which site the run targets.
func (s Site) IsZero() bool { return strings.TrimSpace(s.Host) == "" }

// resolve fills in the defaults, so callers never have to.
func (s Site) resolve() Site {
	host := strings.ToLower(strings.TrimSpace(s.Host))
	if host == "" {
		host = DefaultSiteHost
	}
	scheme := strings.ToLower(strings.TrimSpace(s.Scheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return Site{Scheme: scheme, Host: host}
}

// String returns the site host, e.g. "kino.watch".
func (s Site) String() string { return s.resolve().Host }

// Origin returns the scheme+host prefix, e.g. "https://kino.watch".
func (s Site) Origin() string {
	r := s.resolve()
	return r.Scheme + "://" + r.Host
}

// Referer returns the value for the Referer header. The CDN requires it and
// stalls the connection when it is missing.
func (s Site) Referer() string { return s.Origin() + "/" }

// PodcastFeedURL builds the tokenized RSS feed URL for a podcast id and token.
func (s Site) PodcastFeedURL(id, token string) string {
	return s.Origin() + "/podcast/get/" + id + "/" + token
}

// seriesIDPathRe matches the id in the two URL shapes that carry one: a page
// link (/item/view/38290, optionally with a slug) and a podcast feed
// (/podcast/get/38290/<token>). Both use the same numeric series id.
var seriesIDPathRe = regexp.MustCompile(`^/(?:item/view|podcast/get)/(\d+)(?:/[^/]*)?$`)

// SeriesIDFromURL extracts the series id from a page or feed URL, or returns an
// empty string when the URL carries none. It is the offline counterpart to
// resolving the input over the network: the id keys the download state, so a
// caller that cannot reach the site can still resume an existing series instead
// of treating it as new.
func SeriesIDFromURL(rawURL string) SeriesID {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	m := seriesIDPathRe.FindStringSubmatch(strings.TrimRight(u.Path, "/"))
	if m == nil {
		return ""
	}
	return SeriesID(m[1])
}

// Owns reports whether rawHost belongs to this site, i.e. it is the site host
// itself or a subdomain of it. Session cookies are only ever sent to hosts the
// site owns — never to the CDN, which throttles or stalls requests carrying
// them. rawHost may include a port.
func (s Site) Owns(rawHost string) bool {
	h := hostOnly(rawHost)
	if h == "" {
		return false
	}
	site := hostOnly(s.resolve().Host)
	return h == site || strings.HasSuffix(h, "."+site)
}

// AnyKnownSiteOwns reports whether rawHost belongs to one of KnownSiteHosts.
// It is the fallback for callers that were never told which site a run targets
// (Site is zero) but still must decide whether a request may carry cookies.
func AnyKnownSiteOwns(rawHost string) bool {
	for _, known := range KnownSiteHosts {
		if (Site{Host: known}).Owns(rawHost) {
			return true
		}
	}
	return false
}

// hostOnly lowercases a host and strips any :port suffix.
func hostOnly(rawHost string) string {
	h := strings.ToLower(strings.TrimSpace(rawHost))
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], "]") {
		h = h[:i]
	}
	return strings.Trim(h, "[]")
}
