// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package termuxapi integrates with the Termux:API app to show download
// progress in Android notifications.
//
// All functions are no-ops when termux-notification is not in PATH, so
// the package is safe to use unconditionally on any platform.
package termuxapi

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// termux-notification --id requires an integer.
const notificationID = "42314"

// helperTimeout bounds every termux-* invocation.
//
// The termux-api CLI scripts are thin wrappers that broadcast an intent to the
// Termux:API *app* and wait for it to answer. The scripts are installed by the
// `termux-api` package, but the app is a separate install — and when it is
// missing nothing ever answers, so the helper blocks forever. That used to
// strand a finished download: the episode was written and muxed, then the
// completion notification hung with no output, looking exactly like a freeze.
const helperTimeout = 3 * time.Second

// Available reports whether the Termux:API command-line tools are present in
// PATH. It cannot tell whether the companion app is installed — that is
// discovered at the first call, which is why every invocation is bounded by
// helperTimeout and failures disable notifications for the rest of the run.
func Available() bool {
	_, err := exec.LookPath("termux-notification")
	return err == nil
}

// Notifier wraps a domain.ProgressReporter and mirrors progress into
// Android notifications via termux-notification.
//
// It also implements HLSProgressSink, SegmentProgressSink and ByteProgressSink
// by forwarding to inner, so the live terminal display keeps its full
// per-track breakdown even when wrapped.
type Notifier struct {
	inner domain.ProgressReporter

	mu          sync.Mutex
	seriesTitle string
	completed   int
	total       int
	stopped     bool // guarded by mu; once true, refresh() posts nothing more

	// current episode state (updated concurrently by TrackProgress)
	currentEp  atomic.Value // string
	currentPct atomic.Int32

	// disabled is set once a helper times out: the CLI is installed but the
	// Termux:API app is not answering, so further calls would only stall.
	disabled atomic.Bool
	logger   domain.Logger

	// timeout overrides helperTimeout when non-zero. Tests set it so a wedged
	// stub fails fast and a healthy one is not misread as wedged under load.
	timeout time.Duration

	// throttle: skip notification if previous goroutine is still running
	notifying atomic.Bool
	// wg tracks in-flight refresh goroutines so Stop() can wait for them before
	// posting the final notification.
	wg sync.WaitGroup
}

// Option configures a Notifier.
type Option func(*Notifier)

// WithLogger attaches a logger, used to report once that notifications were
// switched off because the Termux:API app did not respond.
func WithLogger(l domain.Logger) Option {
	return func(n *Notifier) {
		if l != nil {
			n.logger = l.Component("termuxapi")
		}
	}
}

// Wrap returns a new Notifier that delegates to inner and adds Termux
// notifications. If Termux:API is not available, inner is returned as-is.
func Wrap(inner domain.ProgressReporter, opts ...Option) domain.ProgressReporter {
	if !Available() {
		return inner
	}
	n := &Notifier{inner: inner}
	for _, o := range opts {
		o(n)
	}
	return n
}

// ---------------------------------------------------------------------------
// domain.ProgressReporter
// ---------------------------------------------------------------------------

func (n *Notifier) Start(plan domain.SeriesPlan) {
	n.mu.Lock()
	n.total = plan.Total
	n.seriesTitle = plan.Title
	n.mu.Unlock()

	n.inner.Start(plan)
	n.notify(fmt.Sprintf("kinopub — %s", plan.Title),
		fmt.Sprintf("Начало загрузки · %d эп.", plan.Total), 0)
}

func (n *Notifier) EpisodeStarted(key domain.EpisodeKey) {
	label := key.Label()
	n.currentEp.Store(label)
	n.currentPct.Store(0)
	n.inner.EpisodeStarted(key)
	n.refresh()
}

func (n *Notifier) TrackProgress(key domain.EpisodeKey, track domain.TrackRef, percent int) {
	n.currentPct.Store(int32(percent))
	n.inner.TrackProgress(key, track, percent)
	n.refresh()
}

func (n *Notifier) EpisodeCompleted(key domain.EpisodeKey) {
	n.mu.Lock()
	n.completed++
	done := n.completed
	total := n.total
	title := n.seriesTitle
	n.mu.Unlock()

	n.inner.EpisodeCompleted(key)

	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	ep := key.Label()
	n.notify(
		fmt.Sprintf("kinopub %d%% — %s", pct, title),
		fmt.Sprintf("%s готово · %d/%d эп.", ep, done, total),
		pct,
	)
}

func (n *Notifier) EpisodeFailed(key domain.EpisodeKey, err error) {
	n.inner.EpisodeFailed(key, err)
}

