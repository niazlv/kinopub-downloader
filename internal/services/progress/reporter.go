// Package progress implements the ProgressReporter interface with two
// implementations: a live interactive display and a log-based fallback.
// (Req 10)
package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"kinopub_downloader/internal/domain"
	"kinopub_downloader/internal/lib/logx"
)

// LiveReporter renders a multi-line live progress display refreshed at least
// once per second. It uses ANSI escape codes to update in place and shares
// the logx.Coordinator mutex so log lines don't corrupt the display.
// (Req 10.1, 10.2, 10.3, 10.7, 10.8)
type LiveReporter struct {
	w     io.Writer
	coord *logx.Coordinator

	mu   sync.Mutex
	plan domain.SeriesPlan

	// Tracking state
	completedTotal   int            // total completed episodes across all seasons
	completedSeason  map[int]int    // season → completed count
	currentEpisodes  map[domain.EpisodeKey]*episodeState
	failedEpisodes   map[domain.EpisodeKey]error

	// Display state
	lastLines int // number of lines rendered in the last frame
	stopped   bool

	// Ticker for periodic refresh
	ticker *time.Ticker
	done   chan struct{}
}

// episodeState tracks per-episode download progress.
type episodeState struct {
	tracks map[domain.TrackRef]int // track → percent [0,100]
}

// NewLive creates a LiveReporter that writes to w and coordinates with the
// given logx.Coordinator for TTY line-discipline.
func NewLive(w io.Writer, coord *logx.Coordinator) *LiveReporter {
	return &LiveReporter{
		w:     w,
		coord: coord,
	}
}

// Start begins reporting for the full series plan.
func (r *LiveReporter) Start(plan domain.SeriesPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.plan = plan
	r.completedTotal = 0
	r.completedSeason = make(map[int]int, len(plan.Seasons))
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
		tracks: make(map[domain.TrackRef]int),
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
	ep.tracks[track] = clampPercent(percent)
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

	// Series progress line.
	seriesPct := r.seriesPercent()
	lines = append(lines, fmt.Sprintf("Series: %d%% (%d/%d episodes)", seriesPct, r.completedTotal, r.plan.Total))

	// Season progress lines.
	for season, total := range r.plan.Seasons {
		completed := r.completedSeason[season]
		pct := computePercent(completed, total)
		lines = append(lines, fmt.Sprintf("  Season %d: %d%% (%d/%d)", season, pct, completed, total))
	}

	// Current episode progress.
	for key, ep := range r.currentEpisodes {
		epPct := r.episodePercent(ep)
		lines = append(lines, fmt.Sprintf("  ↓ S%02dE%02d: %d%%", key.Season, key.Episode, epPct))
		for track, pct := range ep.tracks {
			label := trackLabel(track)
			lines = append(lines, fmt.Sprintf("      %s: %d%%", label, pct))
		}
	}

	// Failed episodes.
	for key, err := range r.failedEpisodes {
		lines = append(lines, fmt.Sprintf("  ✗ S%02dE%02d: FAILED (%s)", key.Season, key.Episode, err.Error()))
	}

	output := strings.Join(lines, "\n") + "\n"
	fmt.Fprint(r.w, output)
	r.lastLines = len(lines)
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

// Verify that *LiveReporter satisfies domain.ProgressReporter at compile time.
var _ domain.ProgressReporter = (*LiveReporter)(nil)
