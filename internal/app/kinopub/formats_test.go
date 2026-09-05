// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// listingPageScraper serves a two-season playlist without touching the network.
type listingPageScraper struct{}

func (listingPageScraper) ExtractAllSeasons(context.Context, string) (*domain.PagePlaylist, error) {
	return &domain.PagePlaylist{
		ItemID: 38290,
		Title:  "Slime",
		Episodes: []domain.PageEpisode{
			{ManifestURL: "https://cdn.example/s1e1.m3u8", EpisodeTitle: "One", Duration: 1423, Season: 1, Episode: 1},
			{ManifestURL: "https://cdn.example/s1e2.m3u8", EpisodeTitle: "Two", Duration: 1400, Season: 1, Episode: 2},
			{ManifestURL: "https://cdn.example/s2e1.m3u8", EpisodeTitle: "Three", Duration: 1500, Season: 2, Episode: 1},
		},
	}, nil
}

// listingHLSDownloader answers the probes with canned renditions and records
// what a run asked of it: the manifests probed, the preferences set, and the
// quality of any download attempt (which itself always fails, so nothing is
// ever muxed).
type listingHLSDownloader struct {
	stubHLSDownloader
	probed    []string
	downloads int
	quality   domain.Quality
	audioPref domain.AudioPreference
	subsPref  domain.SubtitlePreference
}

func (d *listingHLSDownloader) DownloadEpisode(_ context.Context, _ string, quality domain.Quality, _ string,
	_ domain.EpisodeKey, _ domain.ProgressSink) (*domain.HLSDownloadResult, error) {
	d.downloads++
	d.quality = quality
	return nil, errors.New("stub downloader: no download")
}

func (d *listingHLSDownloader) SetAudioPreference(p domain.AudioPreference)       { d.audioPref = p }
func (d *listingHLSDownloader) SetSubtitlePreference(p domain.SubtitlePreference) { d.subsPref = p }

func (d *listingHLSDownloader) ListVideoQualities(_ context.Context, manifestURL string) ([]domain.VideoQualityInfo, error) {
	d.probed = append(d.probed, manifestURL)
	return []domain.VideoQualityInfo{
		{Height: 1080, Width: 1920, Codec: "h264", BitrateKbps: 3805, Quality: "1080p-h264"},
		{Height: 406, Width: 720, Codec: "h264", BitrateKbps: 1060, Quality: "406p-h264"},
	}, nil
}

func (d *listingHLSDownloader) ListAudioTracks(context.Context, string, domain.Quality) ([]domain.AudioTrackInfo, error) {
	return []domain.AudioTrackInfo{
		{Index: 0, Name: "01. Многоголосый. StudioBand (RUS)", Language: "rus"},
		{Index: 1, Name: "02. Оригинал (JPN)", Language: "jpn"},
	}, nil
}

func (d *listingHLSDownloader) ListSubtitleTracks(context.Context, string, domain.Quality) ([]domain.SubtitleTrackInfo, error) {
	return []domain.SubtitleTrackInfo{
		{Index: 0, Name: "RUS #01", Language: "rus"},
		{Index: 1, Name: "ENG #02", Language: "eng"},
	}, nil
}

func (d *listingHLSDownloader) ProbeTrackStats(context.Context, string, domain.Quality) ([]domain.TrackStats, []domain.TrackStats, error) {
	return []domain.TrackStats{{Codec: "mp4a.40.2", BitrateKbps: 128, SizeBytes: 22720000}, {Codec: "mp4a.40.2"}},
		[]domain.TrackStats{{BitrateKbps: 1, SizeBytes: 90000}, {}}, nil
}

func listingApp(t *testing.T) (*App, *listingHLSDownloader) {
	t.Helper()
	deps := validDeps()
	deps.PageScraper = listingPageScraper{}
	hls := &listingHLSDownloader{}
	deps.HLSDownloader = hls
	app, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app, hls
}

func TestRun_ListFormats_ProbesFirstMatchingEpisodeWithoutDownloading(t *testing.T) {
	app, hls := listingApp(t)
	cfg := domain.RunConfig{InputURL: "https://kino.watch/item/view/38290", ListFormats: true}
	ApplyDefaults(&cfg)

	res, err := app.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hls.downloads != 0 {
		t.Fatalf("a listing run downloaded %d episodes", hls.downloads)
	}
	if res.Formats == nil {
		t.Fatal("RunResult.Formats is nil")
	}
	l := res.Formats
	if want := (domain.EpisodeKey{Series: "38290", Season: 1, Episode: 1}); l.Episode != want {
		t.Errorf("probed episode %+v, want %+v", l.Episode, want)
	}
	if l.Title != "One" || l.Duration != 1423*time.Second {
		t.Errorf("episode title/duration = %q/%s, want One/23m43s", l.Title, l.Duration)
	}
	if l.Matching != 3 || res.Total != 3 {
		t.Errorf("matching = %d, total = %d, want 3 and 3", l.Matching, res.Total)
	}
	if len(l.Video) != 2 || len(l.Audio) != 2 || len(l.Subtitles) != 2 {
		t.Errorf("renditions: video %d, audio %d, subs %d", len(l.Video), len(l.Audio), len(l.Subtitles))
	}
	if strings.Join(hls.probed, ",") != "https://cdn.example/s1e1.m3u8" {
		t.Errorf("probed manifests = %v, want only the first episode's", hls.probed)
	}
	if len(l.AudioStats) != 2 || l.AudioStats[0].BitrateKbps != 128 || len(l.SubtitleStats) != 2 {
		t.Errorf("track stats not attached: audio %+v, subs %+v", l.AudioStats, l.SubtitleStats)
	}
}

