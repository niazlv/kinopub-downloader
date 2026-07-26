// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package httpx provides HTTP client helpers, notably an auth-injecting
// transport that applies a Cookie header, a User-Agent, and arbitrary extra
// headers to every outbound request. This lets the tool reuse a logged-in
// browser session to pass Cloudflare and the site's authentication.
package httpx

import (
	"net/http"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// authTransport wraps a base RoundTripper and injects authentication headers
// (Cookie, User-Agent, and any extra headers) into every request that does not
// already set them. The Cookie is the exception: it is only injected for hosts
// the site owns — see RoundTrip.
type authTransport struct {
	base http.RoundTripper
	auth domain.RequestAuth

	// cookieAnywhere lifts the site scoping for clients that fetch HLS from the
	// CDN, which gates access on cf_clearance and returns 403 without it. See
	// WithAuthCDNCookies.
	cookieAnywhere bool
}

// RoundTrip implements http.RoundTripper. It clones the request before mutating
// headers so it never modifies the caller's request (per the RoundTripper
// contract).
//
// The Cookie is scoped to the site, mirroring ChunkedDownloader.applyAuth: the
// session cookie is never sent to the CDN, which throttles or stalls requests
// carrying it, nor to any unrelated third party. The gate is re-evaluated on
// every hop because http.Client re-runs the RoundTripper for each redirect with
// the new host, so a site → CDN redirect drops the cookie exactly where it must.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())

	if t.auth.Cookie != "" && r.Header.Get("Cookie") == "" && t.cookieAllowed(r.URL.Host) {
		r.Header.Set("Cookie", t.auth.Cookie)
	}
	if t.auth.UserAgent != "" {
		// Always set the User-Agent: Cloudflare's cf_clearance cookie is bound
		// to the UA that solved the challenge, so it must match exactly.
		r.Header.Set("User-Agent", t.auth.UserAgent)
	}
	for k, v := range t.auth.Headers {
		if r.Header.Get(k) == "" {
			r.Header.Set(k, v)
		}
	}

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// cookieAllowed reports whether host is the site the run targets (or one of its
// subdomains) and may therefore receive the session Cookie. When the caller
// never told us which site is in play, any known site host is accepted so a
// mirror is still authenticated rather than silently downgraded.
func (t *authTransport) cookieAllowed(host string) bool {
	if t.cookieAnywhere {
		return true
	}
	if t.auth.Site.IsZero() {
		return domain.AnyKnownSiteOwns(host)
	}
	return t.auth.Site.Owns(host)
}

// WithAuth returns a copy of client whose transport injects the given auth into
// every request, with the Cookie scoped to auth.Site. If auth is empty, the
// original client is returned unchanged. The base client's existing transport
// (e.g., a proxy transport) is preserved and wrapped.
func WithAuth(client *http.Client, auth domain.RequestAuth) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if auth.IsZero() {
		return client
	}

	wrapped := *client // shallow copy so we don't mutate the caller's client
	wrapped.Transport = &authTransport{
		base: client.Transport,
		auth: auth,
	}
	return &wrapped
}

// WithAuthCDNCookies is WithAuth without the site scoping: the Cookie is sent to
// every host the client talks to. It exists for the HLS paths, where the CDN
// itself gates access on the cf_clearance cookie and answers 403 without it —
// the opposite of progressive downloads, where the same cookie causes
// throttling (see ChunkedDownloader.applyAuth and applyHLSAuth).
//
// Prefer WithAuth. Use this only for a client whose requests all target media
// the site published, never for one that follows arbitrary URLs.
func WithAuthCDNCookies(client *http.Client, auth domain.RequestAuth) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if auth.IsZero() {
		return client
	}

	wrapped := *client
	wrapped.Transport = &authTransport{
		base:           client.Transport,
		auth:           auth,
		cookieAnywhere: true,
	}
	return &wrapped
}
