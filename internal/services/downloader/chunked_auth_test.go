package downloader

import (
	"net/http"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// The session cookie must reach the site the run targets — whichever domain
// that is — and must never reach the CDN, which throttles and stalls requests
// carrying it.
func TestApplyAuth_CookieScopedToSite(t *testing.T) {
	tests := []struct {
		name       string
		site       domain.Site
		requestURL string
		wantCookie bool
	}{
		{
			name:       "current domain",
			site:       domain.SiteFromHost("kino.watch"),
			requestURL: "https://kino.watch/item/view/38290",
			wantCookie: true,
		},
		{
			name:       "mirror the tool has never seen",
			site:       domain.SiteFromHost("kino.example"),
			requestURL: "https://kino.example/podcast/get/1/tok",
			wantCookie: true,
		},
		{
			name:       "subdomain of the site",
			site:       domain.SiteFromHost("kino.watch"),
			requestURL: "https://www.kino.watch/item/view/1",
			wantCookie: true,
		},
		{
			name:       "CDN never receives the cookie",
			site:       domain.SiteFromHost("kino.watch"),
			requestURL: "https://str.digital-cdn.net/video.mp4",
			wantCookie: false,
		},
		{
			name:       "a different domain of the service is not the target site",
			site:       domain.SiteFromHost("kino.watch"),
			requestURL: "https://kino.pub/item/view/1",
			wantCookie: false,
		},
		{
			name:       "unknown site falls back to the known hosts",
			site:       domain.Site{},
			requestURL: "https://kino.pub/item/view/1",
			wantCookie: true,
		},
		{
			name:       "unknown site still shields the CDN",
			site:       domain.Site{},
			requestURL: "https://str.cdntogo.net/video.mp4",
			wantCookie: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := domain.RequestAuth{
				Cookie:    "cf_clearance=abc",
				UserAgent: "TestUA",
				Headers:   map[string]string{"Referer": tt.site.Referer()},
				Site:      tt.site,
			}
			d := NewChunked(&http.Client{}, auth, testLogger{})

			req, err := http.NewRequest(http.MethodGet, tt.requestURL, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			d.applyAuth(req)

			if got := req.Header.Get("Cookie"); (got != "") != tt.wantCookie {
				t.Fatalf("Cookie header = %q for %s, want present=%v", got, tt.requestURL, tt.wantCookie)
			}
			// User-Agent and Referer go everywhere, including the CDN, which
			// needs the Referer or it hangs.
			if got := req.Header.Get("User-Agent"); got != "TestUA" {
				t.Fatalf("User-Agent = %q, want TestUA", got)
			}
			if got := req.Header.Get("Referer"); got != tt.site.Referer() {
				t.Fatalf("Referer = %q, want %q", got, tt.site.Referer())
			}
		})
	}
}
