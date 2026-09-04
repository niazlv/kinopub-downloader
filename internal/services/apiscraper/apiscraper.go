// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package apiscraper builds the download pipeline's playlist from the kino.pub
// JSON API instead of scraping the website.
//
// It implements domain.PageScraper so it is a drop-in replacement for the HTML
// scraper: given a kino.pub item URL it fetches the item over the API and emits
// a PagePlaylist whose per-episode manifest URLs are the API's signed hls4
// masters. Those masters expose every quality, audio track and subtitle in the
// standard HLS shape, so the existing HLS resolver, downloader and mux stages
// consume them unchanged — and, being pre-signed, they need no auth header of
// their own.
package apiscraper

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

// ItemFetcher is the API surface the scraper needs; satisfied by *kinopub.Client.
type ItemFetcher interface {
	Item(ctx context.Context, id string) (kinopub.Item, error)
}

// Scraper turns kino.pub item links into playlists via the JSON API.
type Scraper struct {
	api            ItemFetcher
	logger         domain.Logger
	preferredCodec string // "h264" (default) or "h265"; the master still exposes all qualities of that codec
}

// Option configures a Scraper.
type Option func(*Scraper)

// WithPreferredCodec picks which codec family's manifest to hand downstream
// when an episode offers more than one (e.g. h264 and h265). Defaults to h264
// for broad container/player compatibility.
func WithPreferredCodec(codec string) Option {
	return func(s *Scraper) {
		if codec != "" {
			s.preferredCodec = codec
		}
	}
}

// New builds a Scraper over the given fetcher.
func New(api ItemFetcher, logger domain.Logger, opts ...Option) *Scraper {
	s := &Scraper{api: api, preferredCodec: "h264"}
	if logger != nil {
		s.logger = logger.Component("apiscraper")
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// itemIDRe matches the numeric item id in a kino.pub item URL, tolerating the
// /item/view/, /item/ and bare-path forms and any trailing /sNeM segment.
var itemIDRe = regexp.MustCompile(`(?:/item(?:/view)?/|^)(\d+)(?:/|$)`)

// ParseItemID reports whether a URL names a kino.pub item this backend can
// fetch, returning its numeric id. Callers use it to decide whether the app
// session applies to a given input — a podcast feed or a local file does not.
func ParseItemID(raw string) (string, error) { return parseItemID(raw) }

// parseItemID extracts the numeric item id from a URL or accepts a bare numeric
// id. Returns ErrItemIDUnrecognized when neither form is present.
func parseItemID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", domain.ErrItemIDUnrecognized
	}
	if isAllDigits(raw) {
		return raw, nil
	}
	if m := itemIDRe.FindStringSubmatch(raw); m != nil {
		return m[1], nil
	}
	return "", domain.ErrItemIDUnrecognized
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ExtractAllSeasons implements domain.PageScraper. It fetches the item and maps
// its videos (movie) or seasons/episodes (serial) into a PagePlaylist.
func (s *Scraper) ExtractAllSeasons(ctx context.Context, baseURL string) (*domain.PagePlaylist, error) {
	id, err := parseItemID(baseURL)
	if err != nil {
		return nil, err
	}

	item, err := s.api.Item(ctx, id)
	if err != nil {
		return nil, err
	}

	pl := &domain.PagePlaylist{
		ItemID: item.ID,
		Title:  item.Title,
		Poster: item.Posters.PosterURL(),
	}

	switch {
	case len(item.Seasons) > 0:
		for _, season := range item.Seasons {
			for _, ep := range season.Episodes {
				if pe, ok := s.episode(ep, season.Number, item.Title); ok {
					pl.Episodes = append(pl.Episodes, pe)
				}
			}
		}
	default:
		// Movie (and anything else with top-level videos): each video is one
		// episode. A movie's single video maps to S01E01.
		for i, v := range item.Videos {
			if pe, ok := s.episode(v, 1, item.Title); ok {
				if pe.Episode == 0 {
					pe.Episode = i + 1
				}
				pl.Episodes = append(pl.Episodes, pe)
			}
		}
	}

	if len(pl.Episodes) == 0 {
		return nil, domain.ErrNoVideoTrack
	}

	pl.Seasons = seasonCounts(pl.Episodes)
	s.debug("built playlist from API",
		domain.F("item_id", pl.ItemID),
		domain.F("title", pl.Title),
		domain.F("episodes", len(pl.Episodes)),
		domain.F("seasons", len(pl.Seasons)),
	)
	return pl, nil
}

// episode maps one API video to a PageEpisode, choosing the manifest URL. It
// returns ok=false (and logs) when the video carries no usable manifest.
func (s *Scraper) episode(v kinopub.Video, seasonHint int, itemTitle string) (domain.PageEpisode, bool) {
	manifest, ok := pickManifest(v.Files, s.preferredCodec)
	if !ok {
		s.debug("skipping video with no hls manifest",
			domain.F("video_id", v.ID), domain.F("title", v.Title))
		return domain.PageEpisode{}, false
	}

	season := v.SNumber
	if season == 0 {
		season = seasonHint
	}
	if season == 0 {
		season = 1
	}
	episode := v.Number
	title := v.Title
	if title == "" {
		title = itemTitle
	}

	return domain.PageEpisode{
		ManifestURL:  manifest,
		MediaID:      v.ID,
		EpisodeTitle: title,
		Duration:     v.Duration,
		Season:       season,
		Episode:      episode,
	}, true
}

// pickManifest selects the hls4 master to download, preferring the requested
// codec and the highest quality within it. It falls back across codecs and,
// only if no hls4 is present, to the CDN hls master.
func pickManifest(files []kinopub.File, preferredCodec string) (string, bool) {
	best := -1
	bestURL := ""
	bestPreferred := false
	for _, f := range files {
		url := f.URL.HLS4
		if url == "" {
			url = f.URL.HLS
		}
		if url == "" {
			continue
		}
		isPreferred := strings.EqualFold(f.Codec, preferredCodec)
		// Prefer the requested codec; within a codec tier, prefer higher
		// quality_id. A preferred-codec file always beats a non-preferred one.
		switch {
		case bestURL == "":
			best, bestURL, bestPreferred = f.QualityID, url, isPreferred
		case isPreferred && !bestPreferred:
			best, bestURL, bestPreferred = f.QualityID, url, true
		case isPreferred == bestPreferred && f.QualityID > best:
			best, bestURL = f.QualityID, url
		}
	}
	return bestURL, bestURL != ""
}

// seasonCounts summarizes episodes per season, ascending by season number.
func seasonCounts(eps []domain.PageEpisode) []domain.PageSeason {
	counts := map[int]int{}
	for _, e := range eps {
		counts[e.Season]++
	}
	nums := make([]int, 0, len(counts))
	for n := range counts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]domain.PageSeason, 0, len(nums))
	for _, n := range nums {
		out = append(out, domain.PageSeason{Season: n, Count: counts[n]})
	}
	return out
}

func (s *Scraper) debug(msg string, fields ...domain.Field) {
	if s.logger != nil {
		s.logger.Debug(msg, fields...)
	}
}

// compile-time guard: Scraper satisfies the port.
var _ domain.PageScraper = (*Scraper)(nil)
