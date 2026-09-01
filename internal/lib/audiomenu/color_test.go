// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package audiomenu

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

func colorTracks() []domain.TrackInfo {
	return []domain.TrackInfo{
		{Name: "AniLibria", Language: "rus"},
		{Name: "Original", Language: "jpn"},
	}
}

func TestChooser_PlainByDefault(t *testing.T) {
	var out bytes.Buffer
	c := New(strings.NewReader("1\n"), &out, true)
	if _, err := c.ChooseAudio(toAudio(colorTracks()), time.Second); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033[") {
		t.Errorf("a chooser built without WithColor must print no escapes, got %q", out.String())
	}
}

func TestChooser_WithColor(t *testing.T) {
	var out bytes.Buffer
	c := New(strings.NewReader("1\n"), &out, true, WithColor(true))
	if _, err := c.ChooseAudio(toAudio(colorTracks()), time.Second); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, termx.Bold) || !strings.Contains(got, termx.Cyan) {
		t.Errorf("a colored menu should highlight its heading and indices, got %q", got)
	}
	// The track labels themselves stay readable, escapes or not.
	for _, want := range []string{"AniLibria", "Original"} {
		if !strings.Contains(got, want) {
			t.Errorf("menu should list %q, got %q", want, got)
		}
	}
}

// toAudio adapts the shared TrackInfo list to the audio-specific type.
func toAudio(tracks []domain.TrackInfo) []domain.AudioTrackInfo {
	out := make([]domain.AudioTrackInfo, len(tracks))
	for i, t := range tracks {
		out[i] = domain.AudioTrackInfo(t)
	}
	return out
}
