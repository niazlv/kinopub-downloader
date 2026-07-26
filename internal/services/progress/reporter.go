// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package progress implements the ProgressReporter interface with two
// implementations: a live interactive display and a log-based fallback.
// (Req 10)
package progress

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// Indentation (in columns) for each row kind in the live display. Bars are
// aligned to a shared column regardless of indent so they never jump sideways.
const (
	indentSeries  = 2
	indentSeason  = 4
	indentEpisode = 4
	indentTrack   = 6
)

// LiveReporter renders a multi-line live progress display refreshed at least
// once per second. It uses ANSI escape codes to update in place and shares
// the logx.Coordinator mutex so log lines don't corrupt the display.
// (Req 10.1, 10.2, 10.3, 10.7, 10.8)
type LiveReporter struct {
	w     io.Writer
	coord *logx.Coordinator
	isTTY bool

	mu   sync.Mutex
	plan domain.SeriesPlan

	// Tracking state
	completedTotal   int         // total completed episodes across all seasons
	completedSeason  map[int]int // season → completed count
	currentEpisodes  map[domain.EpisodeKey]*episodeState
	failedEpisodes   map[domain.EpisodeKey]error
	deferredEpisodes map[domain.EpisodeKey]deferredInfo // parked for later retry

	// Display state
	lastLines  int       // number of lines rendered in the last frame
	lastRender time.Time // last time we rendered (for throttling TrackProgress)
	stopped    bool

	// Ticker for periodic refresh
	ticker *time.Ticker
	done   chan struct{}
}

// deferredInfo records why an episode is parked for a later retry.
type deferredInfo struct {
	err      error
	attempts int
}

// episodeState tracks per-episode download progress.
type episodeState struct {
	tracks           map[domain.TrackRef]int // track → percent [0,100]
	startTime        time.Time
	firstProgressAt  time.Time // when we first received a non-zero progress update
	firstProgressPct int       // percent at that moment

	// Byte-level tracking for speed and size display.
	totalBytes      int64     // total file size (0 if unknown)
	downloadedBytes int64     // bytes downloaded so far
	lastSpeedBytes  int64     // bytes at last speed sample
	lastSpeedTime   time.Time // time of last speed sample
	speed           float64   // current speed in bytes/sec (smoothed)

	// HLS segment-level tracking.
	doneSegments  int  // segments downloaded so far
	totalSegments int  // total segments to download
	sizeIsApprox  bool // true when totalBytes is an estimate (HLS)

	// Per-track HLS breakdown for nested display (video + audio tracks).
	hlsTracks []domain.TrackProgressInfo
}

// NewLive creates a LiveReporter that writes to w and coordinates with the
// given logx.Coordinator for TTY line-discipline.
func NewLive(w io.Writer, coord *logx.Coordinator) *LiveReporter {
	return &LiveReporter{
		w:     w,
		coord: coord,
		isTTY: true,
	}
}

// Start begins reporting for the full series plan.
func (r *LiveReporter) Start(plan domain.SeriesPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.plan = plan
	r.completedTotal = plan.AlreadyCompleted
	r.completedSeason = make(map[int]int, len(plan.Seasons))
	for season, count := range plan.CompletedPerSeason {
		r.completedSeason[season] = count
	}
	r.currentEpisodes = make(map[domain.EpisodeKey]*episodeState)
	r.failedEpisodes = make(map[domain.EpisodeKey]error)
	r.deferredEpisodes = make(map[domain.EpisodeKey]deferredInfo)
	r.lastLines = 0
	r.stopped = false

	// Register redraw callback with coordinator so log writes trigger a
	// repaint of the progress region.
	r.coord.SetClear(r.clearForLog)
	r.coord.SetRedraw(r.redraw)

	// Start the 1-second refresh ticker.
	r.done = make(chan struct{})
	r.ticker = time.NewTicker(1 * time.Second)
	go r.tickLoop()
}

