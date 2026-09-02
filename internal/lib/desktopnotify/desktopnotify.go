// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package desktopnotify mirrors download progress into native desktop
// notifications, using tools that ship with the OS so it adds no dependencies:
// osascript on macOS and notify-send on Linux. It is a no-op everywhere else
// (including Termux, which has its own richer notifier), so callers can wrap
// unconditionally.
//
// Unlike the Termux notifier, a desktop banner cannot be updated in place, so
// this posts only coarse events — the run starting, each episode finishing, and
// the run ending — rather than a live percentage, to avoid a stream of banners.
package desktopnotify

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// postTimeout bounds each notify command so a wedged helper never holds up a
// download; desktop tools should return immediately, but this is cheap safety.
const postTimeout = 3 * time.Second

// backend describes how to post a notification on the current OS.
type backend struct {
	// command builds the argv for a (title, content) notification.
	command func(title, content string) []string
}

// detectBackend returns the notifier for this OS, or false when none is
// available (an unsupported OS, or the tool is not installed).
func detectBackend() (backend, bool) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err == nil {
			return backend{command: func(title, content string) []string {
				// Build an AppleScript string with both fields escaped so a
				// title or content value cannot break out of the quotes.
				script := "display notification \"" + escapeAppleScript(content) +
					"\" with title \"" + escapeAppleScript(title) + "\""
				return []string{"osascript", "-e", script}
			}}, true
		}
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			return backend{command: func(title, content string) []string {
				// notify-send takes title and body as separate argv, so no
				// escaping is needed.
				return []string{"notify-send", "--app-name=kinopub", title, content}
			}}, true
		}
	}
	return backend{}, false
}

// escapeAppleScript escapes backslashes and double quotes for embedding in an
// AppleScript string literal.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
}

// Available reports whether desktop notifications can be posted on this OS.
func Available() bool {
	_, ok := detectBackend()
	return ok
}

// Notifier wraps a domain.ProgressReporter and posts native notifications for
// coarse events, forwarding everything to inner so the terminal display is
// unaffected.
type Notifier struct {
	inner   domain.ProgressReporter
	backend backend

	mu          sync.Mutex
	seriesTitle string
	total       int
	completed   int
}

// Wrap returns a reporter that adds desktop notifications. When no backend is
// available it returns inner unchanged.
func Wrap(inner domain.ProgressReporter) domain.ProgressReporter {
	b, ok := detectBackend()
	if !ok {
		return inner
	}
	return &Notifier{inner: inner, backend: b}
}

func (n *Notifier) Start(plan domain.SeriesPlan) {
	n.mu.Lock()
	n.total = plan.Total
	n.seriesTitle = plan.Title
	n.mu.Unlock()

	n.inner.Start(plan)
}

func (n *Notifier) EpisodeStarted(key domain.EpisodeKey) { n.inner.EpisodeStarted(key) }

func (n *Notifier) TrackProgress(key domain.EpisodeKey, track domain.TrackRef, percent int) {
	n.inner.TrackProgress(key, track, percent)
}

func (n *Notifier) EpisodeCompleted(key domain.EpisodeKey) {
	n.inner.EpisodeCompleted(key)

	n.mu.Lock()
	n.completed++
	done, total, title := n.completed, n.total, n.seriesTitle
	n.mu.Unlock()

	// Only announce individual episodes for a multi-episode run; a single-file
	// download is covered by the final "done" notification alone.
	if total > 1 {
		n.post(title, key.Label()+" — "+plural(done, total))
	}
}

func (n *Notifier) EpisodeFailed(key domain.EpisodeKey, err error) { n.inner.EpisodeFailed(key, err) }

func (n *Notifier) Stop() {
	n.inner.Stop()

	n.mu.Lock()
	done, total, title := n.completed, n.total, n.seriesTitle
	n.mu.Unlock()

	if done > 0 && done >= total {
		n.post("✓ "+title, "Download complete — "+plural(done, total))
	}
}

// plural renders "N/M episodes" for the notification body.
func plural(done, total int) string {
	if total == 1 {
		return "1 episode"
	}
	return strconv.Itoa(done) + "/" + strconv.Itoa(total) + " episodes"
}

// post fires a notification, bounded by postTimeout and best-effort: a missing
// or misbehaving helper must never affect the download.
func (n *Notifier) post(title, content string) {
	argv := n.backend.command(title, content)
	if len(argv) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), postTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
}

// ---------------------------------------------------------------------------
// Optional sinks forwarded to inner so the terminal display keeps its detail.
// ---------------------------------------------------------------------------

func (n *Notifier) HLSProgress(key domain.EpisodeKey, tracks []domain.TrackProgressInfo) {
	if s, ok := n.inner.(domain.HLSProgressSink); ok {
		s.HLSProgress(key, tracks)
	}
}

func (n *Notifier) SegmentProgress(key domain.EpisodeKey, done, total int, downloaded, approxTotal int64) {
	if s, ok := n.inner.(domain.SegmentProgressSink); ok {
		s.SegmentProgress(key, done, total, downloaded, approxTotal)
	}
}

func (n *Notifier) ByteProgress(key domain.EpisodeKey, downloaded, total int64) {
	if s, ok := n.inner.(domain.ByteProgressSink); ok {
		s.ByteProgress(key, downloaded, total)
	}
}
