// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package apiscraper

import (
	"context"
	"errors"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/services/apiclient"
)

type fakeFetcher struct {
	item  apiclient.Item
	err   error
	gotID string
}

func (f *fakeFetcher) Item(_ context.Context, id string) (apiclient.Item, error) {
	f.gotID = id
	return f.item, f.err
}

func TestParseItemID(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://kino.pub/item/view/126715", "126715", true},
		{"https://kino.pub/item/view/126715/s1e1", "126715", true},
		{"https://kino.watch/item/view/38290/s12e34/", "38290", true},
		{"https://kino.pub/item/66136", "66136", true},
		{"126715", "126715", true},
		{"https://kino.pub/movies", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, err := parseItemID(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("parseItemID(%q) = %q,%v want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("parseItemID(%q) = %q, want error", c.in, got)
		}
	}
}

func TestPickManifestPrefersCodecThenQuality(t *testing.T) {
	files := []apiclient.File{
		{Codec: "h265", QualityID: 4, URL: apiclient.URLSet{HLS4: "h265-2160"}},
		{Codec: "h264", QualityID: 2, URL: apiclient.URLSet{HLS4: "h264-720"}},
		{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "h264-1080"}},
	}
	// Prefer h264 even though h265 has a higher quality_id; within h264 pick the
	// highest quality.
	if got, _ := pickManifest(files, "h264"); got != "h264-1080" {
		t.Errorf("h264 pref = %q, want h264-1080", got)
	}
	if got, _ := pickManifest(files, "h265"); got != "h265-2160" {
		t.Errorf("h265 pref = %q, want h265-2160", got)
	}
}

func TestPickManifestFallsBackToHLSWhenNoHLS4(t *testing.T) {
	files := []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS: "cdn-master"}}}
	if got, ok := pickManifest(files, "h264"); !ok || got != "cdn-master" {
		t.Errorf("fallback = %q,%v", got, ok)
	}
}

func TestPickManifestNoneWhenNoURLs(t *testing.T) {
	files := []apiclient.File{{Codec: "h264", QualityID: 3}}
	if _, ok := pickManifest(files, "h264"); ok {
		t.Error("expected no manifest when files carry no URLs")
	}
}

func TestExtractMovie(t *testing.T) {
	ff := &fakeFetcher{item: apiclient.Item{
		ID: 126715, Title: "Brink", Type: "movie",
		Posters: apiclient.Posters{Big: "big.jpg"},
		Videos: []apiclient.Video{{
			ID: 1, Number: 1, SNumber: 0, Duration: 7146,
			Files: []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "m.m3u8"}}},
		}},
	}}
	pl, err := New(ff, nil).ExtractAllSeasons(context.Background(), "https://kino.pub/item/view/126715")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if ff.gotID != "126715" {
		t.Errorf("fetched id = %q", ff.gotID)
	}
	if pl.ItemID != 126715 || pl.Title != "Brink" || pl.Poster != "big.jpg" {
		t.Errorf("playlist = %+v", pl)
	}
	if len(pl.Episodes) != 1 {
		t.Fatalf("episodes = %d, want 1", len(pl.Episodes))
	}
	e := pl.Episodes[0]
	if e.Season != 1 || e.Episode != 1 || e.ManifestURL != "m.m3u8" || e.EpisodeTitle != "Brink" {
		t.Errorf("episode = %+v", e)
	}
	if len(pl.Seasons) != 1 || pl.Seasons[0].Season != 1 || pl.Seasons[0].Count != 1 {
		t.Errorf("seasons = %+v", pl.Seasons)
	}
}

func TestExtractSerial(t *testing.T) {
	ff := &fakeFetcher{item: apiclient.Item{
		ID: 66136, Title: "Show", Type: "serial",
		Seasons: []apiclient.Season{{
			Number: 1, Episodes: []apiclient.Video{
				{ID: 5, Number: 1, SNumber: 1, Title: "Pilot", Duration: 2600,
					Files: []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "e1"}}}},
				{ID: 6, Number: 2, SNumber: 1, Title: "Ep2", Duration: 2700,
					Files: []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "e2"}}}},
			}}, {
			Number: 2, Episodes: []apiclient.Video{
				{ID: 7, Number: 1, SNumber: 2, Title: "S2E1", Duration: 2800,
					Files: []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "s2e1"}}}},
			}},
		},
	}}
	pl, err := New(ff, nil).ExtractAllSeasons(context.Background(), "66136")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 3 {
		t.Fatalf("episodes = %d, want 3", len(pl.Episodes))
	}
	if len(pl.Seasons) != 2 || pl.Seasons[0].Count != 2 || pl.Seasons[1].Count != 1 {
		t.Errorf("seasons = %+v", pl.Seasons)
	}
}

func TestExtractSkipsEpisodesWithoutManifest(t *testing.T) {
	ff := &fakeFetcher{item: apiclient.Item{
		ID: 1, Title: "X", Type: "serial",
		Seasons: []apiclient.Season{{Number: 1, Episodes: []apiclient.Video{
			{ID: 1, Number: 1, SNumber: 1, Files: nil}, // no files → skipped
			{ID: 2, Number: 2, SNumber: 1, Files: []apiclient.File{{Codec: "h264", QualityID: 3, URL: apiclient.URLSet{HLS4: "ok"}}}},
		}}},
	}}
	pl, err := New(ff, nil).ExtractAllSeasons(context.Background(), "1")
	if err != nil {
		t.Fatalf("ExtractAllSeasons: %v", err)
	}
	if len(pl.Episodes) != 1 || pl.Episodes[0].Episode != 2 {
		t.Errorf("episodes = %+v", pl.Episodes)
	}
}

func TestExtractNoPlayableIsError(t *testing.T) {
	ff := &fakeFetcher{item: apiclient.Item{ID: 1, Type: "movie", Videos: []apiclient.Video{{ID: 1}}}}
	_, err := New(ff, nil).ExtractAllSeasons(context.Background(), "1")
	if !errors.Is(err, domain.ErrNoVideoTrack) {
		t.Fatalf("err = %v, want ErrNoVideoTrack", err)
	}
}

func TestExtractUnrecognizedURL(t *testing.T) {
	_, err := New(&fakeFetcher{}, nil).ExtractAllSeasons(context.Background(), "https://kino.pub/movies")
	if !errors.Is(err, domain.ErrItemIDUnrecognized) {
		t.Fatalf("err = %v, want ErrItemIDUnrecognized", err)
	}
}

func TestExtractPropagatesFetchError(t *testing.T) {
	ff := &fakeFetcher{err: domain.ErrAPIUnauthorized}
	_, err := New(ff, nil).ExtractAllSeasons(context.Background(), "1")
	if !errors.Is(err, domain.ErrAPIUnauthorized) {
		t.Fatalf("err = %v, want ErrAPIUnauthorized", err)
	}
}