// EpisodeStarted signals that an episode download has begun.
func (r *LiveReporter) EpisodeStarted(key domain.EpisodeKey) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// A (re)attempt clears any prior deferred/failed marker for this episode.
	delete(r.deferredEpisodes, key)
	delete(r.failedEpisodes, key)

	r.currentEpisodes[key] = &episodeState{
		tracks:    make(map[domain.TrackRef]int),
		startTime: time.Now(),
	}
	r.render()
}

// EpisodeDeferred signals that an episode failed on a transient error and has
// been parked for a later retry. It is shown in a dedicated section so the user
// sees the tool will come back to it rather than having abandoned it.
func (r *LiveReporter) EpisodeDeferred(key domain.EpisodeKey, err error, attempts int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.currentEpisodes, key)
	delete(r.failedEpisodes, key)
	r.deferredEpisodes[key] = deferredInfo{err: err, attempts: attempts}
	r.render()
}

// TrackProgress reports per-track download progress.
func (r *LiveReporter) TrackProgress(key domain.EpisodeKey, track domain.TrackRef, percent int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ep, ok := r.currentEpisodes[key]
	if !ok {
		return
	}
	percent = clampPercent(percent)
	ep.tracks[track] = percent

	// Record the first meaningful progress for accurate ETA calculation.
	// We wait until percent >= 2 to skip the initial ffmpeg header copy burst.
	if ep.firstProgressAt.IsZero() && percent >= 2 {
		ep.firstProgressAt = time.Now()
		ep.firstProgressPct = percent
	}

	// Throttle renders to avoid excessive redraws (ffmpeg emits progress frequently).
	now := time.Now()
	if now.Sub(r.lastRender) >= 200*time.Millisecond {
		r.lastRender = now
		r.render()
	}
}

// sampleSpeed updates the smoothed download speed from a new cumulative byte
// count, using a one-second sliding window and an exponential moving average.
// The first call seeds the window without producing a sample. Must be called
// with r.mu held (it mutates ep in place).
func (ep *episodeState) sampleSpeed(downloaded int64, now time.Time) {
	if ep.lastSpeedTime.IsZero() {
		ep.lastSpeedTime = now
		ep.lastSpeedBytes = downloaded
		return
	}

	elapsed := now.Sub(ep.lastSpeedTime)
	if elapsed < 1*time.Second {
		return
	}

	byteDiff := downloaded - ep.lastSpeedBytes
	instantSpeed := float64(byteDiff) / elapsed.Seconds()

	// Exponential moving average for smooth speed display.
	if ep.speed == 0 {
		ep.speed = instantSpeed
	} else {
		ep.speed = ep.speed*0.7 + instantSpeed*0.3
	}

	// If no bytes were transferred, decay speed toward zero quickly.
	if byteDiff == 0 {
		ep.speed *= 0.3
		if ep.speed < 1024 {
			ep.speed = 0
		}
	}

	ep.lastSpeedTime = now
	ep.lastSpeedBytes = downloaded
}

// ByteProgress reports byte-level download progress for speed calculation.
func (r *LiveReporter) ByteProgress(key domain.EpisodeKey, downloaded, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ep, ok := r.currentEpisodes[key]
	if !ok {
		return
	}

	ep.downloadedBytes = downloaded
	ep.totalBytes = total
	ep.sampleSpeed(downloaded, time.Now())
}

// SegmentProgress reports HLS segment-level progress with an approximate total
// size estimated from the average segment size so far.
func (r *LiveReporter) SegmentProgress(key domain.EpisodeKey, doneSegments, totalSegments int, downloadedBytes, approxTotalBytes int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ep, ok := r.currentEpisodes[key]
	if !ok {
		return
	}

	ep.doneSegments = doneSegments
	ep.totalSegments = totalSegments
	ep.sizeIsApprox = true

	// Reuse the byte-progress machinery for speed/size by recording the
	// downloaded and estimated-total byte counts.
	ep.downloadedBytes = downloadedBytes
	ep.totalBytes = approxTotalBytes
	ep.sampleSpeed(downloadedBytes, time.Now())
}

