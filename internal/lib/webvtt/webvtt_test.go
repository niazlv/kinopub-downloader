// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package webvtt

import (
	"io"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) []Cue {
	t.Helper()
	cues, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cues
}

func TestParse_HeaderAndCues(t *testing.T) {
	const in = "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\n" +
		"00:00:01.000 --> 00:00:04.000\nПривет\n\n" +
		"00:00:05.500 --> 00:00:08.250\nМир\n"

	cues := mustParse(t, in)
	if len(cues) != 2 {
		t.Fatalf("want 2 cues, got %d", len(cues))
	}
	if cues[0].Start != time.Second || cues[0].End != 4*time.Second {
		t.Errorf("cue 0 timing: %v → %v", cues[0].Start, cues[0].End)
	}
	if cues[0].Text() != "Привет" {
		t.Errorf("cue 0 text: %q", cues[0].Text())
	}
	if want := 5500 * time.Millisecond; cues[1].Start != want {
		t.Errorf("cue 1 start: want %v, got %v", want, cues[1].Start)
	}
}

// The header carries no text; it must never become a cue.
func TestParse_HeaderIsNotACue(t *testing.T) {
	cues := mustParse(t, "WEBVTT\n\n")
	if len(cues) != 0 {
		t.Fatalf("want no cues, got %d: %+v", len(cues), cues)
	}
}

func TestParse_SkipsNoteStyleRegion(t *testing.T) {
	const in = "WEBVTT\n\n" +
		"NOTE this is a comment\nspanning two lines\n\n" +
		"STYLE\n::cue { color: white }\n\n" +
		"REGION\nid:r1\n\n" +
		"00:00:01.000 --> 00:00:02.000\nOnly me\n"

	cues := mustParse(t, in)
	if len(cues) != 1 {
		t.Fatalf("want 1 cue, got %d: %+v", len(cues), cues)
	}
	if cues[0].Text() != "Only me" {
		t.Errorf("got %q", cues[0].Text())
	}
}

func TestParse_CueIdentifierAndSettings(t *testing.T) {
	const in = "WEBVTT\n\n" +
		"cue-42\n00:00:01.000 --> 00:00:02.000 align:start position:10%\nText\n"

	cues := mustParse(t, in)
	if len(cues) != 1 {
		t.Fatalf("want 1 cue, got %d", len(cues))
	}
	if cues[0].Settings != "align:start position:10%" {
		t.Errorf("settings: %q", cues[0].Settings)
	}
	if cues[0].Text() != "Text" {
		t.Errorf("text: %q", cues[0].Text())
	}
}

func TestParse_MultiLineCue(t *testing.T) {
	const in = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst\nsecond\n"
	cues := mustParse(t, in)
	if len(cues) != 1 {
		t.Fatalf("want 1 cue, got %d", len(cues))
	}
	if cues[0].Text() != "first\nsecond" {
		t.Errorf("got %q", cues[0].Text())
	}
}

// A truncated or corrupt timing line must fail loudly: silently dropping it
// would lose subtitles without any signal.
func TestParse_MalformedTimingIsAnError(t *testing.T) {
	const in = "WEBVTT\n\n00:00:0X.000 --> 00:00:02.000\nText\n"
	if _, err := Parse(strings.NewReader(in)); err == nil {
		t.Fatal("want an error for a malformed timestamp, got nil")
	}
}

func TestParse_CRLFAndBOM(t *testing.T) {
	const in = "\uFEFFWEBVTT\r\n\r\n00:00:01.000 --> 00:00:02.000\r\nText\r\n"
	cues := mustParse(t, in)
	if len(cues) != 1 {
		t.Fatalf("want 1 cue, got %d", len(cues))
	}
	if cues[0].Text() != "Text" {
		t.Errorf("got %q", cues[0].Text())
	}
}

func TestParseTimestamp(t *testing.T) {
	cases := map[string]time.Duration{
		"00:00:01.000": time.Second,
		"01:02:03.500": time.Hour + 2*time.Minute + 3*time.Second + 500*time.Millisecond,
		"02:03.250":    2*time.Minute + 3*time.Second + 250*time.Millisecond, // abbreviated
		"00:00:01,000": time.Second,                                          // SubRip comma
	}
	for in, want := range cases {
		got, err := ParseTimestamp(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: want %v, got %v", in, want, got)
		}
	}

	for _, bad := range []string{"", "abc", "00:xx:01.000", "-00:00:01.000"} {
		if _, err := ParseTimestamp(bad); err == nil {
			t.Errorf("%q: want an error, got nil", bad)
		}
	}
}

