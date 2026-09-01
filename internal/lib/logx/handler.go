// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package logx

import (
	"fmt"
	"io"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// ---------------------------------------------------------------------------
// ttyHandler — colored console output (Req 14.5)
// ---------------------------------------------------------------------------

// ttyHandler renders log records in the compact console layout, with ANSI
// colors per severity level when its Styler allows them.
// It respects verbosity filtering (Req 14.2-14.4).
type ttyHandler struct {
	w         io.Writer
	verbosity domain.Verbosity
	coord     *Coordinator
	st        termx.Styler
}

// NewTTYHandler creates a handler that writes colored output to w.
// Records below the verbosity threshold are suppressed.
// If coord is non-nil, writes are serialized through the coordinator.
func NewTTYHandler(w io.Writer, verbosity domain.Verbosity, coord *Coordinator) Handler {
	return NewConsoleHandler(w, verbosity, coord, termx.NewStyler(true))
}

// NewConsoleHandler creates a handler with the same compact layout as
// NewTTYHandler but with the coloring decided by st. A terminal whose user
// asked for no color still deserves the short timestamps and aligned levels,
// so the layout and the escapes are separate choices.
func NewConsoleHandler(w io.Writer, verbosity domain.Verbosity, coord *Coordinator, st termx.Styler) Handler {
	return &ttyHandler{w: w, verbosity: verbosity, coord: coord, st: st}
}

func (h *ttyHandler) Handle(rec Record) {
	if !ShouldDisplay(rec.Level, h.verbosity) {
		return
	}
	line := h.format(rec)
	if h.coord != nil {
		h.coord.WriteLog(line)
	} else {
		fmt.Fprint(h.w, line)
	}
}

func (h *ttyHandler) format(rec Record) string {
	var b strings.Builder

	// Timestamp in HH:MM:SS format.
	b.WriteString(h.st.Gray(rec.Time.Format("15:04:05")))
	b.WriteByte(' ')

	// Level with distinct color per severity.
	b.WriteString(h.st.Wrap(levelColor(rec.Level), fmt.Sprintf("%-5s", LevelString(rec.Level))))
	b.WriteByte(' ')

	// Component (if present).
	if rec.Component != "" {
		b.WriteString(h.st.Cyan("[" + rec.Component + "]"))
		b.WriteByte(' ')
	}

	// Message.
	b.WriteString(rec.Message)

	// Fields.
	for _, f := range rec.Fields {
		b.WriteByte(' ')
		b.WriteString(h.st.Gray(fmt.Sprintf("%s=%v", f.Key, f.Value)))
	}

	b.WriteByte('\n')
	return b.String()
}

// levelColor returns the ANSI color code for a given log level.
func levelColor(l domain.Level) string {
	switch l {
	case domain.LevelDebug:
		return termx.Gray
	case domain.LevelInfo:
		return termx.Blue
	case domain.LevelWarn:
		return termx.Yellow
	case domain.LevelError:
		return termx.BoldRed
	default:
		return termx.Reset
	}
}

// ---------------------------------------------------------------------------
// plainHandler — plain-text severity labels (Req 14.6)
// ---------------------------------------------------------------------------

// plainHandler renders log records with plain-text severity labels like
// [DEBUG], [INFO], [WARN], [ERROR]. Used when output is not a TTY.
// It respects verbosity filtering (Req 14.2-14.4).
type plainHandler struct {
	w         io.Writer
	verbosity domain.Verbosity
	coord     *Coordinator
}

// NewPlainHandler creates a handler that writes plain-text labeled output to w.
// Records below the verbosity threshold are suppressed.
// If coord is non-nil, writes are serialized through the coordinator.
func NewPlainHandler(w io.Writer, verbosity domain.Verbosity, coord *Coordinator) Handler {
	return &plainHandler{w: w, verbosity: verbosity, coord: coord}
}

func (h *plainHandler) Handle(rec Record) {
	if !ShouldDisplay(rec.Level, h.verbosity) {
		return
	}
	line := h.format(rec)
	if h.coord != nil {
		h.coord.WriteLog(line)
	} else {
		fmt.Fprint(h.w, line)
	}
}

func (h *plainHandler) format(rec Record) string {
	var b strings.Builder

	// Timestamp.
	b.WriteString(rec.Time.Format("2006-01-02 15:04:05"))
	b.WriteByte(' ')

	// Level label in brackets.
	b.WriteByte('[')
	b.WriteString(LevelString(rec.Level))
	b.WriteByte(']')
	b.WriteByte(' ')

	// Component (if present).
	if rec.Component != "" {
		b.WriteByte('[')
		b.WriteString(rec.Component)
		b.WriteByte(']')
		b.WriteByte(' ')
	}

	// Message.
	b.WriteString(rec.Message)

	// Fields.
	for _, f := range rec.Fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", f.Value))
	}

	b.WriteByte('\n')
	return b.String()
}

// ---------------------------------------------------------------------------
// fileHandler — writes ALL records regardless of verbosity (Req 13.7)
// ---------------------------------------------------------------------------

// fileHandler writes every record to a file sink at all levels, ignoring
// verbosity settings. This ensures the log file captures the complete history.
type fileHandler struct {
	w     io.Writer
	coord *Coordinator
}

// NewFileHandler creates a handler that writes all records to w regardless of
// verbosity. If coord is non-nil, writes are serialized through the coordinator.
func NewFileHandler(w io.Writer, coord *Coordinator) Handler {
	return &fileHandler{w: w, coord: coord}
}

func (h *fileHandler) Handle(rec Record) {
	line := h.format(rec)
	if h.coord != nil {
		h.coord.WriteLog(line)
	} else {
		fmt.Fprint(h.w, line)
	}
}

func (h *fileHandler) format(rec Record) string {
	var b strings.Builder

	// Full timestamp with date.
	b.WriteString(rec.Time.Format("2006-01-02 15:04:05.000"))
	b.WriteByte(' ')

	// Level label.
	b.WriteString(fmt.Sprintf("%-5s", LevelString(rec.Level)))
	b.WriteByte(' ')

	// Component (if present).
	if rec.Component != "" {
		b.WriteByte('[')
		b.WriteString(rec.Component)
		b.WriteByte(']')
		b.WriteByte(' ')
	}

	// Message.
	b.WriteString(rec.Message)

	// Fields.
	for _, f := range rec.Fields {
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", f.Value))
	}

	b.WriteByte('\n')
	return b.String()
}
