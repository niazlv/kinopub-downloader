// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package desktopnotify

import (
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func TestEscapeAppleScript(t *testing.T) {
	// A title containing a quote must not break out of the AppleScript literal.
	in := `Show "Name" \ path`
	got := escapeAppleScript(in)
	if strings.Contains(strings.ReplaceAll(got, `\"`, ""), `"`) {
		t.Errorf("unescaped quote remains in %q", got)
	}
	if !strings.Contains(got, `\\`) {
		t.Errorf("backslash not escaped in %q", got)
	}
}

func TestPlural(t *testing.T) {
	if got := plural(1, 1); got != "1 episode" {
		t.Errorf("plural(1,1) = %q", got)
	}
	if got := plural(2, 5); got != "2/5 episodes" {
		t.Errorf("plural(2,5) = %q", got)
	}
}

// A recording reporter proves Notifier forwards every call to inner even while
// adding notifications on top.
type recorder struct {
	starts, epStarted, epDone, stops int
}

func (r *recorder) Start(domain.SeriesPlan)                               { r.starts++ }
func (r *recorder) EpisodeStarted(domain.EpisodeKey)                      { r.epStarted++ }
func (r *recorder) TrackProgress(domain.EpisodeKey, domain.TrackRef, int) {}
func (r *recorder) EpisodeCompleted(domain.EpisodeKey)                    { r.epDone++ }
func (r *recorder) EpisodeFailed(domain.EpisodeKey, error)                {}
func (r *recorder) Stop()                                                 { r.stops++ }

func TestNotifierForwardsToInner(t *testing.T) {
	rec := &recorder{}
	// Construct the Notifier directly with a no-op backend so the test does not
	// depend on osascript/notify-send being present.
	n := &Notifier{inner: rec, backend: backend{command: func(_, _ string) []string { return nil }}}

	n.Start(domain.SeriesPlan{Title: "T", Total: 3})
	n.EpisodeStarted(domain.EpisodeKey{})
	n.EpisodeCompleted(domain.EpisodeKey{})
	n.Stop()

	if rec.starts != 1 || rec.epStarted != 1 || rec.epDone != 1 || rec.stops != 1 {
		t.Errorf("inner not forwarded correctly: %+v", rec)
	}
}
