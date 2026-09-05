// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

func sampleListing() *domain.FormatListing {
	return &domain.FormatListing{
		Episode:  domain.EpisodeKey{Series: "119614", Season: 2, Episode: 10},
		Title:    "Storm",
		Duration: 1420 * time.Second,
		Matching: 1,
		Video: []domain.VideoQualityInfo{
			{Height: 1080, Width: 1920, Codec: "h264", BitrateKbps: 3805, FPS: 24, Quality: "1080p-h264"},
			{Height: 720, Width: 1280, Codec: "h264", BitrateKbps: 1933, FPS: 24, Quality: "720p-h264"},
			{Height: 406, Width: 720, Codec: "h264", BitrateKbps: 1060, FPS: 24, Quality: "406p-h264"},
		},
		Audio: []domain.AudioTrackInfo{
			{Name: "01. Многоголосый. StudioBand (RUS)", Language: "rus"},
			{Name: "02. Оригинал (JPN)", Language: "jpn"},
		},
		AudioStats: []domain.TrackStats{
			{Codec: "mp4a.40.2", Channels: 2, BitrateKbps: 128, SizeBytes: 22720000},
			{}, // not sampled: its cells stay blank
		},
		Subtitles:     []domain.SubtitleTrackInfo{{Name: "RUS #01", Language: "rus"}},
		SubtitleStats: []domain.TrackStats{{BitrateKbps: 1, SizeBytes: 90000}},
	}
}

func TestRenderFormats(t *testing.T) {
	var buf bytes.Buffer
	renderFormats(&buf, termx.NewStyler(false), sampleListing())
	out := buf.String()

	want := []string{
		"S02E10  Storm  (23:40)",
		"ID          KIND   RESOLUTION  FPS  CODEC          BITRATE    ~SIZE     LANG  NAME                                PATTERN",
		"1080p-h264  video  1920x1080   24   h264           3805 kbps  ~644 MiB",
		"406p-h264   video  720x406     24   h264           1060 kbps  ~179 MiB",
		`Example: kinopub -f "1080p-h264+a1+s1" <url>`,
	}
	for _, line := range want {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("missing line %q in:\n%s", line, out)
		}
	}
	// Track rows: sampled stats where known, blanks where not, patterns last.
	for _, fragments := range [][]string{
		{"a1          audio", "mp4a.40.2 2ch", "128 kbps", "~22 MiB", "rus   01. Многоголосый. StudioBand (RUS)  studioband"},
		{"a2          audio", "jpn   02. Оригинал (JPN)                  jpn"},
		{"s1          subs", "1 kbps", "~88 KiB", "rus   RUS #01                             rus"},
	} {
		line := lineContaining(out, fragments[0])
		for _, f := range fragments {
			if !strings.Contains(line, f) {
				t.Errorf("row %q lacks %q:\n%s", fragments[0], f, out)
			}
		}
	}
	if a2 := lineContaining(out, "a2          audio"); strings.Contains(a2, "kbps") || strings.Contains(a2, "MiB") {
		t.Errorf("an unsampled track must show no stats: %q", a2)
	}
	if strings.Contains(out, "matching episodes") {
		t.Errorf("a single-episode listing must not mention other episodes:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("trailing spaces in %q", line)
		}
	}
}

// lineContaining returns the first line of out that contains s, or "".
func lineContaining(out, s string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, s) {
			return line
		}
	}
	return ""
}

func TestRenderFormats_MentionsOtherMatchingEpisodes(t *testing.T) {
	l := sampleListing()
	l.Matching = 8
	var buf bytes.Buffer
	renderFormats(&buf, termx.NewStyler(false), l)
	if !strings.Contains(buf.String(), "8 matching episodes; the listing is for the first") {
		t.Errorf("want the multi-episode note, got:\n%s", buf.String())
	}
}

// The example must be a valid -f for the listing it follows.
func TestExampleCommand_IsAValidSpec(t *testing.T) {
	l := sampleListing()
	example := exampleCommand(l)
	spec, err := domain.ParseFormatSpec(strings.Trim(strings.TrimSuffix(strings.TrimPrefix(example, "kinopub -f "), " <url>"), `"`))
	if err != nil {
		t.Fatalf("example %q does not parse: %v", example, err)
	}
	if _, err := spec.Resolve(l); err != nil {
		t.Fatalf("example %q does not resolve: %v", example, err)
	}
}

func TestEstimateSize(t *testing.T) {
	cases := []struct {
		kbps int
		d    time.Duration
		want string
	}{
		{3805, 1420 * time.Second, "~644 MiB"},
		{8000, 2 * time.Hour, "~6.7 GiB"},
		{0, time.Hour, ""},
		{3805, 0, ""},
	}
	for _, c := range cases {
		if got := estimateSize(c.kbps, c.d); got != c.want {
			t.Errorf("estimateSize(%d, %s) = %q, want %q", c.kbps, c.d, got, c.want)
		}
	}
}

func TestFormatClock(t *testing.T) {
	cases := map[time.Duration]string{
		1420 * time.Second:             "23:40",
		5 * time.Second:                "0:05",
		time.Hour + 2*time.Minute:      "1:02:00",
		90*time.Minute + 3*time.Second: "1:30:03",
	}
	for d, want := range cases {
		if got := formatClock(d); got != want {
			t.Errorf("formatClock(%s) = %q, want %q", d, got, want)
		}
	}
}

// A feed listing offers no track ids or patterns: the file comes whole.
func TestRenderFormats_Feed(t *testing.T) {
	l := &domain.FormatListing{
		Episode:  domain.EpisodeKey{Series: "123", Season: 1, Episode: 1},
		Feed:     true,
		Matching: 1,
		Video:    []domain.VideoQualityInfo{{Height: 1080, Width: 1920, BitrateKbps: 3805, Quality: "1080p"}},
		Audio:    []domain.AudioTrackInfo{{Name: "StudioBand", Language: "rus"}},
	}
	var buf bytes.Buffer
	renderFormats(&buf, termx.NewStyler(false), l)
	out := buf.String()
	for _, want := range []string{"1080p  file", "1920x1080", "audio in file", `Example: kinopub -f "1080p" <url>`, "finished files"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PATTERN") || strings.Contains(out, "a1") {
		t.Errorf("a feed listing must not offer track ids or patterns:\n%s", out)
	}
}
