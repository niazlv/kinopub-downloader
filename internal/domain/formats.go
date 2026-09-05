// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"strings"
	"time"
)

// FormatListing is what --list-formats reports: the renditions one episode's
// master playlist offers. It is built from the same probes the interactive
// pickers use, so what it shows is exactly what a run could choose, and the
// selectors derived from it (AudioSelector, SubtitleSelector, the video
// Quality) reproduce a choice on the command line.
type FormatListing struct {
	// Episode is the probed episode and Title its name.
	Episode EpisodeKey
	Title   string
	// Duration is the episode length; it turns bitrates into size estimates.
	Duration time.Duration
	// Matching is how many episodes the selection covers. The listing describes
	// the first; a series normally offers the same renditions for the rest.
	Matching int
	// Feed is set when the listing comes from a podcast feed. Its entries are
	// finished files: -f picks a file by quality, and the audio and subtitle
	// tracks shown are what the file contains, not choices.
	Feed bool

	Video     []VideoQualityInfo
	Audio     []AudioTrackInfo
	Subtitles []SubtitleTrackInfo
}

// AudioSelector returns the --audio pattern that picks the given track. It is
// derived exactly the way the interactive picker turns a choice into a
// preference, so the listing and the menu can never disagree about what a
// pattern selects. Lower case, because matching is case-insensitive and the
// value is meant to be typed.
func AudioSelector(t AudioTrackInfo) string {
	return selector(BuildAudioPreference([]AudioTrackInfo{t}, []int{0}).Include, t.Name)
}

// SubtitleSelector is AudioSelector for subtitles: the language when the
// source names one, distinctive name tokens otherwise.
func SubtitleSelector(t SubtitleTrackInfo) string {
	return selector(BuildSubtitlePreference([]SubtitleTrackInfo{t}, []int{0}).Include, t.Name)
}

// selector joins the include patterns into one --audio/--subs value. A track
// that yields no pattern at all (no language, no distinctive token) falls back
// to its full name, which the substring matcher accepts verbatim.
func selector(include []string, name string) string {
	if len(include) == 0 {
		return strings.ToLower(strings.TrimSpace(name))
	}
	return strings.ToLower(strings.Join(include, ","))
}
