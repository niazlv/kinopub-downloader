// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package audiomenu

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func videoQualities() []domain.VideoQualityInfo {
	return []domain.VideoQualityInfo{
		{Index: 0, Height: 1080, Codec: "h265", BitrateKbps: 2500, Quality: "1080p-h265"},
		{Index: 1, Height: 1080, Codec: "h264", BitrateKbps: 4200, Quality: "1080p-h264"},
		{Index: 2, Height: 720, Codec: "h264", BitrateKbps: 1800, Quality: "720p-h264"},
	}
}

func chooseVideo(t *testing.T, input string) (int, string) {
	t.Helper()
	var out strings.Builder
	c := New(strings.NewReader(input), &out, true)
	got, err := c.ChooseVideo(videoQualities(), time.Second)
	if err != nil {
		t.Fatalf("ChooseVideo: %v", err)
	}
	return got, out.String()
}

func TestChooseVideo_PicksOne(t *testing.T) {
	got, _ := chooseVideo(t, "2\n")
	if got != 1 {
		t.Errorf("want index 1 for input \"2\", got %d", got)
	}
}

func TestChooseVideo_FirstAndLast(t *testing.T) {
	if got, _ := chooseVideo(t, "1\n"); got != 0 {
		t.Errorf("want 0, got %d", got)
	}
	if got, _ := chooseVideo(t, "3\n"); got != 2 {
		t.Errorf("want 2, got %d", got)
	}
}

// -1 means "keep the automatic quality"; every non-answer must reach it.
func TestChooseVideo_DefaultsToAutomatic(t *testing.T) {
	for _, in := range []string{"\n", "auto\n", "automatic\n", "optimal\n", "  \n"} {
		if got, _ := chooseVideo(t, in); got != -1 {
			t.Errorf("input %q: want -1, got %d", in, got)
		}
	}
}

// A run muxes one video stream, so a range or a list is not a valid answer —
// silently taking its first entry would download something unasked for.
func TestChooseVideo_RejectsMultiSelection(t *testing.T) {
	for _, in := range []string{"1,2\n", "1-2\n", "all\n"} {
		if got, _ := chooseVideo(t, in); got != -1 {
			t.Errorf("input %q: want -1 (automatic), got %d", in, got)
		}
	}
}

func TestChooseVideo_RejectsOutOfRange(t *testing.T) {
	for _, in := range []string{"0\n", "4\n", "-1\n", "abc\n"} {
		if got, _ := chooseVideo(t, in); got != -1 {
			t.Errorf("input %q: want -1, got %d", in, got)
		}
	}
}

// Nothing may prompt when there is no terminal — the run must proceed.
func TestChooseVideo_NonInteractive(t *testing.T) {
	var out strings.Builder
	c := New(strings.NewReader("2\n"), &out, false)
	got, err := c.ChooseVideo(videoQualities(), time.Second)
	if err != nil {
		t.Fatalf("ChooseVideo: %v", err)
	}
	if got != -1 {
		t.Errorf("want -1, got %d", got)
	}
	if out.String() != "" {
		t.Errorf("non-interactive mode must print nothing, got %q", out.String())
	}
}

// One option is not a choice.
func TestChooseVideo_SingleQualitySkipsMenu(t *testing.T) {
	var out strings.Builder
	c := New(strings.NewReader("1\n"), &out, true)
	got, err := c.ChooseVideo(videoQualities()[:1], time.Second)
	if err != nil {
		t.Fatalf("ChooseVideo: %v", err)
	}
	if got != -1 {
		t.Errorf("want -1, got %d", got)
	}
	if out.String() != "" {
		t.Errorf("must not prompt for a single option, got %q", out.String())
	}
}

func TestChooseVideo_RendersOptions(t *testing.T) {
	_, out := chooseVideo(t, "1\n")
	for _, want := range []string{"1080p/h265 (2500 kbps)", "1080p/h264 (4200 kbps)", "720p/h264 (1800 kbps)"} {
		if !strings.Contains(out, want) {
			t.Errorf("option %q missing from menu:\n%s", want, out)
		}
	}
}

// Waiting forever would strand an unattended run.
func TestChooseVideo_TimesOut(t *testing.T) {
	var out strings.Builder
	c := New(neverReader{}, &out, true)

	done := make(chan int, 1)
	go func() {
		got, _ := c.ChooseVideo(videoQualities(), 50*time.Millisecond)
		done <- got
	}()

	select {
	case got := <-done:
		if got != -1 {
			t.Errorf("want -1 on timeout, got %d", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ChooseVideo did not return after its timeout")
	}
}

// neverReader blocks until the reader is closed, which never happens here.
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) {
	select {}
}

var _ io.Reader = neverReader{}
