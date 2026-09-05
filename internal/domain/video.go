// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"fmt"
	"time"
)

// VideoQualityInfo describes one video rendition offered to the user by the
// interactive quality picker.
//
// Quality carries the selector that reproduces this option ("1080p-h265"), so a
// choice made in the menu travels through the rest of the run as an ordinary
// --quality value. Nothing downstream needs to know a menu was involved.
type VideoQualityInfo struct {
	// Index is the position within the offered list (0-based). ChooseVideo
	// returns one of these.
	Index int
	// Height is the vertical resolution, e.g. 1080.
	Height int
	// Width is the horizontal resolution, e.g. 1920; zero when the source did not say.
	Width int
	// Codec is "h264" or "h265".
	Codec string
	// BitrateKbps is the declared bandwidth in kbit/s.
	BitrateKbps int
	// FPS is the declared frame rate; zero when the master does not say.
	FPS float64
	// Quality is the selector equivalent to picking this option.
	Quality Quality
}

// Label renders the option as shown in the picker, e.g. "1080p/h265 (2500 kbps)".
func (q VideoQualityInfo) Label() string {
	return fmt.Sprintf("%dp/%s (%d kbps)", q.Height, q.Codec, q.BitrateKbps)
}

// VideoQualitySelector builds the --quality selector that reproduces a given
// height and codec. The codec is always named so the choice is unambiguous:
// without it, a bare "1080p" would prefer h264 and could resolve to a different
// rendition than the one the user pointed at.
func VideoQualitySelector(height int, codec string) Quality {
	return Quality(fmt.Sprintf("%dp-%s", height, codec))
}

// VideoChooser presents the available video qualities and returns the index of
// the one to use for the whole run.
//
// Unlike the audio and subtitle pickers this is a single choice: a run muxes one
// video stream. Implementations may block for input up to a timeout; on timeout,
// non-interactive input, or an explicit decline they return -1, meaning "keep
// the automatic selection".
type VideoChooser interface {
	ChooseVideo(qualities []VideoQualityInfo, timeout time.Duration) (int, error)
}
