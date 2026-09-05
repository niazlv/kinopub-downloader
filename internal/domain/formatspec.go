// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FormatSpec is a parsed -f value: the yt-dlp-style way to say what to keep in
// one flag, using the ids and patterns that -F prints.
//
// Tokens are separated by "," (or "+", for yt-dlp muscle memory) and come in
// three shapes: a video selector in -q's grammar ("1080p-h264", "max"), a typed
// track id from the listing ("a1", "s2"), or a free pattern, which selects
// every audio and subtitle track it matches, so "rus" takes all Russian dubs
// and all Russian subtitles at once. A "!" (or "-") prefix turns a pattern into
// an exclusion for both kinds.
type FormatSpec struct {
	Quality   Quality  // video selector; "" leaves the run's -q alone
	Audio     []int    // typed audio ids, 1-based as printed by -F
	Subtitles []int    // typed subtitle ids, 1-based
	Patterns  []string // free patterns, applied to both audio and subtitles
	Excludes  []string // "!pattern" tokens, applied to both kinds
}

var (
	// qualityTokenRe is -q's grammar: optimal, max, 4k or a height, with an
	// optional codec suffix.
	qualityTokenRe = regexp.MustCompile(`(?i)^(?:optimal|max|4k|\d+p?)(?:-h26[45])?$`)
	// trackTokenRe is a typed id as -F prints it: a1, s12.
	trackTokenRe = regexp.MustCompile(`(?i)^([as])(\d+)$`)
)

// ParseFormatSpec parses a -f value. An empty value is the zero spec.
func ParseFormatSpec(s string) (FormatSpec, error) {
	var spec FormatSpec
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '+' }) {
		tok = strings.TrimSpace(tok)
		switch m := trackTokenRe.FindStringSubmatch(tok); {
		case tok == "":
			continue
		case tok[0] == '!' || tok[0] == '-':
			p := strings.TrimSpace(tok[1:])
			if p == "" {
				return FormatSpec{}, fmt.Errorf("%w: -f: %q excludes nothing", ErrInvalidFlag, tok)
			}
			spec.Excludes = append(spec.Excludes, p)
		case qualityTokenRe.MatchString(tok):
			if spec.Quality != "" {
				return FormatSpec{}, fmt.Errorf("%w: -f: two video selectors (%s and %s); a run muxes one video stream",
					ErrInvalidFlag, spec.Quality, tok)
			}
			spec.Quality = Quality(strings.ToLower(tok))
		case m != nil:
			n, _ := strconv.Atoi(m[2])
			if n < 1 {
				return FormatSpec{}, fmt.Errorf("%w: -f: track ids start at 1, got %q", ErrInvalidFlag, tok)
			}
			if strings.EqualFold(m[1], "a") {
				spec.Audio = append(spec.Audio, n)
			} else {
				spec.Subtitles = append(spec.Subtitles, n)
			}
		default:
			spec.Patterns = append(spec.Patterns, tok)
		}
	}
	if spec.IsZero() && strings.TrimSpace(s) != "" {
		return FormatSpec{}, fmt.Errorf("%w: -f: no format selected in %q", ErrInvalidFlag, s)
	}
	return spec, nil
}

// IsZero reports whether the spec selects nothing (no -f given).
func (spec FormatSpec) IsZero() bool {
	return spec.Quality == "" && len(spec.Audio) == 0 && len(spec.Subtitles) == 0 &&
		len(spec.Patterns) == 0 && len(spec.Excludes) == 0
}

// FormatSelection is what a spec resolves to against one episode's listing:
// the ordinary preferences the rest of a run already understands. The Has
// flags say which of them the spec actually set; the others keep whatever the
// run had (its -q, --audio, --subs or the defaults).
type FormatSelection struct {
	Quality      Quality
	HasQuality   bool
	Audio        AudioPreference
	HasAudio     bool
	Subtitles    SubtitlePreference
	HasSubtitles bool
}

// Resolve applies the spec to a listing.
//
// Typed ids become the keyword patterns the interactive picker would derive
// for the same choice, so "a1" keeps that studio across episodes whose track
// order differs. Free patterns pass through untouched to every kind where they
// match something: that is what makes "rus" mean every Russian track. A token
// that matches nothing is an error rather than a fallback: it was copied from
// a listing, so a miss is a typo, not a preference.
func (spec FormatSpec) Resolve(l *FormatListing) (FormatSelection, error) {
	var sel FormatSelection
	if spec.Quality != "" {
		sel.Quality, sel.HasQuality = spec.Quality, true
	}

	var audioInc, subsInc []string
	for _, n := range spec.Audio {
		if n > len(l.Audio) {
			return sel, fmt.Errorf("%w: -f: audio track a%d does not exist (%s offers %d)",
				ErrInvalidFlag, n, l.Episode.Label(), len(l.Audio))
		}
		t := l.Audio[n-1]
		audioInc = append(audioInc, trackPatterns(BuildAudioPreference(l.Audio, []int{n - 1}).Include, t.Name)...)
	}
	for _, n := range spec.Subtitles {
		if n > len(l.Subtitles) {
			return sel, fmt.Errorf("%w: -f: subtitle track s%d does not exist (%s offers %d)",
				ErrInvalidFlag, n, l.Episode.Label(), len(l.Subtitles))
		}
		t := l.Subtitles[n-1]
		subsInc = append(subsInc, trackPatterns(BuildSubtitlePreference(l.Subtitles, []int{n - 1}).Include, t.Name)...)
	}
	for _, p := range spec.Patterns {
		hitAudio, hitSubs := anyTrackMatches(l.Audio, p), anyTrackMatches(l.Subtitles, p)
		if !hitAudio && !hitSubs {
			return sel, fmt.Errorf("%w: -f: nothing in %s matches %q; -F lists what it offers",
				ErrInvalidFlag, l.Episode.Label(), p)
		}
		if hitAudio {
			audioInc = append(audioInc, p)
		}
		if hitSubs {
			subsInc = append(subsInc, p)
		}
	}

	if len(audioInc) > 0 || len(spec.Excludes) > 0 {
		audioInc = dedupeFold(audioInc)
		sel.Audio = AudioPreference{Include: audioInc, Exclude: spec.Excludes, Prefer: DeriveAudioPrefer(l.Audio, audioInc)}
		sel.HasAudio = true
	}
	if len(subsInc) > 0 || len(spec.Excludes) > 0 {
		subsInc = dedupeFold(subsInc)
		sel.Subtitles = SubtitlePreference{Include: subsInc, Exclude: spec.Excludes, Prefer: DeriveSubtitlePrefer(l.Subtitles, subsInc)}
		sel.HasSubtitles = true
	}
	return sel, nil
}

// trackPatterns returns the patterns that pick one track: the derived include
// patterns, or the full name when nothing distinctive could be derived, which
// the substring matcher accepts verbatim.
func trackPatterns(include []string, name string) []string {
	if len(include) == 0 && strings.TrimSpace(name) != "" {
		return []string{strings.TrimSpace(name)}
	}
	return include
}

// anyTrackMatches reports whether pattern selects at least one of the tracks.
func anyTrackMatches(tracks []TrackInfo, pattern string) bool {
	for _, t := range tracks {
		if audioMatches(t, pattern) {
			return true
		}
	}
	return false
}

// dedupeFold drops case-insensitive repeats, keeping first occurrences in order.
func dedupeFold(in []string) []string {
	seen := make(map[string]bool, len(in))
	var out []string
	for _, s := range in {
		k := strings.ToLower(s)
		if !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}
