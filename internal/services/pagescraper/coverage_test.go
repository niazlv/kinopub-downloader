// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package pagescraper

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
)

// nopLogger returns a no-op logger for tests in this package.
func nopLogger() domain.Logger {
	return logx.New(nil)
}

func TestNormalizeItemURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"strips s1e1", "https://kino.pub/item/view/38290/s1e1", "https://kino.pub/item/view/38290"},
		{"strips trailing slash variant", "https://kino.pub/item/view/38290/s12e34/", "https://kino.pub/item/view/38290"},
		{"case insensitive", "https://kino.pub/item/view/38290/S2E5", "https://kino.pub/item/view/38290"},
		{"multi-digit", "https://kino.pub/item/view/1/s10e100", "https://kino.pub/item/view/1"},
		{"no suffix unchanged", "https://kino.pub/item/view/38290", "https://kino.pub/item/view/38290"},
		{"non-matching suffix unchanged", "https://kino.pub/item/view/38290/extra", "https://kino.pub/item/view/38290/extra"},
		{"only strips at end", "https://kino.pub/s1e1/item/view/38290", "https://kino.pub/s1e1/item/view/38290"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeItemURL(tt.in); got != tt.want {
				t.Errorf("normalizeItemURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToDomain(t *testing.T) {
	p := &PagePlaylist{
		ItemID: 42,
		Title:  "My Series",
		Poster: "http://img/poster.jpg",
		Episodes: []PlayerEpisode{
			{
				Manifest:     "http://cdn/m1.m3u8",
				ID:           1,
				MediaID:      100,
				Title:        "My Series",
				EpisodeTitle: "Pilot",
				Duration:     1200,
				Season:       1,
				Episode:      1,
			},
			{
				Manifest:     "http://cdn/m2.m3u8",
				MediaID:      101,
				EpisodeTitle: "Second",
				Duration:     1300,
				Season:       1,
				Episode:      2,
			},
		},
		Seasons: []PlayerSeason{
			{Season: 1, Count: 10},
			{Season: 2, Count: 8},
		},
	}

	d := p.toDomain()

	if d.ItemID != 42 || d.Title != "My Series" || d.Poster != "http://img/poster.jpg" {
		t.Fatalf("scalar mapping wrong: %+v", d)
	}
	if len(d.Episodes) != 2 {
		t.Fatalf("expected 2 episodes, got %d", len(d.Episodes))
	}
	e0 := d.Episodes[0]
	if e0.ManifestURL != "http://cdn/m1.m3u8" || e0.MediaID != 100 ||
		e0.EpisodeTitle != "Pilot" || e0.Duration != 1200 ||
		e0.Season != 1 || e0.Episode != 1 {
		t.Errorf("episode[0] mapping wrong: %+v", e0)
	}
	if len(d.Seasons) != 2 {
		t.Fatalf("expected 2 seasons, got %d", len(d.Seasons))
	}
	if d.Seasons[1].Season != 2 || d.Seasons[1].Count != 8 {
		t.Errorf("season[1] mapping wrong: %+v", d.Seasons[1])
	}
}

func TestToDomain_Empty(t *testing.T) {
	p := &PagePlaylist{}
	d := p.toDomain()
	if d == nil {
		t.Fatal("toDomain returned nil")
	}
	if len(d.Episodes) != 0 || len(d.Seasons) != 0 {
		t.Errorf("expected empty slices, got episodes=%d seasons=%d", len(d.Episodes), len(d.Seasons))
	}
}

func TestParsePlaylist_Basic(t *testing.T) {
	body := []byte(`
<html><head><script>
window.PLAYER_ITEM_ID = 38290;
window.PLAYER_PLAYLIST = [{"manifest":"http://cdn/1.m3u8","id":1,"media_id":100,"title":"Series","episode_title":"Ep1","poster":"http://p.jpg","duration":1500,"season":1,"episode":1}];
window.PLAYER_SEASONS = [{"season":1,"count":10},{"season":2,"count":8}];
</script></head></html>`)

	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("parsePlaylist error: %v", err)
	}
	if got.ItemID != 38290 {
		t.Errorf("ItemID = %d, want 38290", got.ItemID)
	}
	if got.Title != "Series" {
		t.Errorf("Title = %q, want Series", got.Title)
	}
	if got.Poster != "http://p.jpg" {
		t.Errorf("Poster = %q", got.Poster)
	}
	if len(got.Episodes) != 1 {
		t.Fatalf("episodes = %d, want 1", len(got.Episodes))
	}
	if got.Episodes[0].Manifest != "http://cdn/1.m3u8" {
		t.Errorf("manifest = %q", got.Episodes[0].Manifest)
	}
	if len(got.Seasons) != 2 {
		t.Fatalf("seasons = %d, want 2", len(got.Seasons))
	}
}

// TestParsePlaylist_Multiline verifies the (?s) dotall behaviour: the JSON
// assignment spans multiple lines.
func TestParsePlaylist_Multiline(t *testing.T) {
	body := []byte(`
window.PLAYER_PLAYLIST = [
  {
    "manifest": "http://cdn/1.m3u8",
    "media_id": 100,
    "season": 1,
    "episode": 1
  },
  {
    "manifest": "http://cdn/2.m3u8",
    "media_id": 101,
    "season": 1,
    "episode": 2
  }
];
`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("parsePlaylist error: %v", err)
	}
	if len(got.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(got.Episodes))
	}
	if got.Episodes[1].MediaID != 101 {
		t.Errorf("episode[1].MediaID = %d, want 101", got.Episodes[1].MediaID)
	}
}

