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

	"kinopub_downloader/internal/domain"
	"kinopub_downloader/internal/lib/logx"
	"kinopub_downloader/internal/lib/termx"
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
	completedTotal  int            // total completed episodes across all seasons
	completedSeason map[int]int    // season → completed count
	currentEpisodes map[domain.EpisodeKey]*episodeState
	failedEpisodes  map[domain.EpisodeKey]error

	// Display state
	lastLines  int       // number of lines rendered in the last frame
	lastRender time.Time // last time we rendered (for throttling TrackProgress)
	stopped    bool

	// Ticker for periodic refresh
	ticker *time.Ticker
	done   chan struct{}
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
	r.lastLines = 0
	r.stopped = false

	// Register redraw callback with coordinator so log writes trigger a
	// repaint of the progress region.
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

	r.currentEpisodes[key] = &episodeState{
		tracks:    make(map[domain.TrackRef]int),
		startTime: time.Now(),
	}
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

	// Calculate speed using a sliding window approach.
	now := time.Now()
	if ep.lastSpeedTime.IsZero() {
		ep.lastSpeedTime = now
		ep.lastSpeedBytes = downloaded
		return
	}

	elapsed := now.Sub(ep.lastSpeedTime)
	if elapsed >= 1*time.Second {
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
}

// EpisodeCompleted signals that an episode download finished successfully.
func (r *LiveReporter) EpisodeCompleted(key domain.EpisodeKey) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.currentEpisodes, key)
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

	// Final render to show completion state, then leave cursor below.
	r.mu.Lock()
	r.coord.WriteProgress(func() {
		r.clearLines()
		r.renderFrame()
		// Write a newline so subsequent output starts on a fresh line.
		fmt.Fprint(r.w, "\n")
		r.lastLines = 0
	})
	r.mu.Unlock()
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

// render acquires the coordinator and redraws. Must be called with r.mu held.
func (r *LiveReporter) render() {
	r.coord.WriteProgress(func() {
		r.clearLines()
		r.renderFrame()
	})
}

// redraw is the callback registered with the coordinator. It is called (with
// the coordinator mutex held) after a log line is written, to restore the
// progress display below the log output.
func (r *LiveReporter) redraw() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// After a log line, our previous display was implicitly scrolled away.
	// Reset lastLines since the log write already moved the cursor.
	r.lastLines = 0
	r.renderFrame()
}

// clearLines moves the cursor up and clears each line of the previous frame.
func (r *LiveReporter) clearLines() {
	for i := 0; i < r.lastLines; i++ {
		// Move cursor up one line and clear it.
		fmt.Fprint(r.w, "\033[A\033[2K")
	}
}

// renderFrame writes the current progress state. Must be called with r.mu held
// and within a coordinator-protected section.
func (r *LiveReporter) renderFrame() {
	var lines []string
	termWidth := termx.TerminalWidth()

	// ─── Header separator ───
	lines = append(lines, r.colorize(termx.Gray, r.repeatChar('─', min(termWidth, 60))))

	// Series progress line with colored bar.
	seriesPct := r.seriesPercent()
	seriesBar := r.progressBar(seriesPct, 20, termx.Green)
	seriesLine := fmt.Sprintf("  %s %s %s",
		r.colorize(termx.Bold, "Series"),
		seriesBar,
		r.colorize(termx.Gray, fmt.Sprintf("%d/%d episodes", r.completedTotal, r.plan.Total)))
	lines = append(lines, seriesLine)

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
		bar := r.progressBar(pct, 15, barColor)
		seasonLine := fmt.Sprintf("    Season %d %s %s",
			season, bar,
			r.colorize(termx.Gray, fmt.Sprintf("%d/%d", completed, total)))
		lines = append(lines, seasonLine)
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

			bar := r.progressBar(epPct, 25, termx.Yellow)

			// Speed and size info.
			speedStr := ""
			if ep.speed > 0 {
				speedStr = r.colorize(termx.Cyan, fmt.Sprintf(" %s/s", formatBytesShort(ep.speed)))
			}

			sizeStr := ""
			if ep.totalBytes > 0 {
				sizeStr = r.colorize(termx.Gray, fmt.Sprintf(" %s/%s",
					formatBytesShort(float64(ep.downloadedBytes)),
					formatBytesShort(float64(ep.totalBytes))))
			}

			// ETA estimation: use speed if available (more accurate for chunked),
			// otherwise fall back to time-based estimation.
			etaStr := ""
			if ep.speed > 0 && ep.totalBytes > 0 && epPct < 100 {
				remaining := float64(ep.totalBytes-ep.downloadedBytes) / ep.speed
				if remaining > 0 {
					etaStr = r.colorize(termx.Gray, fmt.Sprintf(" ETA %s", formatDuration(time.Duration(remaining*float64(time.Second)))))
				}
			} else if epPct > 2 && epPct < 100 && !ep.firstProgressAt.IsZero() {
				elapsed := time.Since(ep.firstProgressAt)
				pctDone := epPct - ep.firstProgressPct
				if pctDone > 0 {
					totalEstimate := elapsed * time.Duration(100-ep.firstProgressPct) / time.Duration(pctDone)
					remaining := totalEstimate - elapsed
					if remaining > 0 {
						etaStr = r.colorize(termx.Gray, fmt.Sprintf(" ETA %s", formatDuration(remaining)))
					}
				}
			}

			epLine := fmt.Sprintf("    %s %s%s%s%s",
				r.colorize(termx.Bold, fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
				bar,
				speedStr,
				sizeStr,
				etaStr)
			lines = append(lines, epLine)
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
			errMsg := r.failedEpisodes[key].Error()
			// Truncate long error messages.
			if len(errMsg) > 40 {
				errMsg = errMsg[:37] + "..."
			}
			failLine := fmt.Sprintf("    %s %s",
				r.colorize(termx.Red, fmt.Sprintf("✗ S%02dE%02d", key.Season, key.Episode)),
				r.colorize(termx.Gray, errMsg))
			lines = append(lines, failLine)
		}
	}

	// Bottom separator.
	lines = append(lines, r.colorize(termx.Gray, r.repeatChar('─', min(termWidth, 60))))

	output := strings.Join(lines, "\n") + "\n"
	fmt.Fprint(r.w, output)
	r.lastLines = len(lines)
}

// progressBar renders a colored progress bar of the given width.
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

// trackLabel returns a human-readable label for a track reference.
func trackLabel(ref domain.TrackRef) string {
	switch ref.Kind {
	case domain.TrackVideo:
		return "Video"
	case domain.TrackAudio:
		return fmt.Sprintf("Audio[%d]", ref.Index)
	case domain.TrackSubtitle:
		return fmt.Sprintf("Sub[%d]", ref.Index)
	default:
		return fmt.Sprintf("Track[%d]", ref.Index)
	}
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

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Verify that *LiveReporter satisfies domain.ProgressReporter at compile time.
var _ domain.ProgressReporter = (*LiveReporter)(nil)

// Verify that *LiveReporter satisfies domain.ByteProgressSink at compile time.
var _ domain.ByteProgressSink = (*LiveReporter)(nil)

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