// HLSProgress reports the per-track breakdown (video + audio tracks) so the
// live display can render a nested set of bars under the episode line.
func (r *LiveReporter) HLSProgress(key domain.EpisodeKey, tracks []domain.TrackProgressInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ep, ok := r.currentEpisodes[key]
	if !ok {
		return
	}
	ep.hlsTracks = tracks
}

// EpisodeCompleted signals that an episode download finished successfully.
func (r *LiveReporter) EpisodeCompleted(key domain.EpisodeKey) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.currentEpisodes, key)
	delete(r.deferredEpisodes, key)
	r.completedTotal++
	r.completedSeason[key.Season]++
	r.render()
}

// EpisodeFailed signals that an episode download failed.
func (r *LiveReporter) EpisodeFailed(key domain.EpisodeKey, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.currentEpisodes, key)
	r.failedEpisodes[key] = err
	r.render()
}

// Stop flushes and tears down the live display.
func (r *LiveReporter) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()

	// Stop the ticker.
	r.ticker.Stop()
	close(r.done)

	// Unregister redraw callback.
	r.coord.SetRedraw(nil)
	r.coord.SetClear(nil)

	// Final render to show completion state, then leave cursor below. The frame
	// is composed under r.mu and painted after releasing it, per the lock
	// ordering documented below.
	r.mu.Lock()
	frame, prevLines := r.composeFrame()
	r.lastLines = 0
	r.mu.Unlock()

	r.coord.WriteProgress(func() {
		writeClearLines(r.w, prevLines)
		// Write a newline so subsequent output starts on a fresh line.
		fmt.Fprint(r.w, frame+"\n")
	})
}

// tickLoop runs the periodic refresh.
func (r *LiveReporter) tickLoop() {
	for {
		select {
		case <-r.done:
			return
		case <-r.ticker.C:
			r.mu.Lock()
			// Decay speed for episodes that haven't received data recently.
			now := time.Now()
			for _, ep := range r.currentEpisodes {
				if !ep.lastSpeedTime.IsZero() && now.Sub(ep.lastSpeedTime) > 3*time.Second {
					ep.speed *= 0.5
					if ep.speed < 1024 {
						ep.speed = 0
					}
				}
			}
			r.render()
			r.mu.Unlock()
		}
	}
}

// Lock ordering
//
// Two mutexes guard the display: r.mu (reporter state) and the coordinator's
// mutex (TTY line discipline). The coordinator calls back into clearForLog and
// redraw while holding its own mutex, and those callbacks need r.mu — so the
// only safe global order is coord.mu → r.mu. Every path that starts from the
// reporter side (the public methods and the ticker) therefore builds its frame
// under r.mu, releases r.mu, and only then enters the coordinator. Holding r.mu
// across a coordinator call would invert the order and deadlock against any
// concurrent log write.

// render composes the next frame and hands it to the coordinator for painting.
// Must be called with r.mu held; it releases r.mu for the duration of the
// coordinator call and re-acquires it before returning, so callers that hold
// r.mu across the call still observe consistent state afterwards.
func (r *LiveReporter) render() {
	frame, prevLines := r.composeFrame()
	r.mu.Unlock()
	r.coord.WriteProgress(func() {
		writeClearLines(r.w, prevLines)
		fmt.Fprint(r.w, frame)
	})
	r.mu.Lock()
}

// redraw is the callback registered with the coordinator. It is called (with
// the coordinator mutex held) after a log line is written, to restore the
// progress display below the log output.
func (r *LiveReporter) redraw() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// After a log line, our previous display was already cleared by clearForLog,
	// so lastLines is 0 and only the frame itself has to be written.
	frame, _ := r.composeFrame()
	fmt.Fprint(r.w, frame)
}

