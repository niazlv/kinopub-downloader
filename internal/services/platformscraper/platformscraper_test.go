// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package platformscraper

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/services/manifestscraper"
)

// fakePlatform serves the two API calls the scraper makes: a series with
// three episodes across two seasons (title 201), a movie (title 301), and a
// manifest for any episode. titleStatus lets a test refuse the title call;
// manifestV sets the manifest version served.
type fakePlatform struct {
	titleStatus int
	manifestV   int
}

func (f fakePlatform) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/titles/201", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if f.titleStatus != 0 && f.titleStatus != http.StatusOK {
			w.WriteHeader(f.titleStatus)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"title": map[string]any{"id": 201, "kind": "series", "title": "Сериал", "hasArt": true},
			"episodes": []map[string]any{
				{"id": 11, "seasonNo": 1, "episodeNo": 1, "title": "Первая", "durationSec": 1400},
				{"id": 12, "seasonNo": 1, "episodeNo": 2, "title": "Вторая", "durationSec": 1410},
				{"id": 21, "seasonNo": 2, "episodeNo": 1, "durationSec": 1420},
			},
		})
	})
	mux.HandleFunc("/api/v1/titles/301", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"title":    map[string]any{"id": 301, "kind": "movie", "title": "Фильм", "hasArt": false},
			"episodes": []map[string]any{{"id": 31, "seasonNo": 0, "episodeNo": 1, "durationSec": 5400}},
		})
	})
	mux.HandleFunc("/api/v1/episodes/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/episodes/"), "/manifest")
		v := f.manifestV
		if v == 0 {
			v = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"v": v, "protocol": "HLS",
			"url":       "https://media.example/stream/tkt_" + id + "/master.m3u8",
			"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"audios":    []any{}, "subtitles": []any{}, "renditions": []any{},
			"select": map[string]any{"audios": []any{}, "subtitles": []any{}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestExtractAllSeasons_WholeTitle(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if pl.ItemID != 201 || pl.Title != "Сериал" {
		t.Errorf("playlist identity = %d %q", pl.ItemID, pl.Title)
	}
	if want := srv.URL + "/api/v1/titles/201/art"; pl.Poster != want {
		t.Errorf("poster = %q, want %q", pl.Poster, want)
	}
	want := []domain.PageEpisode{
		{ManifestURL: "https://media.example/stream/tkt_11/master.m3u8", MediaID: 11, EpisodeTitle: "Первая", Season: 1, Episode: 1, Duration: 1400},
		{ManifestURL: "https://media.example/stream/tkt_12/master.m3u8", MediaID: 12, EpisodeTitle: "Вторая", Season: 1, Episode: 2, Duration: 1410},
		{ManifestURL: "https://media.example/stream/tkt_21/master.m3u8", MediaID: 21, Season: 2, Episode: 1, Duration: 1420},
	}
	if len(pl.Episodes) != len(want) {
		t.Fatalf("got %d episodes, want %d", len(pl.Episodes), len(want))
	}
	for i := range want {
		if pl.Episodes[i] != want[i] {
			t.Errorf("episode %d = %+v, want %+v", i, pl.Episodes[i], want[i])
		}
	}
	seasons := []domain.PageSeason{{Season: 1, Count: 2}, {Season: 2, Count: 1}}
	if len(pl.Seasons) != 2 || pl.Seasons[0] != seasons[0] || pl.Seasons[1] != seasons[1] {
		t.Errorf("seasons = %+v, want %+v", pl.Seasons, seasons)
	}
}

func TestExtractAllSeasons_EpisodeLinkNarrowsToThatEpisode(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/12")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 || pl.Episodes[0].MediaID != 12 || pl.Episodes[0].Season != 1 || pl.Episodes[0].Episode != 2 {
		t.Fatalf("episodes = %+v, want just s01e02 (id 12)", pl.Episodes)
	}
	if len(pl.Seasons) != 1 || pl.Seasons[0] != (domain.PageSeason{Season: 1, Count: 1}) {
		t.Errorf("seasons = %+v", pl.Seasons)
	}
}

func TestExtractAllSeasons_UnknownEpisodeIsNamed(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	_, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/99")
	if err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("want an error naming episode 99, got %v", err)
	}
}

