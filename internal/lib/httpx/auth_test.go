// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func TestWithAuth_InjectsHeaders(t *testing.T) {
	var gotCookie, gotUA, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		gotUA = r.Header.Get("User-Agent")
		gotExtra = r.Header.Get("X-Test")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	auth := domain.RequestAuth{
		Cookie:    "cf_clearance=abc; PHPSESSID=xyz",
		UserAgent: "Mozilla/5.0 (TestAgent)",
		Headers:   map[string]string{"X-Test": "1"},
		// The Cookie is site-scoped, so the run must target the test server for
		// it to be injected at all.
		Site: domain.SiteFromURL(srv.URL),
	}
	client := WithAuth(srv.Client(), auth)

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if gotCookie != auth.Cookie {
		t.Errorf("Cookie = %q, want %q", gotCookie, auth.Cookie)
	}
	if gotUA != auth.UserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, auth.UserAgent)
	}
	if gotExtra != "1" {
		t.Errorf("X-Test = %q, want %q", gotExtra, "1")
	}
}

func TestWithAuth_EmptyAuthReturnsSameClient(t *testing.T) {
	client := &http.Client{}
	got := WithAuth(client, domain.RequestAuth{})
	if got != client {
		t.Error("expected the same client to be returned for empty auth")
	}
}

func TestWithAuth_DoesNotOverrideExistingCookie(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := WithAuth(srv.Client(), domain.RequestAuth{
		Cookie: "from=auth",
		Site:   domain.SiteFromURL(srv.URL),
	})

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Cookie", "from=request")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if gotCookie != "from=request" {
		t.Errorf("Cookie = %q, want request-supplied value to win", gotCookie)
	}
}

func TestWithAuth_PreservesBaseTransport(t *testing.T) {
	// A custom base transport should still be invoked through the wrapper.
	called := false
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	client := &http.Client{Transport: base}

	wrapped := WithAuth(client, domain.RequestAuth{UserAgent: "x"})
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	resp, err := wrapped.Transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	resp.Body.Close()
	if !called {
		t.Error("expected base transport to be invoked")
	}
}

// TestWithAuth_CookieScopedToSite pins the rule that the session Cookie only
// ever reaches hosts the site owns. The CDN throttles or stalls requests
// carrying it, and any other host has no business seeing the session at all.
// The User-Agent is unconditional by contrast: cf_clearance is bound to the UA
// that solved the challenge, so every request must carry it.
func TestWithAuth_CookieScopedToSite(t *testing.T) {
	const cookie = "PHPSESSID=xyz"

	tests := []struct {
		name       string
		site       domain.Site
		url        string
		wantCookie bool
	}{
		{
			name:       "site host itself",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://kino.watch/item/view/1",
			wantCookie: true,
		},
		{
			name:       "subdomain of the site",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://www.kino.watch/item/view/1",
			wantCookie: true,
		},
		{
			name:       "site host with a port",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://kino.watch:8443/item/view/1",
			wantCookie: true,
		},
		{
			name:       "CDN host",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://s1.cdntogo.net/video/segment.ts",
			wantCookie: false,
		},
		{
			name:       "unrelated host",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://example.com/",
			wantCookie: false,
		},
		{
			name:       "suffix lookalike is not owned",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://evilkino.watch/",
			wantCookie: false,
		},
		{
			name:       "mirror is not the targeted site",
			site:       domain.Site{Host: "kino.watch"},
			url:        "https://kino.pub/item/view/1",
			wantCookie: false,
		},
		{
			name:       "zero site falls back to any known site",
			site:       domain.Site{},
			url:        "https://kino.pub/item/view/1",
			wantCookie: true,
		},
		{
			name:       "zero site still withholds from the CDN",
			site:       domain.Site{},
			url:        "https://s1.cdntogo.net/video/segment.ts",
			wantCookie: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotCookie, gotUA string
			base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotCookie = r.Header.Get("Cookie")
				gotUA = r.Header.Get("User-Agent")
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header)}, nil
			})

			client := WithAuth(&http.Client{Transport: base}, domain.RequestAuth{
				Cookie:    cookie,
				UserAgent: "Mozilla/5.0 (TestAgent)",
				Site:      tt.site,
			})

			req, err := http.NewRequest(http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Transport.RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip failed: %v", err)
			}
			resp.Body.Close()

			want := ""
			if tt.wantCookie {
				want = cookie
			}
			if gotCookie != want {
				t.Errorf("Cookie = %q, want %q", gotCookie, want)
			}
			if gotUA != "Mozilla/5.0 (TestAgent)" {
				t.Errorf("User-Agent = %q, want it set on every host", gotUA)
			}
		})
	}
}

// TestWithAuth_CookieDroppedOnRedirectToCDN covers the reason the gate lives in
// the transport rather than at request-build time: http.Client re-runs the
// RoundTripper for every redirect hop, so a site URL that redirects to the CDN
// must lose the Cookie on the second hop.
func TestWithAuth_CookieDroppedOnRedirectToCDN(t *testing.T) {
	const cdnURL = "https://s1.cdntogo.net/segment.ts"

	// Real httptest servers all share the 127.0.0.1 host, which would make the
	// "CDN" indistinguishable from the site, so the hops are served by a stub
	// transport instead. http.Client still drives the redirect for real.
	seen := make(map[string]string) // host → Cookie header
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen[r.URL.Host] = r.Header.Get("Cookie")
		if r.URL.Host == "kino.watch" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Body:       http.NoBody,
				Header:     http.Header{"Location": []string{cdnURL}},
				Request:    r,
			}, nil
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
	})

	client := WithAuth(&http.Client{Transport: base}, domain.RequestAuth{
		Cookie: "PHPSESSID=xyz",
		Site:   domain.Site{Host: "kino.watch"},
	})

	resp, err := client.Get("https://kino.watch/item/view/1")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if got := seen["kino.watch"]; got != "PHPSESSID=xyz" {
		t.Errorf("site Cookie = %q, want the session cookie", got)
	}
	if got, ok := seen["s1.cdntogo.net"]; !ok {
		t.Fatal("redirect was not followed — the second hop never ran")
	} else if got != "" {
		t.Errorf("redirect target Cookie = %q, want it dropped", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
