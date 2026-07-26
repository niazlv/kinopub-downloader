// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package hlsdownloader implements HLS segment-based downloading with
// quality selection and resume capability.
package hlsdownloader

import (
	"fmt"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// Variant represents a single quality variant from an HLS master playlist.
type Variant struct {
	Bandwidth  int    // bits per second
	Resolution string // e.g. "1920x1080"
	Codecs     string // e.g. "avc1.640028,mp4a.40.2"
	URL        string // media playlist URL (relative or absolute)
	Width      int    // parsed from Resolution
	Height     int    // parsed from Resolution
	AudioGroup string // GROUP-ID of associated audio renditions (empty = muxed)
}

// IsH265 reports whether this variant uses HEVC/H.265 codec.
func (v Variant) IsH265() bool {
	return strings.Contains(v.Codecs, "hvc1") ||
		strings.Contains(v.Codecs, "hev1") ||
		strings.Contains(v.Codecs, "hevc")
}

// IsH264 reports whether this variant uses AVC/H.264 codec.
func (v Variant) IsH264() bool {
	return strings.Contains(v.Codecs, "avc1") || v.Codecs == ""
}

// BitrateKbps returns the bandwidth in kbps.
func (v Variant) BitrateKbps() int {
	return v.Bandwidth / 1000
}

// Label returns a human-readable label for this variant.
func (v Variant) Label() string {
	codec := "h264"
	if v.IsH265() {
		codec = "h265"
	}
	return fmt.Sprintf("%dp/%s (%d kbps)", v.Height, codec, v.BitrateKbps())
}

// SelectVariant chooses the best variant based on quality preference.
//
// Quality preference modes:
//   - "" or "optimal": 1080p h264 with bitrate ≤ 3000 kbps, or 720p if unavailable
//   - "max": highest bandwidth variant
//   - "1080p": 1080p with lowest bitrate (h264 preferred)
//   - "720p": 720p with highest bitrate (h264 preferred)
//   - "480p": 480p with highest bitrate (h264 preferred)
//   - "1080p-h265": specific resolution + codec (lowest bitrate for that pair)
//
// For an explicit height the codec preference is h264 unless a "-h265" suffix
// forces otherwise; see selectExplicit for the bitrate rule per height.
func SelectVariant(variants []Variant, pref domain.Quality) (Variant, error) {
	if len(variants) == 0 {
		return Variant{}, fmt.Errorf("no variants available")
	}

	prefStr := strings.ToLower(strings.TrimSpace(string(pref)))

	switch {
	case prefStr == "" || prefStr == "optimal":
		return selectOptimal(variants), nil
	case prefStr == "max":
		return selectMax(variants), nil
	default:
		return selectExplicit(variants, prefStr)
	}
}

// selectOptimal picks the "sweet spot" variant:
// 1. 1080p h264 with bitrate ≤ 3000 kbps (ideal ~2500 kbps)
// 2. If no such variant, 720p h264 with highest bitrate
// 3. If nothing matches, closest to 2500 kbps overall
func selectOptimal(variants []Variant) Variant {
	// Try: 1080p h264 ≤ 3000 kbps
	var candidates []Variant
	for _, v := range variants {
		if v.Height == 1080 && v.IsH264() && v.BitrateKbps() <= 3000 {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) > 0 {
		// Pick the one closest to 2500 kbps.
		return closestToBitrate(candidates, 2500)
	}

	// Try: 720p h264
	for _, v := range variants {
		if v.Height == 720 && v.IsH264() {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) > 0 {
		// Pick highest bitrate 720p.
		best := candidates[0]
		for _, v := range candidates[1:] {
			if v.Bandwidth > best.Bandwidth {
				best = v
			}
		}
		return best
	}

	// Fallback: closest to 2500 kbps from all variants.
	return closestToBitrate(variants, 2500)
}

// selectMax picks the highest bandwidth variant.
func selectMax(variants []Variant) Variant {
	best := variants[0]
	for _, v := range variants[1:] {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}
	return best
}

// selectExplicit picks a variant matching the explicit preference string.
// Supports: "1080p", "720p", "480p", "1080p-h265", "720p-h264", etc.
//
// Among the variants matching the requested height two preferences apply:
//
//  1. Codec: h264 wins unless the preference carried an explicit "-h264" /
//     "-h265" suffix. h265 halves the bitrate but does not decode everywhere
//     (older TVs, browsers, low-end Android), so an unqualified "1080p" must
//     not hand back a file the user's device cannot play. h265 is still used
//     when it is the only rendition at that height.
//  2. Bitrate: at 1080p and above the lowest-bitrate rendition is picked — the
//     resolution already carries the detail and the smaller file is the point
//     of asking for a specific height. Below 1080p the highest-bitrate
//     rendition wins instead: a 720p or 480p stream is bandwidth-constrained
//     already, and the cheapest rendition of an already-low resolution is
//     visibly bad for a saving that no longer matters.
//
// A preference that names no parseable height (wantHeight == 0) keeps the
// original lowest-bitrate rule over all variants: with no resolution
// constraint, "smallest" is the safer reading of an unrecognized string —
// "max" is the documented spelling for "biggest".
func selectExplicit(variants []Variant, pref string) (Variant, error) {
	// Parse preference.
	wantHeight := 0
	wantCodec := "" // "", "h264", "h265"

	if strings.Contains(pref, "-") {
		parts := strings.SplitN(pref, "-", 2)
		pref = parts[0]
		wantCodec = parts[1]
	}

	switch pref {
	case "1080p", "1080":
		wantHeight = 1080
	case "720p", "720":
		wantHeight = 720
	case "480p", "480":
		wantHeight = 480
	case "360p", "360":
		wantHeight = 360
	case "2160p", "4k":
		wantHeight = 2160
	default:
		// Try parsing as number.
		fmt.Sscanf(pref, "%dp", &wantHeight)
		if wantHeight == 0 {
			fmt.Sscanf(pref, "%d", &wantHeight)
		}
	}

	// Filter by height.
	var candidates []Variant
	for _, v := range variants {
		if wantHeight > 0 && v.Height != wantHeight {
			continue
		}
		if wantCodec == "h264" && !v.IsH264() {
			continue
		}
		if wantCodec == "h265" && !v.IsH265() {
			continue
		}
		candidates = append(candidates, v)
	}

	if len(candidates) == 0 {
		// No exact match — find closest height.
		return closestToHeight(variants, wantHeight), nil
	}

	// With no codec named by the user, keep the h264 candidates when there are
	// any: h265 is only reached as a fallback for heights that offer nothing
	// else. Variants with no CODECS attribute count as h264 (see IsH264).
	if wantCodec == "" {
		var h264 []Variant
		for _, v := range candidates {
			if v.IsH264() {
				h264 = append(h264, v)
			}
		}
		if len(h264) > 0 {
			candidates = h264
		}
	}

	// Highest bitrate below 1080p, lowest at 1080p and above; see the doc
	// comment. Both loops use a strict comparison, so the first candidate wins
	// a tie and selection stays stable across CDN orderings.
	preferHighest := wantHeight > 0 && wantHeight < 1080

	best := candidates[0]
	for _, v := range candidates[1:] {
		if preferHighest && v.Bandwidth > best.Bandwidth {
			best = v
		} else if !preferHighest && v.Bandwidth < best.Bandwidth {
			best = v
		}
	}
	return best, nil
}

// closestToBitrate returns the variant closest to the target bitrate (in kbps).
func closestToBitrate(variants []Variant, targetKbps int) Variant {
	best := variants[0]
	bestDiff := abs(best.BitrateKbps() - targetKbps)
	for _, v := range variants[1:] {
		diff := abs(v.BitrateKbps() - targetKbps)
		// On a tie, prefer the higher bandwidth so selection is deterministic
		// across CDN orderings and consistent with closestToHeight.
		if diff < bestDiff || (diff == bestDiff && v.Bandwidth > best.Bandwidth) {
			best = v
			bestDiff = diff
		}
	}
	return best
}

// closestToHeight returns the variant closest to the target height.
func closestToHeight(variants []Variant, targetHeight int) Variant {
	best := variants[0]
	bestDiff := abs(best.Height - targetHeight)
	for _, v := range variants[1:] {
		diff := abs(v.Height - targetHeight)
		if diff < bestDiff || (diff == bestDiff && v.Bandwidth > best.Bandwidth) {
			best = v
			bestDiff = diff
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
