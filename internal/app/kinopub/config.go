// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// ValidateConfig validates all config fields and returns ErrInvalidFlag with a
// descriptive message for out-of-range or invalid values.
func ValidateConfig(cfg *domain.RunConfig) error {
	if cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > 16 {
		return fmt.Errorf("%w: max concurrency must be in [1,16], got %d", domain.ErrInvalidFlag, cfg.MaxConcurrency)
	}

	switch cfg.Verbosity {
	case domain.VerbosityQuiet, domain.VerbosityNormal, domain.VerbosityVerbose:
		// valid
	default:
		return fmt.Errorf("%w: verbosity must be quiet, normal, or verbose, got %d", domain.ErrInvalidFlag, cfg.Verbosity)
	}

	if cfg.ProxyURL != "" {
		if err := validateProxyURL(cfg.ProxyURL); err != nil {
			return err
		}
	}

	switch cfg.Container {
	case domain.ContainerMKV, domain.ContainerMP4:
		// valid
	default:
		return fmt.Errorf("%w: container must be mkv or mp4, got %d", domain.ErrInvalidFlag, cfg.Container)
	}

	// --subs-only only works through the HLS pipeline, which needs a page link
	// and downloads each rendition separately. Both of these flags force the RSS
	// pipeline instead, where there are no separate subtitle tracks to fetch —
	// so the run would download full episodes, the exact opposite of the intent.
	if cfg.SubtitlesOnly {
		if cfg.FeedFile != "" {
			return fmt.Errorf("%w: --subs-only cannot be combined with --feed-file; it needs a page link (…/item/view/…)",
				domain.ErrInvalidFlag)
		}
		if cfg.NoChunked {
			return fmt.Errorf("%w: --subs-only cannot be combined with --no-chunked; it needs the HLS pipeline",
				domain.ErrInvalidFlag)
		}
	}

	return nil
}

// validateProxyURL checks that a proxy URL has a valid scheme and host.
func validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: proxy URL is malformed: %v", domain.ErrInvalidFlag, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		// valid scheme
	default:
		return fmt.Errorf("%w: proxy URL scheme must be http, https, or socks5, got %q", domain.ErrInvalidFlag, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: proxy URL must have a host", domain.ErrInvalidFlag)
	}
	return nil
}

// ApplyDefaults fills in default values for unset fields in the config.
func ApplyDefaults(cfg *domain.RunConfig) {
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 2
	}
	if cfg.Verbosity == domain.VerbosityUnset {
		cfg.Verbosity = domain.VerbosityNormal
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.Container == 0 {
		cfg.Container = domain.ContainerMKV
	}
	if !cfg.SeasonSel.All && len(cfg.SeasonSel.Values) == 0 && len(cfg.SeasonSel.Ranges) == 0 {
		cfg.SeasonSel = domain.Selection{All: true}
	}
	if !cfg.EpisodeSel.All && len(cfg.EpisodeSel.Values) == 0 && len(cfg.EpisodeSel.Ranges) == 0 {
		cfg.EpisodeSel = domain.Selection{All: true}
	}
	if cfg.AudioMenu && cfg.AudioMenuTimeout == 0 {
		cfg.AudioMenuTimeout = 90 * time.Second
	}
	if cfg.SubsMenu && cfg.SubsMenuTimeout == 0 {
		cfg.SubsMenuTimeout = 90 * time.Second
	}
	// --subs-only produces nothing but subtitle files, so it necessarily writes
	// them beside the episode rather than into a container that is never built.
	if cfg.SubtitlesOnly {
		cfg.SubsExternal = true
	}
}

// ParseAudioPreference parses an --audio selector string into an
// AudioPreference. The syntax is a comma-separated list of patterns; a pattern
// prefixed with "!" or "-" is an exclusion, everything else is an inclusion.
// Matching is substring/language based and case-insensitive (see
// domain.AudioPreference). Examples:
//
//	"anilibria"        keep only tracks matching "anilibria"
//	"!jpn"             drop the Japanese track, keep the rest
//	"anilibria,!jpn"   keep AniLibria, and never the Japanese track
//	"" or "all"        keep every track
func ParseAudioPreference(s string) (domain.AudioPreference, error) {
	include, exclude, err := parseTrackSelector(s, "audio")
	if err != nil {
		return domain.AudioPreference{}, err
	}
	return domain.AudioPreference{Include: include, Exclude: exclude}, nil
}

// ParseSubtitlePreference parses a --subs selector into a SubtitlePreference.
// The syntax is identical to --audio, so "rus", "!eng" and "rus,!eng" mean the
// same thing for both flags.
func ParseSubtitlePreference(s string) (domain.SubtitlePreference, error) {
	include, exclude, err := parseTrackSelector(s, "subtitle")
	if err != nil {
		return domain.SubtitlePreference{}, err
	}
	return domain.SubtitlePreference{Include: include, Exclude: exclude}, nil
}

// parseTrackSelector splits a comma-separated selector into inclusion and
// exclusion patterns. A pattern prefixed with "!" or "-" is an exclusion. kind
// names the track type in error messages.
func parseTrackSelector(s, kind string) (include, exclude []string, err error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "all") {
		return nil, nil, nil
	}

	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		neg := false
		for len(part) > 0 && (part[0] == '!' || part[0] == '-') {
			neg = true
			part = strings.TrimSpace(part[1:])
		}
		if part == "" {
			return nil, nil, fmt.Errorf("%w: empty %s pattern in %q", domain.ErrInvalidFlag, kind, s)
		}
		if neg {
			exclude = append(exclude, part)
		} else {
			include = append(include, part)
		}
	}
	return include, exclude, nil
}

// ParseSelection parses a selection string like "1,3-5,8" into a Selection.
// An empty string returns Selection{All: true}.
// Supports single numbers ("1,3,5"), ranges ("1-5"), and mixed ("1,3-5,8").
func ParseSelection(s string) (domain.Selection, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return domain.Selection{All: true}, nil
	}

	sel := domain.Selection{
		Values: make(map[int]bool),
	}

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return domain.Selection{}, fmt.Errorf("%w: empty element in selection %q", domain.ErrInvalidFlag, s)
		}

		if idx := strings.Index(part, "-"); idx >= 0 {
			loStr := strings.TrimSpace(part[:idx])
			hiStr := strings.TrimSpace(part[idx+1:])

			lo, err := strconv.Atoi(loStr)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid range start %q in selection %q", domain.ErrInvalidFlag, loStr, s)
			}
			hi, err := strconv.Atoi(hiStr)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid range end %q in selection %q", domain.ErrInvalidFlag, hiStr, s)
			}
			if lo > hi {
				return domain.Selection{}, fmt.Errorf("%w: range start %d > end %d in selection %q", domain.ErrInvalidFlag, lo, hi, s)
			}
			sel.Ranges = append(sel.Ranges, domain.SelectionRange{Lo: lo, Hi: hi})
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid number %q in selection %q", domain.ErrInvalidFlag, part, s)
			}
			sel.Values[n] = true
		}
	}

	return sel, nil
}
