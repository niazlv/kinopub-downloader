// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "testing"

func TestEpisodeRefFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want EpisodeRef
	}{
		// Season + episode.
		{"https://kino.watch/item/view/38290/s1e1", EpisodeRef{Season: 1, Episode: 1}},
		{"https://kino.pub/item/view/38290/s01e01", EpisodeRef{Season: 1, Episode: 1}},
		{"https://kino.watch/item/view/38290/S2E13", EpisodeRef{Season: 2, Episode: 13}},
		{"https://kino.watch/item/view/38290/s1e1/", EpisodeRef{Season: 1, Episode: 1}},

		// Whole season.
		{"https://kino.watch/item/view/38290/s3", EpisodeRef{Season: 3}},
		{"https://kino.watch/item/view/38290/S03", EpisodeRef{Season: 3}},

		// No reference at all.
		{"https://kino.watch/item/view/38290", EpisodeRef{}},
		{"https://kino.watch/item/view/38290/", EpisodeRef{}},

		// A podcast feed's trailing segment is an access token, never an
		// episode reference — mistaking one would silently narrow the run.
		{"https://kino.watch/podcast/get/38290/TOKEN", EpisodeRef{}},
		{"https://kino.watch/podcast/get/38290/s1e1", EpisodeRef{}},

		// Malformed or unrelated suffixes.
		{"https://kino.watch/item/view/38290/extras", EpisodeRef{}},
		{"https://kino.watch/item/view/38290/e5", EpisodeRef{}},   // episode without season
		{"https://kino.watch/item/view/38290/s1e", EpisodeRef{}},  // dangling "e"
		{"https://kino.watch/item/view/38290/1x1", EpisodeRef{}},  // a different convention
		{"https://kino.watch/item/view/38290/s0", EpisodeRef{}},   // season 0 is not downloadable
		{"https://kino.watch/item/view/38290/s0e1", EpisodeRef{}}, // ditto
		{"", EpisodeRef{}},
		{"://nonsense", EpisodeRef{}},
	}

	for _, c := range cases {
		if got := EpisodeRefFromURL(c.url); got != c.want {
			t.Errorf("%q: want %+v, got %+v", c.url, c.want, got)
		}
	}
}

// A run keyed by series id must not be disturbed by the suffix.
func TestEpisodeRefFromURL_SeriesIDStillParses(t *testing.T) {
	const u = "https://kino.watch/item/view/38290/s1e1"
	if got := SeriesIDFromURL(u); got != "38290" {
		t.Errorf("series id: want 38290, got %q", got)
	}
}

func TestEpisodeRef_IsZero(t *testing.T) {
	if !(EpisodeRef{}).IsZero() {
		t.Error("zero value must report IsZero")
	}
	if (EpisodeRef{Season: 1}).IsZero() {
		t.Error("a season-only reference is not zero")
	}
	if (EpisodeRef{Season: 1, Episode: 2}).IsZero() {
		t.Error("a full reference is not zero")
	}
}

// A very long digit run must not be read as a season number.
func TestEpisodeRefFromURL_BoundedDigits(t *testing.T) {
	if got := EpisodeRefFromURL("https://kino.watch/item/view/1/s12345"); !got.IsZero() {
		t.Errorf("want zero for an over-long season, got %+v", got)
	}
}
