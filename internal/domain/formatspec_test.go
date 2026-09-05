// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseFormatSpec(t *testing.T) {
	cases := []struct {
		in   string
		want FormatSpec
	}{
		{"", FormatSpec{}},
		{"1080p-h264", FormatSpec{Quality: "1080p-h264"}},
		{"MAX", FormatSpec{Quality: "max"}},
		{"a1,s2", FormatSpec{Audio: []int{1}, Subtitles: []int{2}}},
		// "+" is accepted for yt-dlp muscle memory; typed ids are case-insensitive
		{"720p+A2+S1", FormatSpec{Quality: "720p", Audio: []int{2}, Subtitles: []int{1}}},
		{"rus, !jpn, -eng", FormatSpec{Patterns: []string{"rus"}, Excludes: []string{"jpn", "eng"}}},
		{"studioband,rus", FormatSpec{Patterns: []string{"studioband", "rus"}}},
	}
	for _, c := range cases {
		got, err := ParseFormatSpec(c.in)
		if err != nil {
			t.Errorf("ParseFormatSpec(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseFormatSpec(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseFormatSpec_Rejects(t *testing.T) {
	for _, in := range []string{"1080p,720p", "a0", "!", ",,"} {
		if _, err := ParseFormatSpec(in); !errors.Is(err, ErrInvalidFlag) {
			t.Errorf("ParseFormatSpec(%q): want ErrInvalidFlag, got %v", in, err)
		}
	}
}

func specListing() *FormatListing {
	return &FormatListing{
		Episode: EpisodeKey{Series: "38290", Season: 1, Episode: 1},
		Video: []VideoQualityInfo{
			{Height: 1080, Codec: "h264", Quality: "1080p-h264"},
			{Height: 720, Codec: "h264", Quality: "720p-h264"},
		},
		Audio: []AudioTrackInfo{
			{Index: 0, Name: "01. Многоголосый. StudioBand (RUS)", Language: "rus"},
			{Index: 1, Name: "02. AniLibria (RUS)", Language: "rus"},
			{Index: 2, Name: "03. Оригинал (JPN)", Language: "jpn"},
		},
		Subtitles: []SubtitleTrackInfo{
			{Index: 0, Name: "RUS #01", Language: "rus"},
			{Index: 1, Name: "ENG #02", Language: "eng"},
		},
	}
}

// A typed id resolves to the same keyword the picker would derive, so the
// choice survives episodes whose track order differs.
func TestFormatSpec_Resolve_TypedIds(t *testing.T) {
	spec, _ := ParseFormatSpec("720p-h264,a2,s2")
	sel, err := spec.Resolve(specListing())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sel.HasQuality || sel.Quality != "720p-h264" {
		t.Errorf("quality = %q (%v)", sel.Quality, sel.HasQuality)
	}
	if !sel.HasAudio || strings.Join(sel.Audio.Include, ",") != "AniLibria" {
		t.Errorf("audio include = %v", sel.Audio.Include)
	}
	if !sel.HasSubtitles || strings.Join(sel.Subtitles.Include, ",") != "eng" {
		t.Errorf("subtitle include = %v", sel.Subtitles.Include)
	}
	// Selecting with the resolved preferences keeps exactly the typed tracks.
	if got := SelectAudio(specListing().Audio, sel.Audio); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("SelectAudio = %v, want [1]", got)
	}
}

// A free pattern is universal: it keeps every audio and subtitle track it
// matches, and only touches the kinds where it matches something.
func TestFormatSpec_Resolve_PatternIsUniversal(t *testing.T) {
	spec, _ := ParseFormatSpec("rus")
	sel, err := spec.Resolve(specListing())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := SelectAudio(specListing().Audio, sel.Audio); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("audio kept %v, want both Russian dubs", got)
	}
	if got := SelectSubtitles(specListing().Subtitles, sel.Subtitles, false); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("subtitles kept %v, want the Russian one", got)
	}

	// Matches audio only: subtitles are left to the run's own preference.
	spec, _ = ParseFormatSpec("studioband")
	sel, err = spec.Resolve(specListing())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !sel.HasAudio || sel.HasSubtitles {
		t.Errorf("studioband: HasAudio=%v HasSubtitles=%v, want true/false", sel.HasAudio, sel.HasSubtitles)
	}
}

func TestFormatSpec_Resolve_ExcludeOnly(t *testing.T) {
	spec, _ := ParseFormatSpec("!jpn")
	sel, err := spec.Resolve(specListing())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := SelectAudio(specListing().Audio, sel.Audio); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("audio kept %v, want everything but the original", got)
	}
}

// A token copied from a listing that matches nothing is a typo, not a wish.
func TestFormatSpec_Resolve_Misses(t *testing.T) {
	for _, in := range []string{"a4", "s3", "klingon"} {
		spec, err := ParseFormatSpec(in)
		if err != nil {
			t.Fatalf("parse %q: %v", in, err)
		}
		if _, err := spec.Resolve(specListing()); !errors.Is(err, ErrInvalidFlag) {
			t.Errorf("%q: want ErrInvalidFlag, got %v", in, err)
		}
	}
}
