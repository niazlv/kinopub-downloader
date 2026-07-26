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
