// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// renderFormats prints the --list-formats table: one row per rendition with an
// id that -f accepts as is. The layout follows yt-dlp's -F so the two tools
// read alike; the last column shows the pattern a track id stands for, which
// is also what -f accepts instead of the id when every match is wanted.
func renderFormats(w io.Writer, st termx.Styler, l *domain.FormatListing) {
	title := l.Episode.Label()
	if l.Title != "" {
		title += "  " + l.Title
	}
	if l.Duration > 0 {
		title += "  (" + formatClock(l.Duration) + ")"
	}
	fmt.Fprintln(w, st.Bold(title))
	if l.Matching > 1 {
		fmt.Fprintln(w, st.Gray(fmt.Sprintf(
			"%d matching episodes; the listing is for the first, the rest normally offer the same", l.Matching)))
	}

	rows := [][]string{{"ID", "KIND", "RESOLUTION", "FPS", "CODEC", "BITRATE", "~SIZE", "LANG", "NAME", "PATTERN"}}
	for _, v := range l.Video {
		id, kind := string(v.Quality), "video"
		if l.Feed {
			kind = "file"
			if id == "" {
				id = "file"
			}
		}
		rows = append(rows, []string{
			id, kind, resolutionOf(v), fpsOf(v), v.Codec, bitrateOf(v.BitrateKbps),
			estimateSize(v.BitrateKbps, l.Duration), "", "", "",
		})
	}
	for i, a := range l.Audio {
		rows = append(rows, trackRow(l.Feed, fmt.Sprintf("a%d", i+1), "audio", a, statsAt(l.AudioStats, i), domain.AudioSelector(a)))
	}
	for i, s := range l.Subtitles {
		rows = append(rows, trackRow(l.Feed, fmt.Sprintf("s%d", i+1), "subs", s, statsAt(l.SubtitleStats, i), domain.SubtitleSelector(s)))
	}
	if l.Feed {
		// Nothing inside a feed's file can be picked, so there is no pattern to show.
		for i := range rows {
			rows[i] = rows[i][:len(rows[i])-1]
		}
	}
	writeTable(w, st, rows)

	if example := exampleCommand(l); example != "" {
		fmt.Fprintf(w, "%s %s\n", st.Gray("Example:"), example)
	}
	switch {
	case l.Feed:
		fmt.Fprintln(w, st.Gray("         the feed serves finished files: -f (or -q) picks the file, and its audio and subtitle tracks come along as they are"))
	case len(l.Video)+len(l.Audio)+len(l.Subtitles) > 0:
		fmt.Fprintln(w, st.Gray(`         a pattern keeps every match, e.g. -f "rus"; -q, --audio and --subs still work`))
	}
}

// trackRow renders an audio or subtitle track. Inside a feed's file a track is
// information, not a choice, so it carries no id.
func trackRow(feed bool, id, kind string, t domain.TrackInfo, stats domain.TrackStats, pattern string) []string {
	codec := stats.Codec
	if stats.Channels > 0 {
		codec = strings.TrimSpace(fmt.Sprintf("%s %dch", codec, stats.Channels))
	}
	size := ""
	if stats.SizeBytes > 0 {
		size = "~" + formatSize(float64(stats.SizeBytes))
	}
	if feed {
		return []string{"", kind + " in file", "", "", codec, bitrateOf(stats.BitrateKbps), size, t.Language, t.Name, ""}
	}
	return []string{id, kind, "", "", codec, bitrateOf(stats.BitrateKbps), size, t.Language, t.Name, pattern}
}

// statsAt returns the probed stats for track i, or zero stats when the probe
// did not run or did not cover it.
func statsAt(stats []domain.TrackStats, i int) domain.TrackStats {
	if i < len(stats) {
		return stats[i]
	}
	return domain.TrackStats{}
}

// bitrateOf renders a bitrate, or nothing when it is unknown.
func bitrateOf(kbps int) string {
	if kbps <= 0 {
		return ""
	}
	return fmt.Sprintf("%d kbps", kbps)
}

// fpsOf renders the frame rate, or nothing when the master did not state it.
func fpsOf(v domain.VideoQualityInfo) string {
	if v.FPS <= 0 {
		return ""
	}
	return strconv.FormatFloat(v.FPS, 'f', -1, 64)
}

// writeTable prints rows as aligned columns, the first row as a header. Widths
// count runes, not bytes: names and studio tokens are mostly Cyrillic, and
// byte-based padding would skew every column after them.
func writeTable(w io.Writer, st termx.Styler, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for r, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(cell)
			// The last column runs free so trailing spaces never pad a line.
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)))
			}
		}
		line := strings.TrimRight(b.String(), " ")
		if r == 0 {
			line = st.Bold(line)
		}
		fmt.Fprintln(w, line)
	}
}

// resolutionOf renders "1920x1080", or just the height when the master did not
// state a width.
func resolutionOf(v domain.VideoQualityInfo) string {
	switch {
	case v.Width > 0 && v.Height > 0:
		return fmt.Sprintf("%dx%d", v.Width, v.Height)
	case v.Height > 0:
		return fmt.Sprintf("%dp", v.Height)
	}
	return ""
}

// estimateSize projects a bitrate over the episode length. It is an estimate
// (the audio is not counted, and HLS bitrates are ceilings), hence the tilde;
// empty when either input is unknown.
func estimateSize(bitrateKbps int, d time.Duration) string {
	if bitrateKbps <= 0 || d <= 0 {
		return ""
	}
	return "~" + formatSize(float64(bitrateKbps)*1000/8*d.Seconds())
}

// formatSize renders a byte count in the unit that keeps it readable: KiB for
// subtitle-sized things, MiB for episodes, GiB with a decimal above that.
func formatSize(bytes float64) string {
	const kib, mib, gib = 1024, 1024 * 1024, 1024 * 1024 * 1024
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1f GiB", bytes/gib)
	case bytes >= mib:
		return fmt.Sprintf("%.0f MiB", bytes/mib)
	default:
		return fmt.Sprintf("%.0f KiB", bytes/kib)
	}
}

// formatClock renders a duration as m:ss, or h:mm:ss past an hour.
func formatClock(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	h, m, s := total/3600, total%3600/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// exampleCommand assembles a -f that picks the first entry of each kind, so the
// reader sees the ids in the position they go in. The ids are joined with "+",
// the way yt-dlp spells "these together" (-f 137+140).
func exampleCommand(l *domain.FormatListing) string {
	var ids []string
	if len(l.Video) > 0 && l.Video[0].Quality != "" {
		ids = append(ids, string(l.Video[0].Quality))
	}
	// A feed's file comes whole: only its quality is a choice.
	if !l.Feed {
		if len(l.Audio) > 0 {
			ids = append(ids, "a1")
		}
		if len(l.Subtitles) > 0 {
			ids = append(ids, "s1")
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return `kinopub -f "` + strings.Join(ids, "+") + `" <url>`
}
