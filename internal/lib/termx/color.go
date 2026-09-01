// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package termx

import (
	"fmt"
	"os"
	"strings"
)

// Additional ANSI attributes used by the CLI presentation layer.
const (
	Dim       = "\033[2m"
	Underline = "\033[4m"
)

// Mode says when ANSI sequences may be written to a stream. It is what the
// --color flag parses into; the decision itself is made by Enabled, which
// weighs the mode against the stream and the environment.
type Mode int

const (
	// ModeAuto colors only what a capable terminal is reading (the default).
	ModeAuto Mode = iota
	// ModeAlways colors unconditionally — for piping into a pager that
	// understands ANSI, e.g. `kinopub --color=always -h | less -R`.
	ModeAlways
	// ModeNever never colors, whatever the terminal or environment says.
	ModeNever
)

// String implements fmt.Stringer and flag.Value.
func (m Mode) String() string {
	switch m {
	case ModeAlways:
		return "always"
	case ModeNever:
		return "never"
	default:
		return "auto"
	}
}

// ParseMode converts a --color value to a Mode. The generous set of aliases
// exists because this flag is typed from memory more often than it is read
// from the help text.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto", "tty", "if-tty":
		return ModeAuto, nil
	case "always", "yes", "on", "force", "true", "1":
		return ModeAlways, nil
	case "never", "no", "off", "none", "false", "0":
		return ModeNever, nil
	}
	return ModeAuto, fmt.Errorf("invalid color mode %q: want auto, always or never", s)
}

// Enabled resolves the mode against the stream that would carry the output.
// On Windows it also switches the console into ANSI-interpreting mode, so a
// caller that asks this question is safe to emit escapes from then on.
func (m Mode) Enabled(f *os.File) bool {
	var on bool
	switch m {
	case ModeAlways:
		on = true
	case ModeNever:
		return false
	default:
		on = Supported(f)
	}
	if on {
		enableVirtualTerminal(f)
	}
	return on
}

// Supported reports whether f is a stream worth writing ANSI colors to.
//
// The environment gets the first and the last word, following the conventions
// other CLIs already taught users: NO_COLOR (no-color.org) turns color off
// even on a terminal, CLICOLOR_FORCE / FORCE_COLOR turn it on even through a
// pipe (CI logs that render ANSI want this), and CLICOLOR=0 turns it off.
// A "dumb" terminal — Emacs shell buffers, some CI runners — gets no escapes.
// Failing all of those, color follows the terminal.
func Supported(f *os.File) bool {
	if envSet("NO_COLOR") {
		return false
	}
	if envSet("CLICOLOR_FORCE") || envSet("FORCE_COLOR") {
		return true
	}
	if os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if t := os.Getenv("TERM"); t == "dumb" {
		return false
	}
	return IsTTY(f)
}

// envSet reports whether an on/off environment variable is present and asks
// for the behaviour it names. "0" is treated as "not set" so that exporting
// NO_COLOR=0 in a shell profile does not silently disable color forever.
func envSet(name string) bool {
	v, ok := os.LookupEnv(name)
	return ok && v != "" && v != "0"
}

// ---------------------------------------------------------------------------
// Styler
// ---------------------------------------------------------------------------

// Styler renders text with ANSI attributes, or returns it untouched when color
// is off. Passing one of these around means the call sites read the same in
// both modes: there is no `if colored` branch anywhere but here.
type Styler struct{ on bool }

// NewStyler returns a Styler that emits escapes only when on is true.
func NewStyler(on bool) Styler { return Styler{on: on} }

// StylerFor builds a Styler for the stream f under mode m.
func StylerFor(m Mode, f *os.File) Styler { return NewStyler(m.Enabled(f)) }

// Enabled reports whether this Styler emits ANSI sequences.
func (s Styler) Enabled() bool { return s.on }

// Wrap surrounds text with an ANSI attribute and a reset. Empty text is left
// alone so that a missing value does not turn into a stray escape pair.
//
// Do not nest Wrap calls: the inner reset would also end the outer attribute.
// Style the pieces separately and concatenate them instead.
func (s Styler) Wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return code + text + Reset
}

func (s Styler) Bold(text string) string      { return s.Wrap(Bold, text) }
func (s Styler) Dim(text string) string       { return s.Wrap(Dim, text) }
func (s Styler) Underline(text string) string { return s.Wrap(Underline, text) }
func (s Styler) Red(text string) string       { return s.Wrap(Red, text) }
func (s Styler) Green(text string) string     { return s.Wrap(Green, text) }
func (s Styler) Yellow(text string) string    { return s.Wrap(Yellow, text) }
func (s Styler) Blue(text string) string      { return s.Wrap(Blue, text) }
func (s Styler) Magenta(text string) string   { return s.Wrap(Magenta, text) }
func (s Styler) Cyan(text string) string      { return s.Wrap(Cyan, text) }
func (s Styler) Gray(text string) string      { return s.Wrap(Gray, text) }
func (s Styler) BoldRed(text string) string   { return s.Wrap(BoldRed, text) }
func (s Styler) BoldGreen(text string) string { return s.Wrap(BoldGreen, text) }
func (s Styler) BoldCyan(text string) string  { return s.Wrap(BoldCyan, text) }