// An episode reference in the link narrows the listing to that episode, the
// same way it narrows a download.
func TestRun_ListFormats_HonoursEpisodeInURL(t *testing.T) {
	app, hls := listingApp(t)
	cfg := domain.RunConfig{InputURL: "https://kino.watch/item/view/38290/s2e1", ListFormats: true}
	ApplyURLEpisodeRef(&cfg, false, false)
	ApplyDefaults(&cfg)

	res, err := app.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := (domain.EpisodeKey{Series: "38290", Season: 2, Episode: 1}); res.Formats.Episode != want {
		t.Errorf("probed episode %+v, want %+v", res.Formats.Episode, want)
	}
	if res.Formats.Matching != 1 {
		t.Errorf("matching = %d, want 1", res.Formats.Matching)
	}
	if strings.Join(hls.probed, ",") != "https://cdn.example/s2e1.m3u8" {
		t.Errorf("probed manifests = %v", hls.probed)
	}
}

// feedApp wires the RSS path: a feed with two episodes and a resolver that
// answers the probe with the given media (or error).
func feedApp(t *testing.T, resolver domain.MediaResolver) *App {
	t.Helper()
	deps := validDeps()
	deps.InputResolver = &mockInputResolver{source: domain.FeedSource{ID: "123", Token: "abc"}}
	deps.FeedParser = &mockFeedParser{series: testSeries()}
	deps.MediaResolver = resolver
	app, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

// On the feed path the listing shows what the feed declares, enriched with the
// tracks ffprobe finds inside the file the run would pick.
func TestRun_ListFormats_FeedListsTheFile(t *testing.T) {
	app := feedApp(t, &mockMediaResolver{media: domain.ResolvedMedia{
		Source:    domain.MediaSource{Quality: "1080p"},
		Video:     domain.VideoTrack{Resolution: "1920x1080", BitRate: 3805},
		Audio:     []domain.AudioTrack{{Language: "rus", Studio: "StudioBand"}, {Language: "jpn"}},
		Subtitles: []domain.SubtitleTrack{{Language: "rus", Source: "RUS"}},
		Duration:  1420 * time.Second,
	}})
	cfg := domain.RunConfig{InputURL: "https://kino.pub/podcast/get/123/abc", ListFormats: true}
	ApplyDefaults(&cfg)

	res, err := app.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	l := res.Formats
	if l == nil || !l.Feed {
		t.Fatalf("want a feed listing, got %+v", l)
	}
	if l.Episode != (domain.EpisodeKey{Series: "test", Season: 1, Episode: 1}) || l.Matching != 2 {
		t.Errorf("episode %+v matching %d, want S01E01 of 2", l.Episode, l.Matching)
	}
	if len(l.Video) != 1 || l.Video[0].Quality != "1080p" || l.Video[0].Width != 1920 || l.Video[0].BitrateKbps != 3805 {
		t.Errorf("video rows = %+v", l.Video)
	}
	if len(l.Audio) != 2 || l.Audio[0].Name != "StudioBand" || l.Audio[1].Language != "jpn" || len(l.Subtitles) != 1 {
		t.Errorf("tracks: audio %+v, subs %+v", l.Audio, l.Subtitles)
	}
	if l.Duration != 1420*time.Second {
		t.Errorf("duration = %s, want the probed 23m40s", l.Duration)
	}
}

// Without ffprobe the feed path still lists what the feed declares.
func TestRun_ListFormats_FeedWithoutProbe(t *testing.T) {
	app := feedApp(t, &mockMediaResolver{err: errors.New("ffprobe: executable not found")})
	cfg := domain.RunConfig{InputURL: "https://kino.pub/podcast/get/123/abc", ListFormats: true}
	ApplyDefaults(&cfg)

	res, err := app.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Formats == nil || len(res.Formats.Video) != 1 || res.Formats.Video[0].Quality != "1080p" || res.Formats.Video[0].Height != 1080 {
		t.Fatalf("want the declared quality, got %+v", res.Formats)
	}
	if len(res.Formats.Audio) != 0 {
		t.Errorf("no probe, yet audio rows: %+v", res.Formats.Audio)
	}
}

// -f is resolved against the first episode and reaches the downloader as the
// ordinary quality and track preferences.
func TestRun_FormatSpec_SetsPreferencesFromListing(t *testing.T) {
	app, hls := listingApp(t)
	spec, err := domain.ParseFormatSpec("406p-h264+a2+s1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := domain.RunConfig{InputURL: "https://kino.watch/item/view/38290/s1e1", FormatSpec: spec}
	ApplyURLEpisodeRef(&cfg, false, false)
	ApplyDefaults(&cfg)

	if _, err := app.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hls.downloads != 1 || hls.quality != "406p-h264" {
		t.Errorf("download attempts = %d at %q, want 1 at 406p-h264", hls.downloads, hls.quality)
	}
	if got := strings.Join(hls.audioPref.Include, ","); got != "jpn" {
		t.Errorf("audio include = %q, want jpn (what a2 stands for)", got)
	}
	if got := strings.Join(hls.subsPref.Include, ","); got != "rus" {
		t.Errorf("subtitle include = %q, want rus (what s1 stands for)", got)
	}
}

// A pattern keeps every match in both kinds; -q stays as configured when the
// spec names no quality.
func TestRun_FormatSpec_PatternKeepsEveryMatch(t *testing.T) {
	app, hls := listingApp(t)
	spec, _ := domain.ParseFormatSpec("rus")
	cfg := domain.RunConfig{InputURL: "https://kino.watch/item/view/38290/s1e1", FormatSpec: spec, Quality: "max"}
	ApplyURLEpisodeRef(&cfg, false, false)
	ApplyDefaults(&cfg)

	if _, err := app.Run(context.Background(), cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if hls.quality != "max" {
		t.Errorf("quality = %q, want the configured max", hls.quality)
	}
	if got := strings.Join(hls.audioPref.Include, ","); got != "rus" {
		t.Errorf("audio include = %q, want rus", got)
	}
	if got := strings.Join(hls.subsPref.Include, ","); got != "rus" {
		t.Errorf("subtitle include = %q, want rus", got)
	}
}

// metadataCountingStore records whether a run got as far as writing the
// series metadata, which is the first thing that creates the series directory.
type metadataCountingStore struct {
	mockStateStore
	metadataWrites int
}

func (s *metadataCountingStore) SetMetadata(context.Context, domain.SeriesID, domain.SeriesMetadata) error {
	s.metadataWrites++
	return nil
}

// A token that matches nothing stops the run before any download, and before
// anything is written to disk: a typo must not leave a series directory behind.
func TestRun_FormatSpec_MissIsAnError(t *testing.T) {
	deps := validDeps()
	deps.PageScraper = listingPageScraper{}
	hls := &listingHLSDownloader{}
	deps.HLSDownloader = hls
	store := &metadataCountingStore{}
	deps.StateStore = store
	app, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	spec, _ := domain.ParseFormatSpec("klingon")
	cfg := domain.RunConfig{InputURL: "https://kino.watch/item/view/38290", FormatSpec: spec}
	ApplyDefaults(&cfg)

	_, err = app.Run(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), `nothing in S01E01 matches "klingon"`) {
		t.Fatalf("want a miss error, got: %v", err)
	}
	if hls.downloads != 0 {
		t.Errorf("downloaded %d episodes despite the error", hls.downloads)
	}
	if store.metadataWrites != 0 {
		t.Errorf("series metadata was written %d times before the spec was checked", store.metadataWrites)
	}
}

// prefRecordingResolver remembers the quality preference each probe was given.
// The RSS pipeline resolves episodes from several workers, hence the mutex.
type prefRecordingResolver struct {
	mockMediaResolver
	mu    sync.Mutex
	prefs []domain.QualityPref
}

func (r *prefRecordingResolver) Resolve(ctx context.Context, ep domain.Episode, pref domain.QualityPref) (domain.ResolvedMedia, error) {
	r.mu.Lock()
	r.prefs = append(r.prefs, pref)
	r.mu.Unlock()
	return r.mockMediaResolver.Resolve(ctx, ep, pref)
}

// On the feed path -f can only pick the file: its quality part applies as -q
// would, the track part is reported, and the download goes ahead.
func TestRun_FormatSpec_OnFeedPicksOnlyTheFile(t *testing.T) {
	resolver := &prefRecordingResolver{mockMediaResolver: mockMediaResolver{media: domain.ResolvedMedia{
		Video: domain.VideoTrack{Resolution: "1920x1080"},
	}}}
	app := feedApp(t, resolver)
	spec, _ := domain.ParseFormatSpec("720p+rus")
	cfg := domain.RunConfig{InputURL: "https://kino.pub/podcast/get/123/abc", FormatSpec: spec}
	ApplyDefaults(&cfg)

	res, err := app.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Succeeded != 2 {
		t.Errorf("succeeded = %d, want both episodes downloaded", res.Succeeded)
	}
	if len(resolver.prefs) == 0 {
		t.Fatal("the resolver was never asked")
	}
	for _, p := range resolver.prefs {
		if p != "720p" {
			t.Errorf("probe asked for quality %q, want 720p from -f", p)
		}
	}
}
