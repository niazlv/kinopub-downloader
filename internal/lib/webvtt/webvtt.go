// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package webvtt parses WebVTT subtitles, merges the per-segment files an HLS
// subtitle rendition is delivered as, and converts them to SubRip (.srt).
//
// HLS splits a subtitle track into one WebVTT file per segment, each carrying
// its own "WEBVTT" header. Concatenating them byte-for-byte — the way the
// downloader joins MPEG-TS media segments — produces a file with headers in the
// middle that players reject, so subtitle segments are parsed into cues and
// re-serialized instead.
package webvtt

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Cue is a single subtitle entry: when it shows, when it hides, and its text.
type Cue struct {
	Start    time.Duration
	End      time.Duration
	Settings string   // WebVTT cue settings (e.g. "align:start position:10%")
	Lines    []string // text lines, without the trailing blank separator
}

// Text joins the cue's lines with newlines.
func (c Cue) Text() string { return strings.Join(c.Lines, "\n") }

// Parse reads a WebVTT file and returns its cues in file order.
//
// Header blocks (the "WEBVTT" line plus any X-TIMESTAMP-MAP and similar
// metadata), NOTE comments, STYLE and REGION blocks carry no displayable text
// and are skipped. A malformed timing line makes the whole file an error rather
// than silently dropping subtitles.
func Parse(r io.Reader) ([]Cue, error) {
	scanner := bufio.NewScanner(r)
	// Subtitle lines are short, but a pathological file should fail loudly
	// rather than truncate a cue mid-way.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		cues    []Cue
		block   []string
		lineNo  int
		flushed bool
	)

	flush := func() error {
		defer func() { block = nil }()
		if len(block) == 0 {
			return nil
		}
		// The first block of a file is the header ("WEBVTT" plus metadata).
		if !flushed {
			flushed = true
			if strings.HasPrefix(strings.TrimSpace(block[0]), "WEBVTT") {
				return nil
			}
		}
		cue, ok, err := parseBlock(block)
		if err != nil {
			return err
		}
		if ok {
			cues = append(cues, cue)
		}
		return nil
	}

	for scanner.Scan() {
		lineNo++
		line := strings.TrimRight(scanner.Text(), "\r")
		// A BOM on the very first line would hide the WEBVTT signature.
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		block = append(block, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read webvtt: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return cues, nil
}

// parseBlock turns one blank-line-delimited block into a Cue. It reports
// ok=false for blocks that hold no displayable text (NOTE/STYLE/REGION).
func parseBlock(block []string) (Cue, bool, error) {
	switch {
	case strings.HasPrefix(block[0], "NOTE"),
		strings.HasPrefix(block[0], "STYLE"),
		strings.HasPrefix(block[0], "REGION"):
		return Cue{}, false, nil
	}

	// A cue may open with an identifier line; the timing line is the one
	// containing the "-->" arrow.
	timingIdx := -1
	for i, l := range block {
		if strings.Contains(l, "-->") {
			timingIdx = i
			break
		}
	}
	if timingIdx == -1 {
		// No timing: a stray block (often a leftover header). Skip it rather
		// than failing — such blocks appear between HLS segments.
		return Cue{}, false, nil
	}

	start, end, settings, err := parseTiming(block[timingIdx])
	if err != nil {
		return Cue{}, false, err
	}

	lines := block[timingIdx+1:]
	if len(lines) == 0 {
		// A timing line with no text shows nothing; dropping it keeps the
		// output clean and cannot lose information.
		return Cue{}, false, nil
	}
	return Cue{
		Start:    start,
		End:      end,
		Settings: settings,
		Lines:    append([]string(nil), lines...),
	}, true, nil
}

// parseTiming parses "00:00:01.000 --> 00:00:04.000 align:start" into its
// start, end and trailing cue settings.
func parseTiming(line string) (start, end time.Duration, settings string, err error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, "", fmt.Errorf("malformed timing line %q", line)
	}

	start, err = ParseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, "", fmt.Errorf("timing line %q: %w", line, err)
	}

	// Everything after the end timestamp is cue settings.
	rest := strings.Fields(strings.TrimSpace(parts[1]))
	if len(rest) == 0 {
		return 0, 0, "", fmt.Errorf("malformed timing line %q: missing end time", line)
	}
	end, err = ParseTimestamp(rest[0])
	if err != nil {
		return 0, 0, "", fmt.Errorf("timing line %q: %w", line, err)
	}
	return start, end, strings.Join(rest[1:], " "), nil
}

