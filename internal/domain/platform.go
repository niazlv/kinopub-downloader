// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A platform is a site built on this tool's download-manifest contract (see
// manifestscraper): it resolves the source itself and serves media through its
// own gateway, so a run against it needs that site's session and nothing from
// kino.pub.
//
// Its title page is addressed by the URL fragment, because the platform routes
// in the browser: "#/title/201" is the title, "#/title/201/s1e3" one episode
// by season and number — the address the page itself shows while playing, so
// the one a person copies — and "#/title/201/1234" the same by the platform's
// episode id, which older links and bookmarks still carry. A season alone
// ("s2") is accepted the way it is for kino.pub links. A fragment never
// reaches a server, but it is still part of the string the user copies, and
// that is what the link is recognised by. The same shape in the path
// ("/title/201") is accepted in case the routing ever moves there. Digits are
// bounded so an arbitrarily long run cannot be read as an id.
var platformTitleRe = regexp.MustCompile(`(?i)^/?title/(\d{1,18})(?:/(\d{1,18})|/s(\d{1,3})(?:e(\d{1,4}))?)?/?$`)

// PlatformLink is a parsed platform title page link.
type PlatformLink struct {
	// Origin is where the platform lives — scheme, host and, if it is served
	// under a path, that path without a trailing slash. Its API hangs off it.
	Origin string
	// TitleID is the platform's own id of the title, not a kino.pub item id.
	TitleID int64
	// EpisodeID is the platform's own id of the episode the link names, or
	// zero when it names the episode by season and number, or the whole title.
	EpisodeID int64
	// Ref is the season and episode the link names ("s1e3"), or the season
	// alone ("s2"); zero when the link names the title or an episode by id.
	Ref EpisodeRef
}

// ParsePlatformLink recognises a platform title page link. The second result
// is false for anything else, including kino.pub links and platform pages that
// are not a title page (search, history, …).
func ParsePlatformLink(rawURL string) (PlatformLink, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return PlatformLink{}, false
	}

	// With fragment routing the path is the app shell's location: "/" for a
	// platform at the root, "/kino/" or "/kino/index.html" for one under a
	// path. Its directory is the origin's path part.
	base := u.Path
	if !strings.HasSuffix(base, "/") {
		base = base[:strings.LastIndex(base, "/")+1]
	}
	base = strings.TrimSuffix(base, "/")

	m := platformTitleRe.FindStringSubmatch(u.Fragment)
	if m == nil {
		m = platformTitleRe.FindStringSubmatch(u.Path)
		base = ""
	}
	if m == nil {
		return PlatformLink{}, false
	}

	title, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || title <= 0 {
		return PlatformLink{}, false
	}
	link := PlatformLink{Origin: u.Scheme + "://" + u.Host + base, TitleID: title}
	switch {
	case m[2] != "":
		if link.EpisodeID, err = strconv.ParseInt(m[2], 10, 64); err != nil || link.EpisodeID <= 0 {
			return PlatformLink{}, false
		}
	case m[3] != "":
		// The regexp guarantees digits; a zero-padded "s01e03" reads the same
		// as "s1e3". Season zero names nothing anyone can download.
		link.Ref.Season, _ = strconv.Atoi(m[3])
		link.Ref.Episode, _ = strconv.Atoi(m[4])
		if link.Ref.Season == 0 || (m[4] != "" && link.Ref.Episode == 0) {
			return PlatformLink{}, false
		}
	}
	return link, true
}
