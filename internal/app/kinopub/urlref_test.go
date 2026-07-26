// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub

import (
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// cfgWithURL builds a config in the state main.go hands to ApplyURLEpisodeRef:
// selections already parsed from the flags, defaulting to "everything".
func cfgWithURL(url string) domain.RunConfig {
	return domain.RunConfig{
		InputURL:   url,
		SeasonSel:  domain.Selection{All: true},
		EpisodeSel: domain.Selection{All: true},
	}
}

func TestApplyURLEpisodeRef_NarrowsToOneEpisode(t *testing.T) {
	cfg := cfgWithURL("https://kino.watch/item/view/38290/s1e1")
	ApplyURLEpisodeRef(&cfg, false, false)

	if !cfg.SeasonSel.Matches(1) || cfg.SeasonSel.Matches(2) {
		t.Errorf("season selection not narrowed to 1: %+v", cfg.SeasonSel)
	}
	if !cfg.EpisodeSel.Matches(1) || cfg.EpisodeSel.Matches(2) {
		t.Errorf("episode selection not narrowed to 1: %+v", cfg.EpisodeSel)
	}
}

// "sN" names a season but no episode, so every episode of it stays selected.
func TestApplyURLEpisodeRef_WholeSeason(t *testing.T) {
	cfg := cfgWithURL("https://kino.watch/item/view/38290/s2")
	ApplyURLEpisodeRef(&cfg, false, false)

	if !cfg.SeasonSel.Matches(2) || cfg.SeasonSel.Matches(1) {
		t.Errorf("season selection not narrowed to 2: %+v", cfg.SeasonSel)
	}
	if !cfg.EpisodeSel.All {
		t.Errorf("episodes must stay unfiltered, got %+v", cfg.EpisodeSel)
	}
}

// An explicit flag is more specific than the link it accompanies.
func TestApplyURLEpisodeRef_ExplicitFlagsWin(t *testing.T) {
	t.Run("seasons", func(t *testing.T) {
		cfg := cfgWithURL("https://kino.watch/item/view/38290/s1e1")
		cfg.SeasonSel = domain.Selection{Values: map[int]bool{3: true}}
		ApplyURLEpisodeRef(&cfg, true, false)

		if !cfg.SeasonSel.Matches(3) || cfg.SeasonSel.Matches(1) {
			t.Errorf("--seasons overridden by the URL: %+v", cfg.SeasonSel)
		}
		// The episode half of the reference still applies.
		if !cfg.EpisodeSel.Matches(1) {
			t.Errorf("episode reference dropped: %+v", cfg.EpisodeSel)
		}
	})

	t.Run("episodes", func(t *testing.T) {
		cfg := cfgWithURL("https://kino.watch/item/view/38290/s1e1")
		cfg.EpisodeSel = domain.Selection{Values: map[int]bool{7: true}}
		ApplyURLEpisodeRef(&cfg, false, true)

		if !cfg.EpisodeSel.Matches(7) || cfg.EpisodeSel.Matches(1) {
			t.Errorf("--episodes overridden by the URL: %+v", cfg.EpisodeSel)
		}
		if !cfg.SeasonSel.Matches(1) {
			t.Errorf("season reference dropped: %+v", cfg.SeasonSel)
		}
	})

	t.Run("both", func(t *testing.T) {
		cfg := cfgWithURL("https://kino.watch/item/view/38290/s1e1")
		cfg.SeasonSel = domain.Selection{Values: map[int]bool{3: true}}
		cfg.EpisodeSel = domain.Selection{Values: map[int]bool{7: true}}
		ApplyURLEpisodeRef(&cfg, true, true)

		if !cfg.SeasonSel.Matches(3) || !cfg.EpisodeSel.Matches(7) {
			t.Errorf("explicit flags disturbed: %+v / %+v", cfg.SeasonSel, cfg.EpisodeSel)
		}
	})
}

// A plain series link must download the whole series, as it always has.
func TestApplyURLEpisodeRef_NoSuffixLeavesConfigAlone(t *testing.T) {
	for _, url := range []string{
		"https://kino.watch/item/view/38290",
		"https://kino.watch/podcast/get/38290/TOKEN",
		"",
	} {
		cfg := cfgWithURL(url)
		ApplyURLEpisodeRef(&cfg, false, false)

		if !cfg.SeasonSel.All || !cfg.EpisodeSel.All {
			t.Errorf("%q narrowed the run: %+v / %+v", url, cfg.SeasonSel, cfg.EpisodeSel)
		}
	}
}

// The suffix must survive the rest of the config pipeline untouched.
func TestApplyURLEpisodeRef_SurvivesApplyDefaults(t *testing.T) {
	cfg := cfgWithURL("https://kino.watch/item/view/38290/s1e1")
	ApplyURLEpisodeRef(&cfg, false, false)
	ApplyDefaults(&cfg)

	if !cfg.SeasonSel.Matches(1) || cfg.SeasonSel.Matches(2) {
		t.Errorf("ApplyDefaults widened the season selection: %+v", cfg.SeasonSel)
	}
	if !cfg.EpisodeSel.Matches(1) || cfg.EpisodeSel.Matches(2) {
		t.Errorf("ApplyDefaults widened the episode selection: %+v", cfg.EpisodeSel)
	}
}
