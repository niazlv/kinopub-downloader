package feedparser

import (
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// The tokenized feed must be fetched back from the host the source was resolved
// from, so a mirror or a renamed domain keeps working. A source with no site
// falls back to the current default domain.
func TestFeedURL_FollowsSourceSite(t *testing.T) {
	tests := []struct {
		name string
		src  domain.FeedSource
		want string
	}{
		{
			name: "site from the URL the source came from",
			src:  domain.FeedSource{ID: "38290", Token: "TOK", Site: domain.SiteFromHost("kino.watch")},
			want: "https://kino.watch/podcast/get/38290/TOK",
		},
		{
			name: "unknown mirror",
			src:  domain.FeedSource{ID: "38290", Token: "TOK", Site: domain.SiteFromHost("kino.example")},
			want: "https://kino.example/podcast/get/38290/TOK",
		},
		{
			name: "former domain",
			src:  domain.FeedSource{ID: "38290", Token: "TOK", Site: domain.SiteFromHost("kino.pub")},
			want: "https://kino.pub/podcast/get/38290/TOK",
		},
		{
			name: "no site falls back to the default domain",
			src:  domain.FeedSource{ID: "38290", Token: "TOK"},
			want: "https://" + domain.DefaultSiteHost + "/podcast/get/38290/TOK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := feedURL(tt.src); got != tt.want {
				t.Fatalf("feedURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
