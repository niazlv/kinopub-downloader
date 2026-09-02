// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ratelimit provides the throttling primitives the segment downloader
// uses to be a good citizen against the CDN: a byte-rate cap (--limit-rate) and
// an adaptive concurrency gate that backs off when the CDN answers 429.
//
// Both are safe for concurrent use and both degrade to no-ops when disabled, so
// callers can wire them in unconditionally.
package ratelimit

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseRate parses a human byte-rate into bytes per second.
//
// It accepts a bare number of bytes ("1048576"), or a number with a unit
// suffix: k/K/KiB and m/M/MiB and g/G/GiB are powers of 1024, while kb/mb/gb
// are powers of 1000. A trailing "/s", "ps" or "B" is ignored, so "2MB/s",
// "2m" and "2M" all parse. The empty string and "0" mean "no limit" and return
// 0 without error.
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}

	orig := s
	lower := strings.ToLower(s)
	// Drop a trailing per-second marker so "2M/s" is accepted; the unit itself
	// is matched below and must stay intact.
	lower = strings.TrimSuffix(lower, "/s")

	// Match the longest unit first so "mib"/"mb" are not shadowed by "m", and
	// the decimal "mb"/"kb"/"gb" forms are not shadowed by a bare "b" (bytes).
	mult := float64(1)
	switch {
	case strings.HasSuffix(lower, "kib"):
		mult, lower = 1024, trimUnit(lower, "kib")
	case strings.HasSuffix(lower, "mib"):
		mult, lower = 1024*1024, trimUnit(lower, "mib")
	case strings.HasSuffix(lower, "gib"):
		mult, lower = 1024*1024*1024, trimUnit(lower, "gib")
	case strings.HasSuffix(lower, "kb"):
		mult, lower = 1000, trimUnit(lower, "kb")
	case strings.HasSuffix(lower, "mb"):
		mult, lower = 1000*1000, trimUnit(lower, "mb")
	case strings.HasSuffix(lower, "gb"):
		mult, lower = 1000*1000*1000, trimUnit(lower, "gb")
	case strings.HasSuffix(lower, "k"):
		mult, lower = 1024, trimUnit(lower, "k")
	case strings.HasSuffix(lower, "m"):
		mult, lower = 1024*1024, trimUnit(lower, "m")
	case strings.HasSuffix(lower, "g"):
		mult, lower = 1024*1024*1024, trimUnit(lower, "g")
	case strings.HasSuffix(lower, "b"):
		mult, lower = 1, trimUnit(lower, "b")
	}

	value, err := strconv.ParseFloat(strings.TrimSpace(lower), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q: expected a number optionally suffixed with k/M/G (e.g. 2M, 500k)", orig)
	}
	if value < 0 {
		return 0, fmt.Errorf("invalid rate %q: must not be negative", orig)
	}

	bytes := value * mult
	if bytes > math.MaxInt64 {
		return 0, fmt.Errorf("invalid rate %q: too large", orig)
	}
	if bytes > 0 && bytes < 1 {
		bytes = 1 // never round a positive limit down to "unlimited"
	}
	return int64(bytes), nil
}

// trimUnit removes the first matching unit suffix from s.
func trimUnit(s string, units ...string) string {
	for _, u := range units {
		if strings.HasSuffix(s, u) {
			return strings.TrimSuffix(s, u)
		}
	}
	return s
}
