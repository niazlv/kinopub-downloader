// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package browsercookies reads cookies for a given domain from local browser
// cookie stores (Safari, Chrome, Firefox) and assembles a Cookie header value.
//
// This is a best-effort convenience: browser cookie stores are
// vaguely-documented, OS-specific, and may be encrypted (Chrome on macOS
// requires Keychain access). Failures are returned as errors so callers can
// fall back to an explicitly supplied --cookie value.
package browsercookies

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/browserutils/kooky"
	_ "github.com/browserutils/kooky/browser/all" // register cookie store finders
)

// Supported browser identifiers accepted by Load.
const (
	BrowserAuto    = "auto"
	BrowserSafari  = "safari"
	BrowserChrome  = "chrome"
	BrowserFirefox = "firefox"
)

// Load reads cookies from the named browser for the first of domainSuffixes
// that has any, and returns a Cookie header value of the form
// "name1=value1; name2=value2" together with the domain it came from.
//
// Several suffixes may be passed because the site is reachable under more than
// one domain (a rename or a mirror): they are tried in order, so put the most
// likely one first. Cookies are never merged across domains — a session belongs
// to exactly one of them.
//
// browser may be one of "safari", "chrome", "firefox", or "auto" (try all
// registered browsers). An empty browser string is treated as "auto".
// Returns an error if no cookies are found or the store cannot be read.
func Load(browser string, domainSuffixes ...string) (cookie, domain string, err error) {
	if browser == "" {
		browser = BrowserAuto
	}
	browser = strings.ToLower(strings.TrimSpace(browser))

	suffixes := make([]string, 0, len(domainSuffixes))
	for _, d := range domainSuffixes {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d != "" {
			suffixes = append(suffixes, d)
		}
	}
	if len(suffixes) == 0 {
		return "", "", fmt.Errorf("no cookie domain given")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Traverse all valid cookies (no domain filter) so we can tell apart two
	// failure modes: a store we could not read at all (zero cookies seen) vs a
	// store we read that simply has no cookie for the target domains. The
	// traversal is slow, so all suffixes are matched in this single pass.
	type entry struct {
		value   string
		expires time.Time
	}
	collected := make(map[string]map[string]entry, len(suffixes))

	var totalSeen, browserSeen int

	seq := kooky.TraverseCookies(ctx, kooky.Valid).OnlyCookies()
	for ck := range seq {
		totalSeen++
		if browser != BrowserAuto && !browserMatches(browser, ck) {
			continue
		}
		browserSeen++

		if ck.Name == "" {
			continue
		}
		dom := strings.ToLower(strings.TrimPrefix(ck.Domain, "."))
		for _, suffix := range suffixes {
			if dom != suffix && !strings.HasSuffix(dom, "."+suffix) {
				continue
			}
			forSuffix := collected[suffix]
			if forSuffix == nil {
				forSuffix = make(map[string]entry)
				collected[suffix] = forSuffix
			}
			prev, ok := forSuffix[ck.Name]
			// Prefer the cookie with the later expiry (more recently issued).
			if !ok || ck.Expires.After(prev.expires) {
				forSuffix[ck.Name] = entry{value: ck.Value, expires: ck.Expires}
			}
		}
	}

	// First suffix with a session wins, so callers can express a preference.
	for _, suffix := range suffixes {
		found := collected[suffix]
		if len(found) == 0 {
			continue
		}
		// Build a deterministic Cookie header (sorted by name).
		names := make([]string, 0, len(found))
		for n := range found {
			names = append(names, n)
		}
		sort.Strings(names)

		var b strings.Builder
		for i, n := range names {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(n)
			b.WriteByte('=')
			b.WriteString(found[n].value)
		}
		return b.String(), suffix, nil
	}

	return "", "", notFoundError(browser, suffixes, totalSeen, browserSeen)
}

// notFoundError builds a descriptive error explaining why no cookies were found,
// including a macOS Full Disk Access hint when no store could be read at all.
func notFoundError(browser string, suffixes []string, totalSeen, browserSeen int) error {
	domains := strings.Join(suffixes, ", ")
	switch {
	case totalSeen == 0:
		return fmt.Errorf(
			"could not read any browser cookie store (0 cookies). On macOS the cookie " +
				"files are protected: grant your terminal Full Disk Access in System " +
				"Settings \u2192 Privacy & Security \u2192 Full Disk Access, then retry. " +
				"Alternatively pass --cookie/--user-agent manually")
	case browser != BrowserAuto && browserSeen == 0:
		return fmt.Errorf(
			"read cookies from other browsers but none from %q (its store may be "+
				"locked, encrypted, or in a non-default profile). Try --browser-cookies "+
				"without a value to search all browsers, or pass --cookie manually", browser)
	default:
		return fmt.Errorf(
			"no cookies for %s found in browser %q \u2014 make sure you are logged in to "+
				"the site in that browser. If it now serves from another domain, name it "+
				"with --site, or pass --cookie manually", domains, browser)
	}
}

// browserMatches reports whether a cookie originates from the requested browser.
// kooky exposes the source browser via Cookie.Browser.Browser(); we match on
// its name case-insensitively and tolerate the info being absent by matching all.
func browserMatches(browser string, cookie *kooky.Cookie) bool {
	if cookie == nil {
		return false
	}
	src := cookie.Browser
	if src == nil {
		return true
	}
	name := strings.ToLower(src.Browser())
	return strings.Contains(name, browser)
}
