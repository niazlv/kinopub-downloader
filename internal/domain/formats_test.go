// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "testing"

// The selectors printed by --list-formats must be the very patterns the
// interactive pickers would derive, so a copied row selects the same track.
func TestAudioSelector(t *testing.T) {
	cases := []struct {
		name, lang, want string
	}{
		{"01. Многоголосый. StudioBand (RUS)", "rus", "studioband"},
		{"02. Оригинал (JPN)", "jpn", "jpn"}, // nothing distinctive: the language
		{"AniLibria", "", "anilibria"},       // no language anywhere: the studio
		{"03. Дубляж", "", "03. дубляж"},     // nothing at all: the name verbatim
	}
	for _, c := range cases {
		got := AudioSelector(AudioTrackInfo{Name: c.name, Language: c.lang})
		if got != c.want {
			t.Errorf("AudioSelector(%q, %q) = %q, want %q", c.name, c.lang, got, c.want)
		}
	}
}

func TestSubtitleSelector(t *testing.T) {
	cases := []struct {
		name, lang, want string
	}{
		{"Русские", "rus", "rus"},
		{"Русские (RUS)", "", "rus"},       // language parsed from the name's tail
		{"Russian (forced)", "", "forced"}, // "russian" is a stopword: the distinctive token remains
		{"Forced", "", "forced"},           // no language: distinctive token
	}
	for _, c := range cases {
		got := SubtitleSelector(SubtitleTrackInfo{Name: c.name, Language: c.lang})
		if got != c.want {
			t.Errorf("SubtitleSelector(%q, %q) = %q, want %q", c.name, c.lang, got, c.want)
		}
	}
}