// clearForLog is the callback registered with the coordinator. It is called
// (with the coordinator mutex held) before a log line is written, to erase
// the progress display so the log line appears cleanly above it.
func (r *LiveReporter) clearForLog() {
	r.mu.Lock()
	defer r.mu.Unlock()
	writeClearLines(r.w, r.lastLines)
	r.lastLines = 0
}

// writeClearLines moves the cursor up and clears n lines of a previous frame.
// It touches no reporter state, so it is safe to call outside r.mu.
func writeClearLines(w io.Writer, n int) {
	for i := 0; i < n; i++ {
		// Move cursor up one line and clear it.
		fmt.Fprint(w, "\033[A\033[2K")
	}
}

// frameLayout holds the per-frame column geometry. It is computed once from
// the terminal width so that every bar starts at the same column and has the
// same width, regardless of how long the row's label is. This is what keeps
// the bars from jumping sideways between frames.
type frameLayout struct {
	width    int // terminal width in columns
	labelCol int // columns reserved for "<indent><label>" before the bar
	barWidth int // number of cells inside the bar (excluding brackets)
}

// computeLayout derives a stable layout from the terminal width. The values
// are clamped so the display stays readable on both narrow and wide terminals.
func computeLayout(termWidth int) frameLayout {
	// Reserve roughly 2/5 of the width for the label column so long audio
	// track names remain mostly readable, and ~1/5 for the bar itself.
	labelCol := clampInt(termWidth*2/5, 14, 42)
	barWidth := clampInt(termWidth/5, 10, 20)
	return frameLayout{width: termWidth, labelCol: labelCol, barWidth: barWidth}
}