// ParseTimestamp parses a WebVTT timestamp, accepting both the "HH:MM:SS.mmm"
// and the abbreviated "MM:SS.mmm" form. A comma is accepted in place of the
// decimal point so SubRip timestamps parse too.
func ParseTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.Replace(s, ",", ".", 1))
	if s == "" {
		return 0, fmt.Errorf("empty timestamp")
	}
	// Reject a sign up front: "-00:00:01.000" would otherwise parse as
	// hours=0 (strconv.Atoi("-00") is 0) and slip past the negative check
	// below, silently yielding a positive timestamp.
	if s[0] == '-' || s[0] == '+' {
		return 0, fmt.Errorf("signed timestamp %q", s)
	}

	secPart := s
	var hours, minutes int
	if fields := strings.Split(s, ":"); len(fields) == 3 {
		var err error
		if hours, err = strconv.Atoi(fields[0]); err != nil {
			return 0, fmt.Errorf("bad hours in %q", s)
		}
		if minutes, err = strconv.Atoi(fields[1]); err != nil {
			return 0, fmt.Errorf("bad minutes in %q", s)
		}
		secPart = fields[2]
	} else if len(fields) == 2 {
		var err error
		if minutes, err = strconv.Atoi(fields[0]); err != nil {
			return 0, fmt.Errorf("bad minutes in %q", s)
		}
		secPart = fields[1]
	} else if len(fields) != 1 {
		return 0, fmt.Errorf("bad timestamp %q", s)
	}

	secs, err := strconv.ParseFloat(secPart, 64)
	if err != nil {
		return 0, fmt.Errorf("bad seconds in %q", s)
	}
	if hours < 0 || minutes < 0 || secs < 0 {
		return 0, fmt.Errorf("negative timestamp %q", s)
	}

	total := time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(secs*float64(time.Second))
	return total, nil
}

// formatTimestamp renders d as "HH:MM:SS<sep>mmm". sep is "." for WebVTT and
// "," for SubRip.
func formatTimestamp(d time.Duration, sep string) string {
	if d < 0 {
		d = 0
	}
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	ms := (d - s*time.Second) / time.Millisecond
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
}

// Merge combines the cues of several WebVTT files — the segments of one HLS
// subtitle rendition, in playlist order — into a single WebVTT document.
//
// Cues are sorted by start time (stably, so same-start cues keep segment order)
// and exact duplicates are dropped: a cue spanning a segment boundary is
// repeated in both segments, and without de-duplication it would render twice.
func Merge(w io.Writer, segments []io.Reader) error {
	var all []Cue
	for i, seg := range segments {
		cues, err := Parse(seg)
		if err != nil {
			return fmt.Errorf("segment %d: %w", i, err)
		}
		all = append(all, cues...)
	}

	sort.SliceStable(all, func(a, b int) bool { return all[a].Start < all[b].Start })

	seen := make(map[string]bool, len(all))
	deduped := all[:0]
	for _, c := range all {
		key := fmt.Sprintf("%d|%d|%s", c.Start, c.End, c.Text())
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, c)
	}

	return WriteVTT(w, deduped)
}

// WriteVTT serializes cues as a WebVTT document.
func WriteVTT(w io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString("WEBVTT\n\n"); err != nil {
		return err
	}
	for _, c := range cues {
		timing := fmt.Sprintf("%s --> %s",
			formatTimestamp(c.Start, "."), formatTimestamp(c.End, "."))
		if c.Settings != "" {
			timing += " " + c.Settings
		}
		if _, err := fmt.Fprintf(bw, "%s\n%s\n\n", timing, c.Text()); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// WriteSRT serializes cues as a SubRip document: sequentially numbered blocks
// with comma-separated milliseconds. Cue settings are dropped because SubRip
// has no equivalent, and cues are renumbered from 1.
func WriteSRT(w io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(w)
	n := 0
	for _, c := range cues {
		text := strings.TrimSpace(c.Text())
		if text == "" {
			continue
		}
		n++
		if _, err := fmt.Fprintf(bw, "%d\n%s --> %s\n%s\n\n",
			n,
			formatTimestamp(c.Start, ","),
			formatTimestamp(c.End, ","),
			text,
		); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ToSRT converts a WebVTT document to SubRip.
func ToSRT(w io.Writer, r io.Reader) error {
	cues, err := Parse(r)
	if err != nil {
		return err
	}
	return WriteSRT(w, cues)
}
