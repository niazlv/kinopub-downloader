// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"reflect"
	"testing"
)

func subTracks() []SubtitleTrackInfo {
	return []SubtitleTrackInfo{
		{Index: 0, Name: "Русские полные", Language: "rus"},
		{Index: 1, Name: "Русские форсированные", Language: "rus"},
		{Index: 2, Name: "English", Language: "eng"},
	}
}

func TestSelectSubtitles_NoTracks(t *testing.T) {
	for _, strict := range []bool{false, true} {
		if got := SelectSubtitles(nil, SubtitlePreference{}, strict); got != nil {
			t.Errorf("strict=%v: want nil for empty track list, got %v", strict, got)
		}
	}
}

func TestSelectSubtitles_KeepsAllByDefault(t *testing.T) {
	got := SelectSubtitles(subTracks(), SubtitlePreference{}, false)
	if want := []int{0, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestSelectSubtitles_IncludeByLanguage(t *testing.T) {
	got := SelectSubtitles(subTracks(), SubtitlePreference{Include: []string{"rus"}}, false)
	if want := []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

// "ru" must canonicalize to the same language as the "rus" tag on the tracks.
func TestSelectSubtitles_IncludeLanguageAlias(t *testing.T) {
	got := SelectSubtitles(subTracks(), SubtitlePreference{Include: []string{"ru"}}, false)
	if want := []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestSelectSubtitles_Exclude(t *testing.T) {
	got := SelectSubtitles(subTracks(), SubtitlePreference{Exclude: []string{"eng"}}, false)
	if want := []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

// Excluding everything must be ignored rather than yielding an empty selection.
func TestSelectSubtitles_ExcludeAllIsIgnored(t *testing.T) {
	pref := SubtitlePreference{Exclude: []string{"rus", "eng"}}
	got := SelectSubtitles(subTracks(), pref, false)
	if want := []int{0, 1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

// Non-strict: a missing selection falls back to exactly one track, and the
// Prefer hint decides which — here Russian rather than the first English track.
func TestSelectSubtitles_FallbackPrefersHintedLanguage(t *testing.T) {
	tracks := []SubtitleTrackInfo{
		{Index: 0, Name: "English", Language: "eng"},
		{Index: 1, Name: "Русские", Language: "rus"},
	}
	pref := SubtitlePreference{Include: []string{"forced"}, Prefer: []string{"rus"}}
	got := SelectSubtitles(tracks, pref, false)
	if want := []int{1}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v (russian fallback), got %v", want, got)
	}
}

// Without a Prefer hint the fallback is the track highest in the source list,
// keeping the result deterministic.
func TestSelectSubtitles_FallbackIsDeterministic(t *testing.T) {
	pref := SubtitlePreference{Include: []string{"klingon"}}
	got := SelectSubtitles(subTracks(), pref, false)
	if want := []int{0}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

// Strict mode is the whole point of --subs-only: substituting a language the
// user did not ask for would silently produce the wrong artifact.
func TestSelectSubtitles_StrictReturnsNilWhenUnmatched(t *testing.T) {
	pref := SubtitlePreference{Include: []string{"klingon"}, Prefer: []string{"rus"}}
	if got := SelectSubtitles(subTracks(), pref, true); got != nil {
		t.Errorf("strict mode must not fall back, got %v", got)
	}
}

// A strict run that *can* be satisfied must still return every match.
func TestSelectSubtitles_StrictKeepsMatches(t *testing.T) {
	pref := SubtitlePreference{Include: []string{"rus"}}
	got := SelectSubtitles(subTracks(), pref, true)
	if want := []int{0, 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestSubtitlePreference_IsAll(t *testing.T) {
	if !(SubtitlePreference{}).IsAll() {
		t.Error("zero value must report IsAll")
	}
	if (SubtitlePreference{Include: []string{"rus"}}).IsAll() {
		t.Error("Include must clear IsAll")
	}
	if (SubtitlePreference{Exclude: []string{"eng"}}).IsAll() {
		t.Error("Exclude must clear IsAll")
	}
	// Prefer only ranks the fallback; it does not filter anything.
	if !(SubtitlePreference{Prefer: []string{"rus"}}).IsAll() {
		t.Error("Prefer alone must not clear IsAll")
	}
}

func TestBuildSubtitlePreference_UsesLanguage(t *testing.T) {
	pref := BuildSubtitlePreference(subTracks(), []int{0})
	if want := []string{"rus"}; !reflect.DeepEqual(pref.Include, want) {
		t.Errorf("Include: want %v, got %v", want, pref.Include)
	}
	if want := []string{"rus"}; !reflect.DeepEqual(pref.Prefer, want) {
		t.Errorf("Prefer: want %v, got %v", want, pref.Prefer)
	}
}

// Two tracks in one language must not produce a duplicated pattern.
func TestBuildSubtitlePreference_Deduplicates(t *testing.T) {
	pref := BuildSubtitlePreference(subTracks(), []int{0, 1})
	if want := []string{"rus"}; !reflect.DeepEqual(pref.Include, want) {
		t.Errorf("want %v, got %v", want, pref.Include)
	}
}

func TestBuildSubtitlePreference_IgnoresOutOfRange(t *testing.T) {
	pref := BuildSubtitlePreference(subTracks(), []int{-1, 99})
	if !pref.IsAll() {
		t.Errorf("out-of-range indices must yield an empty preference, got %+v", pref)
	}
}

// A source that labels tracks without a language tag still needs a selector.
func TestBuildSubtitlePreference_FallsBackToNameKeywords(t *testing.T) {
	tracks := []SubtitleTrackInfo{{Index: 0, Name: "Crunchyroll"}}
	pref := BuildSubtitlePreference(tracks, []int{0})
	if len(pref.Include) == 0 {
		t.Fatal("want a name-derived pattern, got none")
	}
	if !matchesAny(tracks[0], pref.Include) {
		t.Errorf("derived pattern %v does not match its own track", pref.Include)
	}
}

func TestSubtitleSidecarName(t *testing.T) {
	used := map[string]bool{}
	tracks := subTracks()

	if got := SubtitleSidecarName("S01E01", tracks[0], "srt", used); got != "S01E01.rus.srt" {
		t.Errorf("got %q", got)
	}
	// Second Russian track must not overwrite the first.
	if got := SubtitleSidecarName("S01E01", tracks[1], "srt", used); got != "S01E01.rus.2.srt" {
		t.Errorf("got %q", got)
	}
	if got := SubtitleSidecarName("S01E01", tracks[2], "srt", used); got != "S01E01.eng.srt" {
		t.Errorf("got %q", got)
	}
}

func TestSubtitleSidecarName_UndeterminedLanguage(t *testing.T) {
	used := map[string]bool{}
	got := SubtitleSidecarName("S01E01", SubtitleTrackInfo{Name: "Subtitles"}, "srt", used)
	if got != "S01E01.und.srt" {
		t.Errorf("got %q", got)
	}
}

// The language may only be present as a trailing parenthetical in the name.
func TestSubtitleSidecarName_LanguageFromName(t *testing.T) {
	used := map[string]bool{}
	got := SubtitleSidecarName("S01E01", SubtitleTrackInfo{Name: "Forced (RUS)"}, "srt", used)
	if got != "S01E01.rus.srt" {
		t.Errorf("got %q", got)
	}
}
