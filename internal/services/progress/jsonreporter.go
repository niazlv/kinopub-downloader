// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package progress

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// Event is one line of machine-readable progress. A host that draws its own
// interface — the desktop app, the Android shell reading the CLI's stdout —
// gets the same facts the terminal bars are drawn from, and nothing else.
//
// Type is one of: start, episode, track, progress, stop. The result of a run
// is not an Event: the host has it from the return value, or from the
// "result" line the CLI prints last.
type Event struct {
	Type string `json:"type"`
	// start
	Title   string      `json:"title,omitempty"`
	Total   int         `json:"total,omitempty"`
	Seasons map[int]int `json:"seasons,omitempty"`
	Done    int         `json:"done,omitempty"`
	// episode, track, progress
	Key     string `json:"key,omitempty"`
	Season  int    `json:"season,omitempty"`
	Episode int    `json:"episode,omitempty"`
	// episode: started | completed | failed | deferred
	State    string `json:"state,omitempty"`
	Error    string `json:"error,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
	// track: which stream and how far it is
	Track   string `json:"track,omitempty"`
	Percent int    `json:"percent,omitempty"`
	// progress: bytes and per-track breakdown, when the pipeline knows them
	Downloaded int64        `json:"downloaded,omitempty"`
	TotalBytes int64        `json:"totalBytes,omitempty"`
	Approx     bool         `json:"approx,omitempty"`
	Segments   int          `json:"segments,omitempty"`
	SegmentsOf int          `json:"segmentsOf,omitempty"`
	Tracks     []TrackEvent `json:"tracks,omitempty"`
}

// TrackEvent is one stream inside a progress event.
type TrackEvent struct {
	Label      string `json:"label"`
	Done       int    `json:"done"`
	Of         int    `json:"of"`
	Downloaded int64  `json:"downloaded"`
	Approx     int64  `json:"approx,omitempty"`
}

// EventReporter is a ProgressReporter that hands every change to a function.
// It also implements the byte, segment and HLS sinks, so the pipeline sends
// it the detailed numbers it would otherwise keep for the terminal.
type EventReporter struct {
	mu   sync.Mutex
	emit func(Event)
}

// NewEvents makes a reporter that calls fn for each event, in order, never
// concurrently.
func NewEvents(fn func(Event)) *EventReporter {
	return &EventReporter{emit: fn}
}

// NewJSON makes a reporter that writes one JSON object per line to w — the
// CLI's --progress-json, read by the Android shell.
func NewJSON(w io.Writer) *EventReporter {
	enc := json.NewEncoder(w)
	var mu sync.Mutex
	return NewEvents(func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		_ = enc.Encode(e)
	})
}

func (r *EventReporter) send(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.emit(e)
}

func key(k domain.EpisodeKey) (string, int, int) { return k.Label(), k.Season, k.Episode }

// trackName names a stream the way a host can show it: "video", "audio:2".
func trackName(t domain.TrackRef) string {
	switch t.Kind {
	case domain.TrackVideo:
		return "video"
	case domain.TrackAudio:
		return fmt.Sprintf("audio:%d", t.Index)
	default:
		return fmt.Sprintf("subtitle:%d", t.Index)
	}
}

func (r *EventReporter) Start(plan domain.SeriesPlan) {
	r.send(Event{Type: "start", Title: plan.Title, Total: plan.Total, Seasons: plan.Seasons, Done: plan.AlreadyCompleted})
}

func (r *EventReporter) EpisodeStarted(k domain.EpisodeKey) {
	l, s, e := key(k)
	r.send(Event{Type: "episode", Key: l, Season: s, Episode: e, State: "started"})
}

func (r *EventReporter) TrackProgress(k domain.EpisodeKey, track domain.TrackRef, percent int) {
	l, s, e := key(k)
	r.send(Event{Type: "track", Key: l, Season: s, Episode: e, Track: trackName(track), Percent: percent})
}

func (r *EventReporter) EpisodeCompleted(k domain.EpisodeKey) {
	l, s, e := key(k)
	r.send(Event{Type: "episode", Key: l, Season: s, Episode: e, State: "completed"})
}

func (r *EventReporter) EpisodeFailed(k domain.EpisodeKey, err error) {
	l, s, e := key(k)
	r.send(Event{Type: "episode", Key: l, Season: s, Episode: e, State: "failed", Error: err.Error()})
}

func (r *EventReporter) EpisodeDeferred(k domain.EpisodeKey, err error, attempts int) {
	l, s, e := key(k)
	r.send(Event{Type: "episode", Key: l, Season: s, Episode: e, State: "deferred", Error: err.Error(), Attempts: attempts})
}

func (r *EventReporter) Stop() { r.send(Event{Type: "stop"}) }

// ByteProgress implements domain.ByteProgressSink.
func (r *EventReporter) ByteProgress(k domain.EpisodeKey, downloaded, total int64) {
	l, s, e := key(k)
	ev := Event{Type: "progress", Key: l, Season: s, Episode: e, Downloaded: downloaded, TotalBytes: total}
	if total > 0 {
		ev.Percent = int(downloaded * 100 / total)
	}
	r.send(ev)
}

// SegmentProgress implements domain.SegmentProgressSink.
func (r *EventReporter) SegmentProgress(k domain.EpisodeKey, done, total int, downloaded, approxTotal int64) {
	l, s, e := key(k)
	ev := Event{Type: "progress", Key: l, Season: s, Episode: e, Segments: done, SegmentsOf: total,
		Downloaded: downloaded, TotalBytes: approxTotal, Approx: approxTotal > 0}
	if total > 0 {
		ev.Percent = done * 100 / total
	}
	r.send(ev)
}

// HLSProgress implements domain.HLSProgressSink.
func (r *EventReporter) HLSProgress(k domain.EpisodeKey, tracks []domain.TrackProgressInfo) {
	l, s, e := key(k)
	ev := Event{Type: "progress", Key: l, Season: s, Episode: e}
	var done, total int
	for _, t := range tracks {
		ev.Tracks = append(ev.Tracks, TrackEvent{Label: t.Label, Done: t.DoneSegments, Of: t.TotalSegments,
			Downloaded: t.DownloadedBytes, Approx: t.ApproxTotalBytes})
		done += t.DoneSegments
		total += t.TotalSegments
		ev.Downloaded += t.DownloadedBytes
		ev.TotalBytes += t.ApproxTotalBytes
	}
	ev.Segments, ev.SegmentsOf, ev.Approx = done, total, ev.TotalBytes > 0
	if total > 0 {
		ev.Percent = done * 100 / total
	}
	r.send(ev)
}