// TestParsePlaylist_AdjacentAssignments verifies the non-greedy match stops at
// the first `];` and does NOT swallow the adjacent PLAYER_SEASONS assignment.
func TestParsePlaylist_AdjacentAssignments(t *testing.T) {
	body := []byte(`window.PLAYER_PLAYLIST = [{"media_id":100,"season":1,"episode":1}];window.PLAYER_SEASONS = [{"season":1,"count":3}];`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("parsePlaylist error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Fatalf("episodes = %d, want 1 (non-greedy must stop at first ];)", len(got.Episodes))
	}
	if got.Episodes[0].MediaID != 100 {
		t.Errorf("media_id = %d, want 100", got.Episodes[0].MediaID)
	}
	if len(got.Seasons) != 1 || got.Seasons[0].Count != 3 {
		t.Errorf("seasons parsed wrong: %+v", got.Seasons)
	}
}

// TestParsePlaylist_NestedBrackets ensures arrays containing nested `]` inside
// string values still parse (the regex captures up to the first `];` token).
func TestParsePlaylist_NestedBrackets(t *testing.T) {
	// The episode_title contains a "]" character but it is followed by other
	// content (not "];"), so the non-greedy match should capture the whole array.
	body := []byte(`window.PLAYER_PLAYLIST = [{"media_id":7,"episode_title":"weird ] title","season":1,"episode":1}];`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("parsePlaylist error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Fatalf("episodes = %d, want 1", len(got.Episodes))
	}
	if got.Episodes[0].EpisodeTitle != "weird ] title" {
		t.Errorf("episode_title = %q", got.Episodes[0].EpisodeTitle)
	}
}

func TestParsePlaylist_NoPlaylist(t *testing.T) {
	body := []byte(`<html><body>login required</body></html>`)
	s := New(&http.Client{}, nopLogger())
	_, err := s.parsePlaylist(body)
	if err == nil {
		t.Fatal("expected error for missing PLAYER_PLAYLIST")
	}
	if !strings.Contains(err.Error(), "PLAYER_PLAYLIST not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestParsePlaylist_InvalidPlaylistJSON(t *testing.T) {
	body := []byte(`window.PLAYER_PLAYLIST = [not valid json];`)
	s := New(&http.Client{}, nopLogger())
	_, err := s.parsePlaylist(body)
	if err == nil {
		t.Fatal("expected json parse error")
	}
	if !strings.Contains(err.Error(), "parse PLAYER_PLAYLIST") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestParsePlaylist_InvalidSeasonsJSON: bad seasons JSON is tolerated (seasons
// stays empty) while the playlist still parses successfully.
func TestParsePlaylist_InvalidSeasonsJSON(t *testing.T) {
	body := []byte(`window.PLAYER_PLAYLIST = [{"media_id":1,"season":1,"episode":1}];window.PLAYER_SEASONS = [bad];`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Errorf("episodes = %d, want 1", len(got.Episodes))
	}
	if len(got.Seasons) != 0 {
		t.Errorf("seasons should be empty on bad JSON, got %d", len(got.Seasons))
	}
}

func TestParsePlaylist_NoItemID(t *testing.T) {
	body := []byte(`window.PLAYER_PLAYLIST = [{"media_id":5,"season":1,"episode":1}];`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ItemID != 0 {
		t.Errorf("ItemID = %d, want 0 (no PLAYER_ITEM_ID present)", got.ItemID)
	}
}

func TestParsePlaylist_NoSeasons(t *testing.T) {
	body := []byte(`window.PLAYER_PLAYLIST = [{"media_id":5,"season":1,"episode":1}];`)
	s := New(&http.Client{}, nopLogger())
	got, err := s.parsePlaylist(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Seasons) != 0 {
		t.Errorf("seasons = %d, want 0", len(got.Seasons))
	}
}

// --- fetchPage / ExtractFeedSource / parseBody tests (scraper.go) ---

func TestFetchPage_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello body"))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	body, err := s.fetchPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchPage error: %v", err)
	}
	if string(body) != "hello body" {
		t.Errorf("body = %q", string(body))
	}
}

func TestFetchPage_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	_, err := s.fetchPage(context.Background(), srv.URL)
	if !errors.Is(err, domain.ErrAuthRequired) {
		t.Errorf("expected ErrAuthRequired, got %v", err)
	}
}

func TestFetchPage_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	_, err := s.fetchPage(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
	if !strings.Contains(err.Error(), "unexpected status 500") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchPage_BadRequest(t *testing.T) {
	s := New(&http.Client{}, nopLogger())
	// Invalid URL with control char triggers NewRequestWithContext error.
	_, err := s.fetchPage(context.Background(), "http://exa\nmple.com")
	if err == nil {
		t.Fatal("expected build request error")
	}
	if !strings.Contains(err.Error(), "build request") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFetchPage_DoError(t *testing.T) {
	s := New(&http.Client{}, nopLogger())
	// Unroutable scheme/host causes client.Do to fail.
	_, err := s.fetchPage(context.Background(), "http://127.0.0.1:0/")
	if err == nil {
		t.Fatal("expected fetch error")
	}
	if !strings.Contains(err.Error(), "fetch page") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExtractFeedSource_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="/podcast/get/38290/TOKEN_abc123">feed</a>`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	fs, err := s.ExtractFeedSource(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ExtractFeedSource error: %v", err)
	}
	if fs.ID != "38290" || fs.Token != "TOKEN_abc123" {
		t.Errorf("FeedSource = %+v", fs)
	}
}

func TestExtractFeedSource_NoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>no podcast link here</html>`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	_, err := s.ExtractFeedSource(context.Background(), srv.URL)
	if !errors.Is(err, domain.ErrFeedTokenUnavailable) {
		t.Errorf("expected ErrFeedTokenUnavailable, got %v", err)
	}
}

func TestExtractFeedSource_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	_, err := s.ExtractFeedSource(context.Background(), srv.URL)
	if !errors.Is(err, domain.ErrAuthRequired) {
		t.Errorf("expected ErrAuthRequired, got %v", err)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"short", 8, "short"},
		{"exactly8", 8, "exactly8"},
		{"longerstring", 4, "long…"},
		{"", 3, ""},
		{"abcdef", 0, "…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.s, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}

// --- ExtractPlaylist / ExtractAllSeasons via httptest ---

func TestExtractPlaylist_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`window.PLAYER_ITEM_ID = 7;
window.PLAYER_PLAYLIST = [{"media_id":1,"manifest":"m","season":1,"episode":1}];`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	got, err := s.ExtractPlaylist(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ExtractPlaylist error: %v", err)
	}
	if got.ItemID != 7 || len(got.Episodes) != 1 {
		t.Errorf("unexpected result: %+v", got)
	}
}

func TestExtractAllSeasons_SingleSeason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`window.PLAYER_PLAYLIST = [{"media_id":1,"season":1,"episode":1}];
window.PLAYER_SEASONS = [{"season":1,"count":1}];`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	got, err := s.ExtractAllSeasons(context.Background(), srv.URL+"/s1e1")
	if err != nil {
		t.Fatalf("ExtractAllSeasons error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Errorf("episodes = %d, want 1", len(got.Episodes))
	}
}

func TestExtractAllSeasons_InitialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	_, err := s.ExtractAllSeasons(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected initial page error")
	}
	if !strings.Contains(err.Error(), "initial page") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestExtractAllSeasons_MultiSeason exercises the dedup logic and per-season
// fetching: the base page returns season 1, and the per-season URL returns
// season 2 episodes (with an overlapping duplicate to verify dedup).
func TestExtractAllSeasons_MultiSeason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/s2e1") {
			// Season 2 page: include a duplicate (s1e1) plus new s2 episodes.
			_, _ = w.Write([]byte(`window.PLAYER_PLAYLIST = [` +
				`{"media_id":11,"season":1,"episode":1},` +
				`{"media_id":21,"season":2,"episode":1},` +
				`{"media_id":22,"season":2,"episode":2}` +
				`];window.PLAYER_SEASONS = [{"season":1,"count":2},{"season":2,"count":2}];`))
			return
		}
		// Base page: season 1 episodes + season metadata for 2 seasons.
		_, _ = w.Write([]byte(`window.PLAYER_PLAYLIST = [` +
			`{"media_id":11,"season":1,"episode":1},` +
			`{"media_id":12,"season":1,"episode":2}` +
			`];window.PLAYER_SEASONS = [{"season":1,"count":2},{"season":2,"count":2}];`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	got, err := s.ExtractAllSeasons(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ExtractAllSeasons error: %v", err)
	}
	// Expect s1e1, s1e2, s2e1, s2e2 (the duplicate s1e1 from season 2 page
	// must be deduplicated).
	if len(got.Episodes) != 4 {
		t.Fatalf("episodes = %d, want 4 (dedup): %+v", len(got.Episodes), got.Episodes)
	}
	counts := map[[2]int]int{}
	for _, e := range got.Episodes {
		counts[[2]int{e.Season, e.Episode}]++
	}
	for key, c := range counts {
		if c != 1 {
			t.Errorf("episode %v appears %d times, want 1", key, c)
		}
	}
}

// TestExtractAllSeasons_SeasonFetchFails: when fetching a non-base season fails,
// it is skipped and the already-known episodes are still returned.
func TestExtractAllSeasons_SeasonFetchFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/s2e1") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`window.PLAYER_PLAYLIST = [{"media_id":11,"season":1,"episode":1}];` +
			`window.PLAYER_SEASONS = [{"season":1,"count":1},{"season":2,"count":1}];`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	got, err := s.ExtractAllSeasons(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ExtractAllSeasons error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Errorf("episodes = %d, want 1 (failed season skipped)", len(got.Episodes))
	}
}

// TestExtractAllSeasons_WrongSeasonReturned: season page returns season 1 again
// for the s2 request; gotRightSeason stays false (warn path), episodes already
// seen are deduped so nothing new is added.
func TestExtractAllSeasons_WrongSeasonReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return season 1 data regardless of requested season.
		_, _ = w.Write([]byte(`window.PLAYER_PLAYLIST = [{"media_id":11,"season":1,"episode":1}];` +
			`window.PLAYER_SEASONS = [{"season":1,"count":1},{"season":2,"count":1}];`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nopLogger())
	got, err := s.ExtractAllSeasons(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ExtractAllSeasons error: %v", err)
	}
	if len(got.Episodes) != 1 {
		t.Errorf("episodes = %d, want 1", len(got.Episodes))
	}
}