// The central reason this package exists: byte-concatenating HLS segments would
// leave "WEBVTT" headers mid-file.
func TestMerge_SingleHeaderAndOrdering(t *testing.T) {
	seg1 := "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\n" +
		"00:00:01.000 --> 00:00:02.000\nfirst\n"
	seg2 := "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\n" +
		"00:00:03.000 --> 00:00:04.000\nsecond\n"

	var out strings.Builder
	if err := Merge(&out, []io.Reader{strings.NewReader(seg1), strings.NewReader(seg2)}); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	got := out.String()
	if n := strings.Count(got, "WEBVTT"); n != 1 {
		t.Errorf("want exactly one WEBVTT header, got %d\n%s", n, got)
	}
	if strings.Contains(got, "X-TIMESTAMP-MAP") {
		t.Errorf("segment header metadata leaked into the merged file:\n%s", got)
	}
	if i, j := strings.Index(got, "first"), strings.Index(got, "second"); i == -1 || j == -1 || i > j {
		t.Errorf("cues out of order:\n%s", got)
	}
}

// A cue overlapping a segment boundary appears in both segments; rendering it
// twice would show a duplicate subtitle.
func TestMerge_DeduplicatesBoundaryCues(t *testing.T) {
	const shared = "00:00:09.000 --> 00:00:11.000\nboundary\n"
	seg1 := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nfirst\n\n" + shared
	seg2 := "WEBVTT\n\n" + shared + "\n00:00:12.000 --> 00:00:13.000\nlast\n"

	var out strings.Builder
	if err := Merge(&out, []io.Reader{strings.NewReader(seg1), strings.NewReader(seg2)}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if n := strings.Count(out.String(), "boundary"); n != 1 {
		t.Errorf("want the boundary cue once, got %d\n%s", n, out.String())
	}
}

// Segments arriving out of order must still produce a monotonic document.
func TestMerge_SortsByStartTime(t *testing.T) {
	late := "WEBVTT\n\n00:00:10.000 --> 00:00:11.000\nlate\n"
	early := "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nearly\n"

	var out strings.Builder
	if err := Merge(&out, []io.Reader{strings.NewReader(late), strings.NewReader(early)}); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if i, j := strings.Index(out.String(), "early"), strings.Index(out.String(), "late"); i > j {
		t.Errorf("not sorted by start time:\n%s", out.String())
	}
}

func TestMerge_NoSegments(t *testing.T) {
	var out strings.Builder
	if err := Merge(&out, nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := out.String(); got != "WEBVTT\n\n" {
		t.Errorf("want a bare header, got %q", got)
	}
}

func TestToSRT(t *testing.T) {
	const in = "WEBVTT\n\n" +
		"00:00:01.000 --> 00:00:04.000 align:start\nПривет\n\n" +
		"00:00:05.500 --> 00:00:08.250\nдве\nстроки\n"

	var out strings.Builder
	if err := ToSRT(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("ToSRT: %v", err)
	}

	want := "1\n00:00:01,000 --> 00:00:04,000\nПривет\n\n" +
		"2\n00:00:05,500 --> 00:00:08,250\nдве\nстроки\n\n"
	if out.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", out.String(), want)
	}
}

// SubRip has no cue settings; carrying them through would corrupt the file.
func TestToSRT_DropsCueSettings(t *testing.T) {
	const in = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000 align:start position:10%\nText\n"
	var out strings.Builder
	if err := ToSRT(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("ToSRT: %v", err)
	}
	if strings.Contains(out.String(), "align:start") {
		t.Errorf("cue settings leaked into SubRip:\n%s", out.String())
	}
}

// SubRip numbering must be sequential from 1 even when empty cues are skipped.
func TestToSRT_RenumbersSequentially(t *testing.T) {
	const in = "WEBVTT\n\n" +
		"9\n00:00:01.000 --> 00:00:02.000\na\n\n" +
		"17\n00:00:03.000 --> 00:00:04.000\nb\n"

	var out strings.Builder
	if err := ToSRT(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("ToSRT: %v", err)
	}
	if !strings.HasPrefix(out.String(), "1\n") || !strings.Contains(out.String(), "\n2\n") {
		t.Errorf("want renumbering from 1:\n%s", out.String())
	}
}

func TestToSRT_AlwaysEmitsHours(t *testing.T) {
	// The abbreviated MM:SS.mmm form must expand: SubRip requires HH:MM:SS,mmm.
	const in = "WEBVTT\n\n02:03.250 --> 02:04.000\nText\n"
	var out strings.Builder
	if err := ToSRT(&out, strings.NewReader(in)); err != nil {
		t.Fatalf("ToSRT: %v", err)
	}
	if !strings.Contains(out.String(), "00:02:03,250 --> 00:02:04,000") {
		t.Errorf("got:\n%s", out.String())
	}
}

// A round trip must not drift timings.
func TestWriteVTT_RoundTrip(t *testing.T) {
	const in = "WEBVTT\n\n00:01:02.345 --> 00:01:04.500\nText\n"
	cues := mustParse(t, in)

	var out strings.Builder
	if err := WriteVTT(&out, cues); err != nil {
		t.Fatalf("WriteVTT: %v", err)
	}
	again := mustParse(t, out.String())
	if len(again) != 1 || again[0].Start != cues[0].Start || again[0].End != cues[0].End {
		t.Errorf("round trip drifted: %+v vs %+v", cues, again)
	}
}
