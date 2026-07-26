// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"fmt"
	"sort"
	"strings"
)

// SubtitlePreference describes which subtitle tracks to keep for a run.
//
// Matching is identical to AudioPreference — substring-based, case-insensitive,
// with language canonicalization — so "--subs rus" and "--audio rus" behave the
// same way. What differs is what happens when nothing matches, because a video
// must carry audio but need not carry subtitles. See SelectSubtitles.
//
// The zero value (no Include, no Exclude) means "keep every track".
type SubtitlePreference struct {
	// Include lists patterns to keep. A track is kept when its name or language
	// contains any Include pattern (case-insensitive). When Include is empty,
	// every track is kept (subject to Exclude).
	Include []string
	// Exclude lists patterns to drop. A track is dropped when its name or
	// language contains any Exclude pattern (case-insensitive). Exclude is
	// applied before Include. If excluding would remove every track, the
	// exclusion is ignored rather than yielding an empty selection.
	Exclude []string
	// Prefer lists language hints used only to rank the fallback track, exactly
	// as AudioPreference.Prefer does.
	Prefer []string

	// Strict disables the fallback: when Include matches nothing the episode
	// yields no subtitles instead of a substitute track. Set by --subs-only,
	// where quietly downloading a language the user did not ask for would
	// produce the wrong artifact. See SelectSubtitles.
	Strict bool

	// Only downloads subtitles alone, skipping video and audio entirely. It
	// travels with the preference so it reaches the downloader through the same
	// single setter the audio preference uses.
	Only bool
}

// IsAll reports whether the preference keeps every available track unchanged.
// Strict and Only describe how a run behaves, not which tracks it keeps, so
// neither affects this.
func (p SubtitlePreference) IsAll() bool {
	return len(p.Include) == 0 && len(p.Exclude) == 0
}

// SelectSubtitles resolves which subtitle tracks to keep for an episode. It
// returns the indices (into tracks) to download, in ascending order.
//
// Algorithm:
//  1. Drop tracks matching any Exclude pattern. If that removes everything, the
//     exclusion is ignored.
//  2. If Include is empty, keep all remaining tracks.
//  3. Otherwise keep the remaining tracks matching any Include pattern.
//  4. If Include matched nothing, behaviour depends on strict:
//     - strict (--subs-only): return nil. The caller must treat this as an
//     error, because the run exists solely to produce those subtitles and
//     silently substituting a different language would be worse than failing.
//     - non-strict: fall back to a single best remaining track, preferring a
//     Prefer-hint language and then source order — mirroring SelectAudio, so a
//     missing "forced russian" still yields some Russian subtitle rather than
//     none.
//
// An episode with no subtitle tracks at all yields nil in both modes; subtitles
// are optional, so only strict callers treat that as a failure.
func SelectSubtitles(tracks []SubtitleTrackInfo, pref SubtitlePreference, strict bool) []int {
	if len(tracks) == 0 {
		return nil
	}

	// 1. Apply excludes.
	remaining := make([]int, 0, len(tracks))
	for i, t := range tracks {
		if !matchesAny(t, pref.Exclude) {
			remaining = append(remaining, i)
		}
	}
	if len(remaining) == 0 {
		for i := range tracks {
			remaining = append(remaining, i)
		}
	}

	// 2. No positive filter → keep everything that survived excludes.
	if len(pref.Include) == 0 {
		return remaining
	}

	// 3. Keep includes among the remaining tracks.
	matched := make([]int, 0, len(remaining))
	for _, i := range remaining {
		if matchesAny(tracks[i], pref.Include) {
			matched = append(matched, i)
		}
	}
	if len(matched) > 0 {
		return matched
	}

	// 4. Nothing matched.
	if strict {
		return nil
	}
	best := append([]int(nil), remaining...)
	sort.SliceStable(best, func(a, b int) bool {
		ra, rb := preferRank(tracks[best[a]], pref.Prefer), preferRank(tracks[best[b]], pref.Prefer)
		if ra != rb {
			return ra < rb
		}
		return best[a] < best[b]
	})
	return []int{best[0]}
}

// DeriveSubtitlePrefer returns the canonical languages of the tracks matched by
// the include patterns, so a fallback prefers the language originally asked for
// (choosing "forced" in a Russian track yields ["rus"], and a missing forced
// track then falls back to another Russian subtitle rather than English).
func DeriveSubtitlePrefer(tracks []SubtitleTrackInfo, include []string) []string {
	return DeriveAudioPrefer(tracks, include)
}

// BuildSubtitlePreference constructs a SubtitlePreference that keeps exactly the
// chosen tracks across episodes.
//
// Unlike audio — where the distinctive token is a studio name that drifts between
// episodes — a subtitle track is identified primarily by its language, which is
// stable. So the language is used as the Include pattern when the source
// provides one, and distinctive name tokens are used only as a fallback for
// sources that label tracks without a language tag.
func BuildSubtitlePreference(tracks []SubtitleTrackInfo, chosen []int) SubtitlePreference {
	var include, prefer []string
	seenInc := make(map[string]bool)
	seenPref := make(map[string]bool)

	add := func(dst *[]string, seen map[string]bool, v string) {
		key := strings.ToLower(v)
		if v != "" && !seen[key] {
			seen[key] = true
			*dst = append(*dst, v)
		}
	}

	for _, idx := range chosen {
		if idx < 0 || idx >= len(tracks) {
			continue
		}
		t := tracks[idx]
		lang := normLang(t.Language)
		if lang == "" {
			lang = parseTrailingLang(t.Name)
		}
		if lang != "" {
			add(&include, seenInc, lang)
			add(&prefer, seenPref, lang)
			continue
		}
		// No language anywhere — fall back to distinctive name tokens.
		for _, kw := range ExtractAudioKeywords(t) {
			add(&include, seenInc, kw)
		}
	}
	return SubtitlePreference{Include: include, Prefer: prefer}
}

// SubtitleSidecarName builds the file name for a subtitle written alongside the
// episode instead of muxed into it, following the widely recognized
// "<base>.<lang>.<ext>" convention that players auto-load.
//
// used records names already handed out for this episode and is updated in
// place. When several tracks share a language, the second and later ones get a
// numeric suffix ("S01E01.rus.srt", then "S01E01.rus.2.srt"), so distinct tracks
// never overwrite each other.
//
// base is the episode file name without extension; ext is the subtitle format
// extension without a dot (e.g. "srt"). Tracks with no usable language tag are
// labelled "und", the ISO 639-2 code for "undetermined".
func SubtitleSidecarName(base string, track SubtitleTrackInfo, ext string, used map[string]bool) string {
	lang := normLang(track.Language)
	if lang == "" {
		lang = parseTrailingLang(track.Name)
	}
	if lang == "" {
		lang = "und"
	}

	name := fmt.Sprintf("%s.%s.%s", base, lang, ext)
	for n := 2; used[name]; n++ {
		name = fmt.Sprintf("%s.%s.%d.%s", base, lang, n, ext)
	}
	used[name] = true
	return name
}
