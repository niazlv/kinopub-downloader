// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package credstore

import (
	"sort"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// SiteSession is one site's website login: the Cookie header and the browser
// User-Agent it was issued to.
type SiteSession struct {
	Cookie    string    `json:"cookie"`
	UserAgent string    `json:"user_agent,omitempty"`
	SavedAt   time.Time `json:"saved_at,omitempty"`
}

// NamedSession is a SiteSession with the site it belongs to, for listing.
type NamedSession struct {
	Site string
	SiteSession
}

// SiteKey normalises a host into the key its login is stored under: lowercase,
// without a port, scheme or path a pasted value may carry. Empty stays empty —
// it is the key of a legacy login whose site was never recorded.
func SiteKey(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return ""
	}
	h = domain.SiteFromHost(h).String()
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], "]") {
		h = h[:i]
	}
	return h
}

// sessions is every website login, the pre-Sites slot included, keyed by site
// ("" for a legacy login whose site was never recorded). It reads both places
// so a Credentials value works the same whether it came from a file written
// before Sites existed or was built in memory by a test.
func (c Credentials) sessions() map[string]SiteSession {
	out := make(map[string]SiteSession, len(c.Sites)+1)
	for key, s := range c.Sites {
		if s.Cookie != "" {
			out[SiteKey(key)] = s
		}
	}
	if c.Cookie != "" {
		key := SiteKey(c.Site)
		if _, ok := out[key]; !ok {
			out[key] = SiteSession{Cookie: c.Cookie, UserAgent: c.UserAgent, SavedAt: c.CookieSavedAt}
		}
	}
	return out
}

// SessionFor returns the website login usable on the target site: the one
// saved for that host, or for a parent site of it (a login for kino.watch
// serves www.kino.watch). A legacy login with no site recorded serves any host
// the service is known by. The second result is the site it was saved under.
//
// Nothing else ever matches: the target host is whatever the user-supplied URL
// names, so a link naming an unrelated host — a "mirror", say — must not
// receive another site's session.
func (c Credentials) SessionFor(target domain.Site) (SiteSession, string, bool) {
	all := c.sessions()
	host := target.String()
	if s, ok := all[SiteKey(host)]; ok {
		return s, SiteKey(host), true
	}
	best := ""
	for key := range all {
		if key != "" && domain.SiteFromHost(key).Owns(host) && len(key) > len(best) {
			best = key
		}
	}
	if best != "" {
		return all[best], best, true
	}
	if s, ok := all[""]; ok && domain.AnyKnownSiteOwns(host) {
		return s, "", true
	}
	return SiteSession{}, "", false
}

// HasCookieFor reports whether a website login is stored for the target site.
func (c Credentials) HasCookieFor(target domain.Site) bool {
	_, _, ok := c.SessionFor(target)
	return ok
}

// Sessions lists the website logins, sites in alphabetical order.
func (c Credentials) Sessions() []NamedSession {
	all := c.sessions()
	out := make([]NamedSession, 0, len(all))
	for key, s := range all {
		out = append(out, NamedSession{Site: key, SiteSession: s})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

// SiteHosts lists the sites a website login is stored for, for messages.
func (c Credentials) SiteHosts() []string {
	sessions := c.Sessions()
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		site := s.Site
		if site == "" {
			site = strings.Join(domain.KnownSiteHosts, " or ") + " (site not recorded at login)"
		}
		out = append(out, site)
	}
	return out
}

// SetSession stores the website login for one site, replacing that site's
// only. Logins are held per site: signing in to a platform built on this tool
// must not evict the kino.pub session that is used beside it.
func (c *Credentials) SetSession(host string, s SiteSession) {
	c.normalize()
	if c.Sites == nil {
		c.Sites = make(map[string]SiteSession)
	}
	c.Sites[SiteKey(host)] = s
	c.normalize()
}

// RemoveSession drops one site's website login and reports whether there was
// one. The host may name the site exactly or a host that site owns.
func (c *Credentials) RemoveSession(host string) bool {
	c.normalize()
	_, key, ok := c.SessionFor(domain.SiteFromHost(host))
	if !ok {
		return false
	}
	delete(c.Sites, key)
	// The mirror still holds the login just removed; left in place it would
	// be folded straight back in.
	c.Cookie, c.UserAgent, c.Site, c.CookieSavedAt = "", "", "", time.Time{}
	c.normalize()
	return true
}

// normalize folds the legacy slot into Sites and mirrors the kino.pub login
// back into it. Every write goes through here, so the two never disagree.
//
// The mirror is for a downgrade: an older build looks for its cookie in the
// top-level fields and finds the same one there, while a newer build reads
// Sites first and ignores the copy.
func (c *Credentials) normalize() {
	all := c.sessions()
	if s, ok := all[""]; ok {
		// A login saved before the site was recorded belonged to the service
		// under whatever name it had at the time; file it under the current
		// one. From here on every login has a site.
		delete(all, "")
		if _, taken := all[domain.DefaultSiteHost]; !taken {
			all[domain.DefaultSiteHost] = s
		}
	}
	if len(all) == 0 {
		all = nil
	}
	c.Sites = all

	c.Cookie, c.UserAgent, c.Site, c.CookieSavedAt = "", "", "", time.Time{}
	if s, key, ok := c.SessionFor(domain.Site{}); ok {
		c.Cookie, c.UserAgent, c.Site, c.CookieSavedAt = s.Cookie, s.UserAgent, key, s.SavedAt
	}
}
