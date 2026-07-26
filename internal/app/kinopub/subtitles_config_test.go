// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func TestParseSubtitlePreference(t *testing.T) {
	cases := []struct {
		in      string
		include []string
		exclude []string
	}{
		{"", nil, nil},
		{"all", nil, nil},
		{"rus", []string{"rus"}, nil},
		{"!eng", nil, []string{"eng"}},
		{"-eng", nil, []string{"eng"}}, // '-' is an alias for '!'
		{"rus,!eng", []string{"rus"}, []string{"eng"}},
		{" rus , !eng ", []string{"rus"}, []string{"eng"}}, // surrounding space
	}
	for _, c := range cases {
		pref, err := ParseSubtitlePreference(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(pref.Include, c.include) {
			t.Errorf("%q: Include want %v, got %v", c.in, c.include, pref.Include)
		}
		if !reflect.DeepEqual(pref.Exclude, c.exclude) {
			t.Errorf("%q: Exclude want %v, got %v", c.in, c.exclude, pref.Exclude)
		}
	}
}

func TestParseSubtitlePreference_EmptyPatternIsAnError(t *testing.T) {
	if _, err := ParseSubtitlePreference("rus,!"); !errors.Is(err, domain.ErrInvalidFlag) {
		t.Errorf("want ErrInvalidFlag, got %v", err)
	}
}

// --subs and --audio must not drift apart: they document one shared syntax.
func TestSubtitleAndAudioSelectorsAgree(t *testing.T) {
	const sel = "rus,!eng,forced"
	a, err := ParseAudioPreference(sel)
	if err != nil {
		t.Fatalf("audio: %v", err)
	}
	s, err := ParseSubtitlePreference(sel)
	if err != nil {
		t.Fatalf("subs: %v", err)
	}
	if !reflect.DeepEqual(a.Include, s.Include) || !reflect.DeepEqual(a.Exclude, s.Exclude) {
		t.Errorf("selectors diverged:\n audio %v / %v\n subs  %v / %v",
			a.Include, a.Exclude, s.Include, s.Exclude)
	}
}

// --subs-only writes .srt files beside the episode; there is no container to
// mux into, so external output is implied rather than something to remember.
func TestApplyDefaults_SubtitlesOnlyImpliesExternal(t *testing.T) {
	cfg := domain.RunConfig{SubtitlesOnly: true}
	ApplyDefaults(&cfg)
	if !cfg.SubsExternal {
		t.Error("SubtitlesOnly must imply SubsExternal")
	}
}

func TestApplyDefaults_SubsMenuTimeout(t *testing.T) {
	cfg := domain.RunConfig{SubsMenu: true}
	ApplyDefaults(&cfg)
	if cfg.SubsMenuTimeout != 90*time.Second {
		t.Errorf("want a 90s default, got %v", cfg.SubsMenuTimeout)
	}

	// An explicit timeout must survive.
	cfg = domain.RunConfig{SubsMenu: true, SubsMenuTimeout: 5 * time.Second}
	ApplyDefaults(&cfg)
	if cfg.SubsMenuTimeout != 5*time.Second {
		t.Errorf("explicit timeout overwritten: %v", cfg.SubsMenuTimeout)
	}

	// Without the menu there is nothing to time out.
	cfg = domain.RunConfig{}
	ApplyDefaults(&cfg)
	if cfg.SubsMenuTimeout != 0 {
		t.Errorf("want no timeout without the menu, got %v", cfg.SubsMenuTimeout)
	}
}

func TestApplyDefaults_VideoMenuTimeout(t *testing.T) {
	cfg := domain.RunConfig{VideoMenu: true}
	ApplyDefaults(&cfg)
	if cfg.VideoMenuTimeout != 90*time.Second {
		t.Errorf("want a 90s default, got %v", cfg.VideoMenuTimeout)
	}

	cfg = domain.RunConfig{}
	ApplyDefaults(&cfg)
	if cfg.VideoMenuTimeout != 0 {
		t.Errorf("want no timeout without the menu, got %v", cfg.VideoMenuTimeout)
	}
}

// All three pickers time out on the same schedule; a run left unattended must
// not stall on one of them longer than another.
func TestApplyDefaults_AllMenuTimeoutsAgree(t *testing.T) {
	cfg := domain.RunConfig{VideoMenu: true, AudioMenu: true, SubsMenu: true}
	ApplyDefaults(&cfg)
	if cfg.VideoMenuTimeout != cfg.AudioMenuTimeout || cfg.AudioMenuTimeout != cfg.SubsMenuTimeout {
		t.Errorf("timeouts diverged: video=%v audio=%v subs=%v",
			cfg.VideoMenuTimeout, cfg.AudioMenuTimeout, cfg.SubsMenuTimeout)
	}
}

// Both flags force the RSS pipeline, where --subs-only cannot work: rejecting
// them up front beats downloading full episodes the user did not ask for.
func TestValidateConfig_SubtitlesOnlyConflicts(t *testing.T) {
	base := func() domain.RunConfig {
		cfg := domain.RunConfig{SubtitlesOnly: true, InputURL: "https://kino.watch/item/view/1"}
		ApplyDefaults(&cfg)
		return cfg
	}

	t.Run("feed-file", func(t *testing.T) {
		cfg := base()
		cfg.FeedFile = "./feed.xml"
		if err := ValidateConfig(&cfg); !errors.Is(err, domain.ErrInvalidFlag) {
			t.Errorf("want ErrInvalidFlag, got %v", err)
		}
	})

	t.Run("no-chunked", func(t *testing.T) {
		cfg := base()
		cfg.NoChunked = true
		if err := ValidateConfig(&cfg); !errors.Is(err, domain.ErrInvalidFlag) {
			t.Errorf("want ErrInvalidFlag, got %v", err)
		}
	})

	t.Run("plain page link is accepted", func(t *testing.T) {
		cfg := base()
		if err := ValidateConfig(&cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
