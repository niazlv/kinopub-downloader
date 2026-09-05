// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package termuxapi

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeHelpers puts stub termux-* scripts on PATH for the duration of a test.
// body is the shell body shared by every helper.
func fakeHelpers(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"termux-notification", "termux-notification-remove", "termux-vibrate"} {
		script := "#!/bin/sh\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// wedgedTimeout is what the tests give a helper that never returns: short, so
// the suite stays fast, but long enough for the stub to have started.
const wedgedTimeout = 200 * time.Millisecond

// healthyTimeout is what the tests give a helper that exits on its own. It is
// deliberately far above the production default so a loaded machine that is
// slow to exec the shell stub cannot make a success look like a hang.
const healthyTimeout = time.Minute

// A helper that never returns must not stall the run: the Termux:API app may be
// absent while its CLI scripts are installed, which used to leave a finished
// download hanging with no output.
func TestRunHelperTimesOutAndDisables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	fakeHelpers(t, "sleep 60")

	n := &Notifier{inner: &recordingReporter{}, timeout: wedgedTimeout}

	start := time.Now()
	n.runHelper("termux-notification", "--id", notificationID)
	elapsed := time.Since(start)

	if elapsed > wedgedTimeout+2*time.Second {
		t.Errorf("runHelper blocked for %v, want it bounded by %v", elapsed, wedgedTimeout)
	}
	if !n.disabled.Load() {
		t.Error("notifications were not disabled after a helper timed out")
	}

	// Once disabled, later calls must return immediately rather than pay the
	// timeout again on every episode.
	start = time.Now()
	n.runHelper("termux-notification", "--id", notificationID)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("disabled helper still took %v", elapsed)
	}
}

// Stop() posts the final notification; with a wedged helper it must still
// return promptly instead of demanding a second interrupt.
func TestStopDoesNotBlockOnWedgedHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	fakeHelpers(t, "sleep 60")

	n := &Notifier{inner: &recordingReporter{}, timeout: wedgedTimeout}
	n.total, n.completed, n.seriesTitle = 1, 1, "S"

	done := make(chan struct{})
	go func() {
		n.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wedgedTimeout + 2*time.Second):
		t.Fatal("Stop() blocked on a wedged termux helper")
	}
}

// A helper that exits normally leaves notifications enabled.
func TestRunHelperSuccessKeepsEnabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	fakeHelpers(t, "exit 0")

	n := &Notifier{inner: &recordingReporter{}, timeout: healthyTimeout}
	n.runHelper("termux-notification")
	if n.disabled.Load() {
		t.Error("a successful helper must not disable notifications")
	}
}
