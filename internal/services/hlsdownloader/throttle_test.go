// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("5"); got != 5*time.Second {
		t.Errorf("parseRetryAfter(\"5\") = %v, want 5s", got)
	}
	if got := parseRetryAfter("  10 "); got != 10*time.Second {
		t.Errorf("parseRetryAfter with spaces = %v, want 10s", got)
	}
}

func TestParseRetryAfterEmptyOrBad(t *testing.T) {
	for _, in := range []string{"", "soon", "-3", "0"} {
		if got := parseRetryAfter(in); got != 0 {
			t.Errorf("parseRetryAfter(%q) = %v, want 0", in, got)
		}
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	if got <= 0 || got > 31*time.Second {
		t.Errorf("parseRetryAfter(future date) = %v, want ~30s", got)
	}
	// A date in the past yields no wait.
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past date) = %v, want 0", got)
	}
}

// A throttleError must be recognizable through errors.As after being wrapped,
// which is how downloadSegment tells CDN pushback from an ordinary failure.
func TestThrottleErrorUnwraps(t *testing.T) {
	base := &throttleError{status: 429, retryAfter: 3 * time.Second}
	wrapped := fmt.Errorf("segment 7 failed: %w", base)

	var thr *throttleError
	if !errors.As(wrapped, &thr) {
		t.Fatal("errors.As did not find the throttleError")
	}
	if thr.status != 429 || thr.retryAfter != 3*time.Second {
		t.Errorf("unwrapped = %+v, want status 429 retryAfter 3s", thr)
	}
}