// composeFrame builds the frame for the current progress state and returns it
// together with the line count of the frame it replaces. It records the new
// line count in r.lastLines, so the caller must paint the returned frame.
// Must be called with r.mu held; it performs no I/O and takes no other lock,
// which is what lets callers paint outside r.mu (see "Lock ordering" above).
func (r *LiveReporter) composeFrame() (frame string, prevLines int) {
	prevLines = r.lastLines

	var lines []string
	lay := computeLayout(termx.TerminalWidth())
	sepWidth := min(lay.width, 60)

	// ─── Header separator ───
	lines = append(lines, r.colorize(termx.Gray, r.repeatChar('─', sepWidth)))

	// Series progress line with colored bar.
	seriesPct := r.seriesPercent()
	lines = append(lines, r.barRow(lay, indentSeries, "Series", termx.Bold, seriesPct, termx.Green,
		fmt.Sprintf("%d/%d episodes", r.completedTotal, r.plan.Total)))

	// Season progress lines (sorted by season number).
	seasonNums := make([]int, 0, len(r.plan.Seasons))
	for season := range r.plan.Seasons {
		seasonNums = append(seasonNums, season)
	}
	sort.Ints(seasonNums)

	for _, season := range seasonNums {
		total := r.plan.Seasons[season]
		completed := r.completedSeason[season]
		pct := computePercent(completed, total)

		barColor := termx.Blue
		if pct == 100 {
			barColor = termx.Green
		}
		lines = append(lines, r.barRow(lay, indentSeason, fmt.Sprintf("Season %d", season), "",
			pct, barColor, fmt.Sprintf("%d/%d", completed, total)))
	}

	// Current episode downloads (parallel).
	if len(r.currentEpisodes) > 0 {
		lines = append(lines, "") // blank separator
		lines = append(lines, r.colorize(termx.Cyan, "  ⬇ Downloading:"))

		// Sort episode keys for stable display.
		keys := make([]domain.EpisodeKey, 0, len(r.currentEpisodes))
		for key := range r.currentEpisodes {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Season != keys[j].Season {
				return keys[i].Season < keys[j].Season
			}
			return keys[i].Episode < keys[j].Episode
		})

		for _, key := range keys {
			ep := r.currentEpisodes[key]
			epPct := r.episodePercent(ep)

			lines = append(lines, r.barRow(lay, indentEpisode,
				key.Label(), termx.Bold,
				epPct, termx.Yellow, r.episodeStats(ep, epPct)))

			// Nested per-track breakdown (video + each audio track).
			for _, ti := range ep.hlsTracks {
				tPct := 0
				if ti.TotalSegments > 0 {
					tPct = computePercent(ti.DoneSegments, ti.TotalSegments)
				}
				lines = append(lines, r.barRow(lay, indentTrack, ti.Label, "",
					tPct, termx.Magenta, r.trackStats(ti)))
			}
		}
	}

	// Deferred episodes (parked for a later retry).
	if len(r.deferredEpisodes) > 0 {
		lines = append(lines, "") // blank separator
		deferredKeys := make([]domain.EpisodeKey, 0, len(r.deferredEpisodes))
		for key := range r.deferredEpisodes {
			deferredKeys = append(deferredKeys, key)
		}
		sort.Slice(deferredKeys, func(i, j int) bool {
			if deferredKeys[i].Season != deferredKeys[j].Season {
				return deferredKeys[i].Season < deferredKeys[j].Season
			}
			return deferredKeys[i].Episode < deferredKeys[j].Episode
		})

		for _, key := range deferredKeys {
			di := r.deferredEpisodes[key]
			label := fmt.Sprintf("⟳ %s ", key.Label())
			note := fmt.Sprintf("retry pending (attempt %d)", di.attempts)
			if di.err != nil {
				note = fmt.Sprintf("retry pending (attempt %d): %s", di.attempts, di.err.Error())
			}
			budget := lay.width - displayWidth(label) - indentEpisode - 1
			note = truncateText(note, budget)
			deferLine := fmt.Sprintf("    %s%s",
				r.colorize(termx.Yellow, label),
				r.colorize(termx.Gray, note))
			lines = append(lines, deferLine)
		}
	}

	// Failed episodes.
	if len(r.failedEpisodes) > 0 {
		lines = append(lines, "") // blank separator
		// Sort failed keys for stable display.
		failedKeys := make([]domain.EpisodeKey, 0, len(r.failedEpisodes))
		for key := range r.failedEpisodes {
			failedKeys = append(failedKeys, key)
		}
		sort.Slice(failedKeys, func(i, j int) bool {
			if failedKeys[i].Season != failedKeys[j].Season {
				return failedKeys[i].Season < failedKeys[j].Season
			}
			return failedKeys[i].Episode < failedKeys[j].Episode
		})

		for _, key := range failedKeys {
			label := fmt.Sprintf("✗ %s ", key.Label())
			// Truncate the error so the whole line fits the terminal width.
			budget := lay.width - displayWidth(label) - indentEpisode - 1
			errMsg := truncateText(r.failedEpisodes[key].Error(), budget)
			failLine := fmt.Sprintf("    %s%s",
				r.colorize(termx.Red, label),
				r.colorize(termx.Gray, errMsg))
			lines = append(lines, failLine)
		}
	}

	// Bottom separator.
	lines = append(lines, r.colorize(termx.Gray, r.repeatChar('─', sepWidth)))

	r.lastLines = len(lines)
	return strings.Join(lines, "\n") + "\n", prevLines
}