func (n *Notifier) Stop() {
	n.inner.Stop()

	n.mu.Lock()
	n.stopped = true
	done := n.completed
	total := n.total
	title := n.seriesTitle
	n.mu.Unlock()

	// Wait for any in-flight ongoing-notification post to finish so the final
	// notification/removal below is the last thing the user sees.
	n.wg.Wait()

	if done > 0 && done >= total {
		n.runHelper("termux-notification",
			"--id", notificationID,
			"--title", fmt.Sprintf("✓ %s", title),
			"--content", fmt.Sprintf("Скачано %d эпизодов", done),
		)
		n.runHelper("termux-vibrate", "-d", "400")
	} else {
		n.runHelper("termux-notification-remove", notificationID)
	}
}

// ---------------------------------------------------------------------------
// Optional sink interfaces — forwarded to inner so the live terminal display
// keeps the full per-track HLS breakdown.
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

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (n *Notifier) refresh() {
	// Skip if a notification command is already in flight — TrackProgress fires
	// very frequently and termux-notification is slow (~200ms).
	if !n.notifying.CompareAndSwap(false, true) {
		return
	}

	ep, _ := n.currentEp.Load().(string)
	pct := int(n.currentPct.Load())

	n.mu.Lock()
	if n.stopped {
		// Stop() has run (or is running) — don't launch a post that could land
		// after the final notification. Releasing the throttle is enough.
		n.mu.Unlock()
		n.notifying.Store(false)
		return
	}
	n.wg.Add(1)
	done := n.completed
	total := n.total
	title := n.seriesTitle
	n.mu.Unlock()

	seriesPct := 0
	if total > 0 {
		seriesPct = done*100/total + pct/total
	}
	titleStr := fmt.Sprintf("kinopub ↓ %d%% — %s", seriesPct, title)
	content := fmt.Sprintf("%s · %d/%d эп.  %d%%", ep, done, total, pct)

	go func() {
		defer n.wg.Done()
		defer n.notifying.Store(false)
		n.runHelper("termux-notification",
			"--id", notificationID,
			"--title", titleStr,
			"--content", content,
			"--ongoing",
			"--priority", "low",
			"--progress-max", "100",
			"--progress", strconv.Itoa(seriesPct),
		)
	}()
}

func (n *Notifier) notify(title, content string, pct int) {
	n.runHelper("termux-notification",
		"--id", notificationID,
		"--title", title,
		"--content", content,
		"--ongoing",
		"--priority", "low",
		"--progress-max", "100",
		"--progress", strconv.Itoa(pct),
	)
}

// runHelper invokes a termux-* helper, bounded by helperTimeout (or n.timeout
// when set).
//
// A helper that does not return in time means the Termux:API app is absent or
// wedged; notifications are then switched off for the rest of the run so a
// finished download is never held up by a decoration. Ordinary non-zero exits
// are ignored — a missing notification must never affect the download.
func (n *Notifier) runHelper(name string, args ...string) {
	if n.disabled.Load() {
		return
	}

	cmd := exec.Command(name, args...)
	// Its own process group, so a wedged helper can be torn down together with
	// the broadcast it spawned rather than leaving a grandchild behind.
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		n.disable(name, "helper could not be started")
		return
	}

	timeout := n.timeout
	if timeout <= 0 {
		timeout = helperTimeout
	}

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		if killedBySignal(err) {
			// Ctrl+C reaches the whole foreground process group, so a helper
			// killed by a signal means the run is shutting down. Stop posting:
			// the final notification would otherwise block again and demand a
			// second Ctrl+C.
			n.disable(name, "interrupted")
		}
	case <-time.After(timeout):
		// Signal the group and return without waiting for it: reaping is left
		// to the goroutine above, so a decoration can never hold up a finished
		// download.
		killProcessGroup(cmd)
		n.disable(name, "the Termux:API app did not respond")
	}
}

// disable turns notifications off for the rest of the run, reporting why once.
func (n *Notifier) disable(helper, reason string) {
	if !n.disabled.CompareAndSwap(false, true) {
		return
	}
	if n.logger != nil {
		n.logger.Warn("Termux notifications disabled",
			domain.F("reason", reason),
			domain.F("helper", helper),
			domain.F("hint", "install the Termux:API app, or ignore — downloads are unaffected"),
		)
	}
}

// killedBySignal reports whether a helper was terminated by a signal rather
// than exiting on its own. ExitCode is -1 in exactly that case.
func killedBySignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ProcessState == nil {
		return false
	}
	return ee.ProcessState.ExitCode() == -1
}