func TestExtractAllSeasons_MovieIsS01E01(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/301")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 || pl.Episodes[0].Season != 1 || pl.Episodes[0].Episode != 1 {
		t.Fatalf("episodes = %+v, want a single s01e01", pl.Episodes)
	}
	if pl.Poster != "" {
		t.Errorf("poster = %q, want none for a title without art", pl.Poster)
	}
}

func TestExtractAllSeasons_NoSessionIsReportedAsSuch(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := fakePlatform{titleStatus: status}.serve(t)
		_, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201")
		if !errors.Is(err, domain.ErrPlatformSessionRequired) {
			t.Errorf("status %d: want ErrPlatformSessionRequired, got %v", status, err)
		}
	}
}

func TestExtractAllSeasons_NewerManifestIsRefused(t *testing.T) {
	srv := fakePlatform{manifestV: 2}.serve(t)
	_, err := New(srv.Client(), nil).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201")
	if !errors.Is(err, manifestscraper.ErrUnsupportedVersion) {
		t.Fatalf("want ErrUnsupportedVersion, got %v", err)
	}
}

func TestExtractAllSeasons_RejectsNonPlatformLinks(t *testing.T) {
	_, err := New(nil, nil).ExtractAllSeasons(context.Background(), "https://kino.watch/item/view/38290")
	if !errors.Is(err, domain.ErrInvalidInputURL) {
		t.Fatalf("want ErrInvalidInputURL, got %v", err)
	}
}

// only builds the selection main derives from a link with a season/episode
// suffix (see kinopub.ApplyURLEpisodeRef), so the scraper is exercised the way
// it is wired.
func only(season, episode int) Option {
	seasons := domain.Selection{Values: map[int]bool{season: true}}
	episodes := domain.Selection{All: true}
	if episode > 0 {
		episodes = domain.Selection{Values: map[int]bool{episode: true}}
	}
	return WithSelection(seasons, episodes)
}

func TestExtractAllSeasons_SeasonEpisodeLinkNarrowsToThatEpisode(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil, only(1, 2)).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/s1e2")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 || pl.Episodes[0].MediaID != 12 || pl.Episodes[0].EpisodeTitle != "Вторая" {
		t.Fatalf("episodes = %+v, want just s01e02 (id 12)", pl.Episodes)
	}
}

func TestExtractAllSeasons_SeasonLinkKeepsTheWholeSeason(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil, only(1, 0)).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/s1")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 2 || pl.Episodes[0].MediaID != 11 || pl.Episodes[1].MediaID != 12 {
		t.Fatalf("episodes = %+v, want season 1 only", pl.Episodes)
	}
}

// An explicit --seasons/--episodes outranks the link's suffix; the scraper
// simply follows the selection it is given.
func TestExtractAllSeasons_ExplicitSelectionOutranksTheLink(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil, WithSelection(
		domain.Selection{Values: map[int]bool{2: true}}, domain.Selection{All: true},
	)).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/s1e2")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 || pl.Episodes[0].MediaID != 21 {
		t.Fatalf("episodes = %+v, want season 2 as selected", pl.Episodes)
	}
}

func TestExtractAllSeasons_MissingEpisodeIsNamed(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	_, err := New(srv.Client(), nil, only(9, 9)).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/201/s9e9")
	if err == nil || !strings.Contains(err.Error(), "s09e09") {
		t.Fatalf("want an error naming s09e09, got %v", err)
	}
}

// A movie is S01E01 to the pipeline, so a --seasons 1 selection keeps it.
func TestExtractAllSeasons_MovieMatchesSeasonOne(t *testing.T) {
	srv := fakePlatform{}.serve(t)
	pl, err := New(srv.Client(), nil, only(1, 1)).ExtractAllSeasons(context.Background(), srv.URL+"/#/title/301")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 {
		t.Fatalf("episodes = %+v", pl.Episodes)
	}
}