// barRow renders a single aligned progress row:
//
//	<indent><label padded/clipped to labelCol> <bar> <pct> <stats>
//
// The bar always starts at the same column (labelCol) and has a fixed width, so
// bars never shift horizontally between frames. The trailing stats are clipped
// to whatever space remains so the line never wraps.
func (r *LiveReporter) barRow(lay frameLayout, indent int, label, labelColor string, pct int, barColor, stats string) string {
	// Build the "<indent><label>" cell padded/clipped to exactly labelCol cols.
	cell := strings.Repeat(" ", indent) + label
	cell = padOrClip(cell, lay.labelCol)
	if labelColor != "" {
		// Color only the label text, keeping the padding plain so widths align.
		trimmed := strings.TrimRight(cell, " ")
		pad := cell[len(trimmed):]
		cell = r.colorize(labelColor, trimmed) + pad
	}

	bar := r.progressBar(pct, lay.barWidth, barColor)

	// Remaining columns for the stats tail (after label, space, bar+brackets,
	// a space, and the " 100%" suffix).
	used := lay.labelCol + 1 + (lay.barWidth + 2) + 1 + 5
	remaining := lay.width - used - 1
	statsOut := ""
	if stats != "" && remaining > 1 {
		statsOut = " " + r.colorize(termx.Gray, truncateText(stats, remaining-1))
	}

	return fmt.Sprintf("%s %s%s", cell, bar, statsOut)
}

// episodeStats builds the trailing stats string for an episode row (segments,
// speed, size and ETA). Returned as plain text; coloring/clipping is done by
// barRow.
func (r *LiveReporter) episodeStats(ep *episodeState, epPct int) string {
	var parts []string

	if ep.totalSegments > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d seg", ep.doneSegments, ep.totalSegments))
	}
	if ep.speed > 0 {
		parts = append(parts, fmt.Sprintf("%s/s", formatBytesShort(ep.speed)))
	}
	if ep.totalBytes > 0 {
		prefix := ""
		if ep.sizeIsApprox {
			prefix = "~"
		}
		parts = append(parts, fmt.Sprintf("%s/%s%s",
			formatBytesShort(float64(ep.downloadedBytes)), prefix,
			formatBytesShort(float64(ep.totalBytes))))
	} else if ep.downloadedBytes > 0 {
		parts = append(parts, formatBytesShort(float64(ep.downloadedBytes)))
	}

	if eta := r.episodeETA(ep, epPct); eta != "" {
		parts = append(parts, "ETA "+eta)
	}

	return strings.Join(parts, " ")
}

// episodeETA returns a formatted ETA string, or "" when it cannot be estimated.
func (r *LiveReporter) episodeETA(ep *episodeState, epPct int) string {
	// Prefer speed-based estimation (more accurate for chunked downloads).
	if ep.speed > 0 && ep.totalBytes > 0 && epPct < 100 {
		remaining := float64(ep.totalBytes-ep.downloadedBytes) / ep.speed
		if remaining > 0 {
			return formatDuration(time.Duration(remaining * float64(time.Second)))
		}
		return ""
	}
	// Fall back to time-based estimation.
	if epPct > 2 && epPct < 100 && !ep.firstProgressAt.IsZero() {
		elapsed := time.Since(ep.firstProgressAt)
		pctDone := epPct - ep.firstProgressPct
		if pctDone > 0 {
			totalEstimate := elapsed * time.Duration(100-ep.firstProgressPct) / time.Duration(pctDone)
			if remaining := totalEstimate - elapsed; remaining > 0 {
				return formatDuration(remaining)
			}
		}
	}
	return ""
}

// trackStats builds the trailing stats string for a nested track row.
func (r *LiveReporter) trackStats(ti domain.TrackProgressInfo) string {
	parts := []string{fmt.Sprintf("%d/%d seg", ti.DoneSegments, ti.TotalSegments)}
	if ti.ApproxTotalBytes > 0 {
		parts = append(parts, fmt.Sprintf("%s/~%s",
			formatBytesShort(float64(ti.DownloadedBytes)),
			formatBytesShort(float64(ti.ApproxTotalBytes))))
	} else if ti.DownloadedBytes > 0 {
		parts = append(parts, formatBytesShort(float64(ti.DownloadedBytes)))
	}
	return strings.Join(parts, " ")
}

