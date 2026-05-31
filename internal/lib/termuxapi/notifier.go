// Package termuxapi integrates with the Termux:API app to show download
// progress in Android notifications and prevent the device from sleeping.
//
// All functions are no-ops when termux-notification is not in PATH, so
// the package is safe to use unconditionally on any platform.
package termuxapi

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

const notificationID = "kinopub-download"

// Available reports whether Termux:API tools are present in PATH.
func Available() bool {
	_, err := exec.LookPath("termux-notification")
	return err == nil
}

// Notifier wraps a domain.ProgressReporter and mirrors progress into
// Android notifications via termux-notification. It also acquires a
// wake lock so the device stays awake during long downloads.
type Notifier struct {
	inner domain.ProgressReporter

	mu          sync.Mutex
	plan        domain.SeriesPlan
	seriesTitle string
	completed   int
	total       int

	// current episode state (updated concurrently by TrackProgress)
	currentEp  atomic.Value // string
	currentPct atomic.Int32

	wakeLockCtx    context.Context
	wakeLockCancel context.CancelFunc
}

// Wrap returns a new Notifier that delegates to inner and adds Termux
// notifications. If Termux:API is not available, inner is returned as-is.
func Wrap(inner domain.ProgressReporter) domain.ProgressReporter {
	if !Available() {
		return inner
	}
	return &Notifier{inner: inner}
}

func (n *Notifier) Start(plan domain.SeriesPlan) {
	n.mu.Lock()
	n.plan = plan
	n.total = plan.Total
	n.seriesTitle = plan.Title
	n.mu.Unlock()

	n.inner.Start(plan)
	n.acquireWakeLock()
	n.notify("Начало загрузки", fmt.Sprintf("%s · %d эп.", n.seriesTitle, n.total), 0)
}

func (n *Notifier) EpisodeStarted(key domain.EpisodeKey) {
	label := fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)
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
	n.mu.Unlock()

	n.inner.EpisodeCompleted(key)
	ep := fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	content := fmt.Sprintf("%s · %s готово (%d/%d)", n.seriesTitle, ep, done, total)
	n.notify(fmt.Sprintf("kinopub  %d%%", pct), content, pct)
}

func (n *Notifier) EpisodeFailed(key domain.EpisodeKey, err error) {
	n.inner.EpisodeFailed(key, err)
}

func (n *Notifier) Stop() {
	n.inner.Stop()
	n.releaseWakeLock()

	n.mu.Lock()
	done := n.completed
	total := n.total
	title := n.seriesTitle
	n.mu.Unlock()

	if done >= total && total > 0 {
		n.notifyDone(fmt.Sprintf("✓ %s", title), fmt.Sprintf("Скачано %d эпизодов", done))
		go exec.Command("termux-vibrate", "-d", "300").Run() //nolint:errcheck
	} else {
		n.removeNotification()
	}
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (n *Notifier) refresh() {
	ep, _ := n.currentEp.Load().(string)
	pct := int(n.currentPct.Load())

	n.mu.Lock()
	done := n.completed
	total := n.total
	title := n.seriesTitle
	n.mu.Unlock()

	seriesPct := 0
	if total > 0 {
		seriesPct = done*100/total + pct/total
	}
	content := fmt.Sprintf("%s  %s · %d/%d эп.  %d%%", title, ep, done, total, pct)
	n.notify(fmt.Sprintf("kinopub ↓  %d%%", seriesPct), content, seriesPct)
}

func (n *Notifier) notify(title, content string, pct int) {
	args := []string{
		"--id", notificationID,
		"--title", title,
		"--content", content,
		"--ongoing",
		"--priority", "low",
		"--progress-max", "100",
		"--progress", strconv.Itoa(pct),
	}
	go exec.Command("termux-notification", args...).Run() //nolint:errcheck
}

func (n *Notifier) notifyDone(title, content string) {
	args := []string{
		"--id", notificationID,
		"--title", title,
		"--content", content,
	}
	go exec.Command("termux-notification", args...).Run() //nolint:errcheck
}

func (n *Notifier) removeNotification() {
	go exec.Command("termux-notification-remove", notificationID).Run() //nolint:errcheck
}

func (n *Notifier) acquireWakeLock() {
	ctx, cancel := context.WithCancel(context.Background())
	n.wakeLockCtx = ctx
	n.wakeLockCancel = cancel
	go func() {
		cmd := exec.CommandContext(ctx, "termux-wake-lock")
		cmd.Run() //nolint:errcheck
	}()
}

func (n *Notifier) releaseWakeLock() {
	if n.wakeLockCancel != nil {
		n.wakeLockCancel()
	}
	go exec.Command("termux-wake-unlock").Run() //nolint:errcheck
}
