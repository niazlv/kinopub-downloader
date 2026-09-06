// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

// Job represents a single download task for one episode.
type Job struct {
	Episode     Episode
	Media       ResolvedMedia
	OutPath     string
	PosterPath  string // path to a local poster image file to embed as cover art (optional)
	SeriesTitle string // series title for container metadata (optional)
}

// JobOutcome records the result of a single job after all attempts.
type JobOutcome struct {
	Key       EpisodeKey
	Succeeded bool
	Err       error
	Attempts  int
}

// RunResult summarizes the outcome of a complete download run.
type RunResult struct {
	Total     int
	Succeeded int
	Failed    int
	Skipped   int
	Outcomes  []JobOutcome
	// Formats is set by a --list-formats run, which reports renditions instead
	// of downloading anything.
	Formats *FormatListing
	// Series is what the run learned about the source before downloading:
	// title, seasons, episodes. Set by --dry-run and --list-formats, so a host
	// can show the choice a run would make and let the user narrow it.
	Series *Series
}