// progressBar renders a colored progress bar of the given width followed by a
// fixed-width percentage suffix (" 100%").
// Example: [████████░░░░░░░░░░░░] 45%
func (r *LiveReporter) progressBar(percent, width int, color string) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := (percent * width) / 100
	empty := width - filled

	var b strings.Builder
	b.WriteString(r.colorize(termx.Gray, "["))
	b.WriteString(color)
	b.WriteString(strings.Repeat("█", filled))
	b.WriteString(termx.Reset)
	b.WriteString(r.colorize(termx.Gray, strings.Repeat("░", empty)))
	b.WriteString(r.colorize(termx.Gray, "]"))
	b.WriteString(fmt.Sprintf(" %3d%%", percent))

	return b.String()
}

// displayWidth returns the number of terminal columns a plain (no-ANSI) string
// occupies. Latin and Cyrillic letters are single-width; this is a deliberate
// approximation that ignores rare wide/combining runes.
func displayWidth(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// padOrClip right-pads s with spaces, or clips it with an ellipsis, so the
// result occupies exactly cols columns.
func padOrClip(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) == cols {
		return s
	}
	if len(rs) < cols {
		return s + strings.Repeat(" ", cols-len(rs))
	}
	if cols == 1 {
		return "…"
	}
	return string(rs[:cols-1]) + "…"
}

// truncateText clips s to at most cols columns, adding an ellipsis when cut.
func truncateText(s string, cols int) string {
	if cols <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= cols {
		return s
	}
	if cols == 1 {
		return "…"
	}
	return string(rs[:cols-1]) + "…"
}

// clampInt clamps v to the inclusive range [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// colorize wraps text in ANSI color codes if TTY is detected.
func (r *LiveReporter) colorize(color, text string) string {
	if !r.isTTY {
		return text
	}
	return color + text + termx.Reset
}

// repeatChar repeats a rune n times.
func (r *LiveReporter) repeatChar(ch rune, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(string(ch), n)
}

// seriesPercent computes the overall series completion percentage.
func (r *LiveReporter) seriesPercent() int {
	if r.plan.Total == 0 {
		return 0
	}
	return computePercent(r.completedTotal, r.plan.Total)
}

// episodePercent computes the average track progress for an episode.
func (r *LiveReporter) episodePercent(ep *episodeState) int {
	if len(ep.tracks) == 0 {
		return 0
	}
	var sum int
	for _, pct := range ep.tracks {
		sum += pct
	}
	return computePercent(sum, len(ep.tracks)*100)
}

// computePercent returns floor(100 * completed / total) clamped to [0,100].
func computePercent(completed, total int) int {
	if total <= 0 {
		return 0
	}
	pct := (100 * completed) / total
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// clampPercent clamps a percent value to [0,100].
func clampPercent(pct int) int {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// formatDuration formats a duration as a human-readable string (e.g. "2m30s", "45s").
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		if s > 0 {
			return fmt.Sprintf("%dm%ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) - h*60
	return fmt.Sprintf("%dh%dm", h, m)
}

// Verify that *LiveReporter satisfies domain.ProgressReporter at compile time.
var _ domain.ProgressReporter = (*LiveReporter)(nil)

// Verify that *LiveReporter satisfies domain.ByteProgressSink at compile time.
var _ domain.ByteProgressSink = (*LiveReporter)(nil)

// Verify that *LiveReporter satisfies domain.SegmentProgressSink at compile time.
var _ domain.SegmentProgressSink = (*LiveReporter)(nil)

// Verify that *LiveReporter satisfies domain.HLSProgressSink at compile time.
var _ domain.HLSProgressSink = (*LiveReporter)(nil)

// formatBytesShort formats a byte count as a compact human-readable string.
func formatBytesShort(b float64) string {
	const (
		KB = 1024.0
		MB = 1024.0 * KB
		GB = 1024.0 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1fG", b/GB)
	case b >= MB:
		return fmt.Sprintf("%.1fM", b/MB)
	case b >= KB:
		return fmt.Sprintf("%.0fK", b/KB)
	default:
		return fmt.Sprintf("%.0fB", b)
	}
}
