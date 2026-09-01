// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "testing"

func TestSiteFromURL(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string // Site.String()
	}{
		{"current domain", "https://kino.watch/item/view/38290", "kino.watch"},
		{"former domain", "https://kino.pub/item/view/38290", "kino.pub"},
		{"unknown mirror", "https://kino.example/podcast/get/1/tok", "kino.example"},
		{"host with port", "http://mirror.kino.watch:8443/item/view/1", "mirror.kino.watch:8443"},
		{"uppercase host", "https://KINO.WATCH/item/view/1", "kino.watch"},
		{"no host falls back to default", "/item/view/1", DefaultSiteHost},
		{"empty falls back to default", "", DefaultSiteHost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SiteFromURL(tt.rawURL).String(); got != tt.want {
				t.Fatalf("SiteFromURL(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestSiteFromHost(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"kino.watch", "kino.watch"},
		{"kino.example", "kino.example"},
		{"https://kino.example/", "kino.example"}, // a pasted URL is tolerated
		{"  kino.pub  ", "kino.pub"},
		{"", DefaultSiteHost},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := SiteFromHost(tt.in).String(); got != tt.want {
				t.Fatalf("SiteFromHost(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSiteURLs(t *testing.T) {
	site := SiteFromURL("https://kino.example/item/view/38290/s1e1")

	if got, want := site.Origin(), "https://kino.example"; got != want {
		t.Fatalf("Origin() = %q, want %q", got, want)
	}
	if got, want := site.Referer(), "https://kino.example/"; got != want {
		t.Fatalf("Referer() = %q, want %q", got, want)
	}
	// The feed must be rebuilt against the host the page came from.
	if got, want := site.PodcastFeedURL("38290", "TOK"), "https://kino.example/podcast/get/38290/TOK"; got != want {
		t.Fatalf("PodcastFeedURL() = %q, want %q", got, want)
	}

	// The zero Site stands in for the default domain.
	if got, want := (Site{}).PodcastFeedURL("1", "t"), "https://"+DefaultSiteHost+"/podcast/get/1/t"; got != want {
		t.Fatalf("zero Site PodcastFeedURL() = %q, want %q", got, want)
	}
	// A plain host keeps https; an explicit http scheme survives.
	if got, want := SiteFromURL("http://kino.example/x").Origin(), "http://kino.example"; got != want {
		t.Fatalf("Origin() = %q, want %q", got, want)
	}
}

// Owns decides whether a request may carry the session cookie. Sending it to
// the CDN throttles and stalls the download, so a CDN host must never match.
func TestSiteOwns(t *testing.T) {
	site := SiteFromURL("https://kino.example/item/view/1")

	owned := []string{"kino.example", "KINO.EXAMPLE", "kino.example:443", "cdn.kino.example"}
	for _, host := range owned {
		if !site.Owns(host) {
			t.Errorf("Owns(%q) = false, want true", host)
		}
	}

	foreign := []string{
		"digital-cdn.net",
		"cdntogo.net",
		"kino.watch",      // a different domain of the same service
		"notkino.example", // suffix must be on a label boundary
		"kino.example.evil.com",
		"",
	}
	for _, host := range foreign {
		if site.Owns(host) {
			t.Errorf("Owns(%q) = true, want false", host)
		}
	}
}

func TestAnyKnownSiteOwns(t *testing.T) {
	for _, host := range KnownSiteHosts {
		if !AnyKnownSiteOwns(host) {
			t.Errorf("AnyKnownSiteOwns(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"digital-cdn.net", "cdntogo.net", "kino.example", ""} {
		if AnyKnownSiteOwns(host) {
			t.Errorf("AnyKnownSiteOwns(%q) = true, want false", host)
		}
	}
}

func TestSiteIsZero(t *testing.T) {
	if !(Site{}).IsZero() {
		t.Fatal("zero Site should report IsZero")
	}
	if SiteFromURL("https://kino.example/x").IsZero() {
		t.Fatal("Site with a host should not report IsZero")
	}
}

func TestSeriesIDFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want SeriesID
	}{
		{"page link", "https://kino.watch/item/view/38290", "38290"},
		{"page link with slug", "https://kino.watch/item/view/38290/some-title", "38290"},
		{"page link trailing slash", "https://kino.watch/item/view/38290/", "38290"},
		{"podcast feed", "https://kino.watch/podcast/get/38290/TOKEN", "38290"},
		{"old domain", "https://kino.pub/item/view/12", "12"},
		{"unrelated path", "https://kino.watch/profile", ""},
		{"non-numeric id", "https://kino.watch/item/view/abc", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SeriesIDFromURL(tt.url); got != tt.want {
				t.Errorf("SeriesIDFromURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestSiteRewriteHost(t *testing.T) {
	tests := []struct {
		name   string
		site   Site
		host   string
		want   string
		wantOK bool
	}{
		{"former to current", Site{Host: "kino.watch"}, "kino.pub", "kino.watch", true},
		{"subdomain preserved", Site{Host: "kino.watch"}, "api.kino.pub", "api.kino.watch", true},
		{"case and port dropped", Site{Host: "kino.watch"}, "WWW.KINO.PUB:443", "www.kino.watch", true},
		{"current to mirror", Site{Host: "kino.example"}, "kino.watch", "kino.example", true},
		{"already owned", Site{Host: "kino.watch"}, "kino.watch", "kino.watch", false},
		{"owned subdomain", Site{Host: "kino.watch"}, "cdn.kino.watch", "cdn.kino.watch", false},
		{"unknown host untouched", Site{Host: "kino.watch"}, "digital-cdn.net", "digital-cdn.net", false},
		{"lookalike untouched", Site{Host: "kino.watch"}, "notkino.pub", "notkino.pub", false},
		{"empty host", Site{Host: "kino.watch"}, "", "", false},
		{"zero site targets default", Site{}, "kino.pub", DefaultSiteHost, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.site.RewriteHost(tt.host)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("RewriteHost(%q) = (%q, %v), want (%q, %v)", tt.host, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSiteRewriteURL(t *testing.T) {
	site := Site{Host: "kino.watch"}

	tests := []struct {
		name   string
		rawURL string
		want   string
		wantOK bool
	}{
		{"page link", "https://kino.pub/item/view/38290/s1e1", "https://kino.watch/item/view/38290/s1e1", true},
		{"feed link with query", "https://kino.pub/podcast/get/1/tok?x=1", "https://kino.watch/podcast/get/1/tok?x=1", true},
		{"subdomain preserved", "https://api.kino.pub/v1/file", "https://api.kino.watch/v1/file", true},
		{"current domain untouched", "https://kino.watch/item/view/1", "https://kino.watch/item/view/1", false},
		{"cdn untouched", "https://cdn.example.com/s01e01.mp4", "https://cdn.example.com/s01e01.mp4", false},
		{"relative untouched", "/item/view/1", "/item/view/1", false},
		{"empty untouched", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := site.RewriteURL(tt.rawURL)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("RewriteURL(%q) = (%q, %v), want (%q, %v)", tt.rawURL, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSiteUpgraded(t *testing.T) {
	tests := []struct {
		name   string
		site   Site
		want   string
		wantOK bool
	}{
		{"former domain upgrades", Site{Host: "kino.pub"}, KnownSiteHosts[0], true},
		{"former subdomain upgrades", Site{Host: "www.kino.pub"}, "www." + KnownSiteHosts[0], true},
		{"current stays", Site{Host: "kino.watch"}, "kino.watch", false},
		{"mirror stays", Site{Host: "kino.example"}, "kino.example", false},
		{"zero site stays", Site{}, DefaultSiteHost, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.site.Upgraded()
			if got.String() != tt.want || ok != tt.wantOK {
				t.Fatalf("Upgraded() = (%q, %v), want (%q, %v)", got.String(), ok, tt.want, tt.wantOK)
			}
		})
	}

	// The scheme survives the upgrade: an http mirror bookmark stays http.
	up, ok := (Site{Scheme: "http", Host: "kino.pub"}).Upgraded()
	if !ok || up.Origin() != "http://"+KnownSiteHosts[0] {
		t.Fatalf("Upgraded() Origin = %q (ok=%v), want %q", up.Origin(), ok, "http://"+KnownSiteHosts[0])
	}
}
