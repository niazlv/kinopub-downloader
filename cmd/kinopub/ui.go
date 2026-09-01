// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Presentation layer for everything the CLI writes itself: the help screens,
// the labelled diagnostics, and the color decision behind them. The rule is
// that a call site never asks whether color is on — it asks a Styler for
// styled text and gets plain text back when it is not.

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// The resolved color decision, made once at startup and used by everything the
// CLI prints. stdout and stderr are decided separately because they are
// redirected separately: `kinopub update > log` should keep coloring the
// diagnostics on the terminal while writing a clean file.
var (
	colorMode = termx.ModeAuto
	errStyle  = termx.NewStyler(false)
	outStyle  = termx.NewStyler(false)
)

// setColorMode records the mode and re-resolves both stream stylers.
func setColorMode(m termx.Mode) {
	colorMode = m
	errStyle = termx.StylerFor(m, os.Stderr)
	outStyle = termx.StylerFor(m, os.Stdout)
}

// colorFlag is the --color flag: it applies the mode as soon as it is parsed,
// so a usage or error message printed later in the same parse is already
// styled the way the user asked for. It keeps its own copy of the mode so that
// the help screen reports the flag's default rather than the mode in force.
type colorFlag struct{ mode termx.Mode }

func (c *colorFlag) String() string { return c.mode.String() }

func (c *colorFlag) Set(v string) error {
	m, err := termx.ParseMode(v)
	if err != nil {
		return err
	}
	c.mode = m
	setColorMode(m)
	return nil
}

// noColorFlag is the --no-color spelling of --color=never, kept because it is
// what users type out of habit from other tools.
type noColorFlag struct{}

func (noColorFlag) String() string { return "" }

func (noColorFlag) IsBoolFlag() bool { return true }

func (noColorFlag) Set(string) error {
	setColorMode(termx.ModeNever)
	return nil
}

// noColorFlag has no value of its own, so nothing about it belongs in the
// "(default …)" note.
func (noColorFlag) isZeroDefault() bool { return true }

// registerColorFlags adds --color and --no-color to a subcommand's flag set.
// Every flag set needs them: the flag package rejects an unknown flag, so a
// subcommand without these would fail on a --color the user meant globally.
func registerColorFlags(fs *flag.FlagSet) {
	fs.Var(&colorFlag{}, "color", "`when` to color output: auto (a terminal only), always, or never")
	fs.Var(noColorFlag{}, "no-color", "never color output (same as --color=never)")
}

// detectColorMode reads the color flags out of the raw arguments before any
// flag set exists. Messages can be printed before parsing — an unknown flag,
// a missing URL, a subcommand with no flag set at all — and those should obey
// --color too. The real parse re-applies the flag afterwards, so a mistake
// here only ever affects styling, never behaviour.
func detectColorMode(args []string) termx.Mode {
	mode := termx.ModeAuto
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch strings.TrimLeft(name, "-") {
		case "color":
			if name == arg && !strings.HasPrefix(arg, "-") {
				continue // a positional argument that happens to be "color"
			}
			if !hasValue {
				if i+1 >= len(args) {
					continue
				}
				value = args[i+1]
				i++
			}
			if m, err := termx.ParseMode(value); err == nil {
				mode = m
			}
		case "no-color":
			if strings.HasPrefix(arg, "-") {
				mode = termx.ModeNever
			}
		}
	}
	return mode
}

// ---------------------------------------------------------------------------
// Labelled diagnostics
// ---------------------------------------------------------------------------

// errorf prints "Error: …" to stderr.
func errorf(format string, args ...any) {
	labeled(os.Stderr, errStyle.BoldRed("Error:"), "Error:", fmt.Sprintf(format, args...))
}

// warnf prints "Warning: …" to stderr.
func warnf(format string, args ...any) {
	labeled(os.Stderr, errStyle.Yellow("Warning:"), "Warning:", fmt.Sprintf(format, args...))
}

// notef prints "Note: …" to stderr — something the run did differently than
// asked, which is not a problem in itself.
func notef(format string, args ...any) {
	labeled(os.Stderr, errStyle.Cyan("Note:"), "Note:", fmt.Sprintf(format, args...))
}

// labeled writes a message behind a label, wrapping it to the terminal and
// indenting the continuation lines under the first word. plain is the label
// without escapes: its length is what the layout is measured against, since
// ANSI sequences occupy no columns.
func labeled(w *os.File, label, plain, text string) {
	width := helpWidth(w)
	indent := strings.Repeat(" ", utf8.RuneCountInString(plain)+1)
	for i, line := range wrapText(text, width-utf8.RuneCountInString(plain)-1) {
		if i == 0 {
			fmt.Fprintf(w, "%s %s\n", label, line)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
	}
}

// ---------------------------------------------------------------------------
// Help screens
// ---------------------------------------------------------------------------

// helpPrinter renders one help screen. It exists so the help text reads as
// structure — sections, commands, examples, flags — rather than as three dozen
// Fprintf calls that each have to remember the color scheme.
type helpPrinter struct {
	w     io.Writer
	st    termx.Styler
	width int
}

// newHelpPrinter builds a printer for f, styled and wrapped for that stream.
func newHelpPrinter(f *os.File, st termx.Styler) *helpPrinter {
	return &helpPrinter{w: f, st: st, width: helpWidth(f)}
}

// helpWidth is the column budget for text written to f: the terminal width,
// capped so that a very wide window does not produce unreadably long lines.
func helpWidth(f *os.File) int {
	w := termx.Width(f)
	if w > 96 {
		w = 96
	}
	if w < 40 {
		w = 40
	}
	return w
}

// title renders the first line: name, version, and one-line description. The
// version is omitted when empty — a subcommand screen names no version.
func (h *helpPrinter) title(name, version, tagline string) {
	if version == "" {
		fmt.Fprintf(h.w, "%s — %s\n", h.st.Bold(name), tagline)
		return
	}
	fmt.Fprintf(h.w, "%s %s — %s\n", h.st.Bold(name), h.st.Gray(version), tagline)
}

// section starts a titled block, preceded by a blank line.
func (h *helpPrinter) section(title string) {
	fmt.Fprintf(h.w, "\n%s\n", h.st.Bold(title))
}

// blank writes an empty line.
func (h *helpPrinter) blank() {
	fmt.Fprintln(h.w)
}

// text writes a wrapped paragraph at the left margin.
func (h *helpPrinter) text(format string, args ...any) {
	for _, line := range wrapText(sprint(format, args...), h.width) {
		fmt.Fprintln(h.w, line)
	}
}

// line writes a single line exactly as given, without wrapping — for anything
// whose layout carries meaning.
func (h *helpPrinter) line(format string, args ...any) {
	fmt.Fprintln(h.w, sprint(format, args...))
}

// sprint formats only when there is something to substitute, so help text
// containing a literal % is printed as written rather than as %!(NOVERB).
func sprint(format string, args ...any) string {
	if len(args) == 0 {
		return format
	}
	return fmt.Sprintf(format, args...)
}

// command is one entry of a command or syntax list.
type command struct {
	name string // e.g. "kinopub login [flags]"
	desc string // e.g. "save authentication credentials"; may be empty
}

// commands renders a command list with the descriptions aligned.
func (h *helpPrinter) commands(entries ...command) {
	width := 0
	for _, e := range entries {
		if e.desc == "" {
			continue
		}
		if n := utf8.RuneCountInString(e.name); n > width {
			width = n
		}
	}
	for _, e := range entries {
		if e.desc == "" {
			h.line("  %s", h.st.Cyan(e.name))
			continue
		}
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(e.name))
		h.line("  %s%s  %s", h.st.Cyan(e.name), pad, h.st.Gray("— "+e.desc))
	}
}

// bullet renders "• label value", with the value picked out. Used for the
// lists of accepted URL forms and doctor checks.
func (h *helpPrinter) bullet(label, value string) {
	if value == "" {
		h.line("  %s %s", h.st.Cyan("•"), label)
		return
	}
	h.line("  %s %s %s", h.st.Cyan("•"), label, h.st.Cyan(value))
}

// bulletCont continues the previous bullet's value on its own line, aligned
// under it. pad is the width of that bullet's label.
func (h *helpPrinter) bulletCont(pad int, value string) {
	h.line("%s%s", strings.Repeat(" ", pad+5), h.st.Cyan(value))
}

// step renders a numbered instruction.
func (h *helpPrinter) step(n int, text string) {
	h.line("    %s %s", h.st.Cyan(fmt.Sprintf("%d.", n)), text)
}

// example renders a "# what this does" comment above the command it describes.
func (h *helpPrinter) example(comment, cmd string) {
	h.line("  %s", h.st.Gray("# "+comment))
	name, rest, _ := strings.Cut(cmd, " ")
	h.line("  %s %s", h.st.Bold(name), rest)
	h.blank()
}

// flagColumn is where flag descriptions start when the flag itself is short
// enough to leave room for them.
const flagColumn = 26

// flags renders a flag set: names in color, value types dimmed, descriptions
// wrapped into a second column. It replaces flag.PrintDefaults, whose
// two-lines-per-flag output makes a help screen with three dozen flags very
// hard to scan.
func (h *helpPrinter) flags(fs *flag.FlagSet) {
	entries, _ := collectFlags(fs)
	h.flagList(entries)
}

// flagGroup names a block of related flags within a help screen.
type flagGroup struct {
	title string
	names []string // flag names, in the order they should be listed
}

// groupedFlags renders the flag set in titled blocks. Flags the groups do not
// mention are listed last under "Other flags", so a flag added later shows up
// in the help even if whoever added it never touched this list.
func (h *helpPrinter) groupedFlags(fs *flag.FlagSet, groups []flagGroup) {
	entries, byName := collectFlags(fs)

	shown := make(map[int]bool)
	for _, g := range groups {
		var list []flagEntry
		for _, name := range g.names {
			i, ok := byName[name]
			if !ok || shown[i] {
				continue
			}
			shown[i] = true
			list = append(list, entries[i])
		}
		if len(list) == 0 {
			continue
		}
		h.section(g.title)
		h.flagList(list)
	}

	var rest []flagEntry
	for i, e := range entries {
		if !shown[i] {
			rest = append(rest, e)
		}
	}
	if len(rest) > 0 {
		h.section("Other flags")
		h.flagList(rest)
	}
}

// flagEntry is one line of a flag list: a flag plus, when the same variable was
// registered twice, its single-letter shorthand.
type flagEntry struct {
	long  *flag.Flag
	short *flag.Flag // may be nil
}

// collectFlags folds a flag set into display entries, pairing each shorthand
// with its long form, and maps every name to the entry that shows it.
//
// The pairing is found by identity rather than by naming convention: a
// shorthand is registered against the same variable as its long form
// (fs.StringVar(&output, "output", …) and fs.StringVar(&output, "o", …)), so
// the two flag.Values hold the same pointer. That cannot go stale the way a
// hand-maintained list of pairs would.
func collectFlags(fs *flag.FlagSet) ([]flagEntry, map[string]int) {
	var entries []flagEntry
	byName := make(map[string]int)
	byTarget := make(map[uintptr]int)

	fs.VisitAll(func(f *flag.Flag) {
		target := valueTarget(f)
		if i, ok := byTarget[target]; ok && target != 0 {
			e := &entries[i]
			// The shorter name is the shorthand, whichever was declared first.
			if utf8.RuneCountInString(f.Name) < utf8.RuneCountInString(e.long.Name) {
				e.short = f
			} else if e.short == nil {
				e.short = e.long
				e.long = f
			}
			byName[f.Name] = i
			return
		}
		entries = append(entries, flagEntry{long: f})
		byName[f.Name] = len(entries) - 1
		if target != 0 {
			byTarget[target] = len(entries) - 1
		}
	})
	return entries, byName
}

// valueTarget returns the address the flag writes to, or 0 when the flag.Value
// is not a pointer (which no flag in this program uses, but a future one might).
func valueTarget(f *flag.Flag) uintptr {
	v := reflect.ValueOf(f.Value)
	if !v.IsValid() || v.Kind() != reflect.Pointer {
		return 0
	}
	return v.Pointer()
}

// flagList renders one block of flags.
func (h *helpPrinter) flagList(entries []flagEntry) {
	for _, e := range entries {
		f := e.long
		valueType, usage := flag.UnquoteUsage(f)
		if def := defaultNote(f); def != "" {
			usage += " " + def
		}

		names := dashes(f.Name) + f.Name
		if e.short != nil {
			names = dashes(e.short.Name) + e.short.Name + ", " + names
		}
		plainWidth := 2 + utf8.RuneCountInString(names)
		styled := "  " + h.st.Cyan(names)
		if valueType != "" {
			plainWidth += 1 + utf8.RuneCountInString(valueType)
			styled += " " + h.st.Dim(valueType)
		}

		lines := wrapText(usage, h.width-flagColumn)
		if len(lines) == 0 {
			h.line("%s", styled)
			continue
		}
		// A flag too wide for the column gets its description on the next
		// line rather than pushing the whole column out for everyone else.
		if plainWidth+2 > flagColumn {
			h.line("%s", styled)
			for _, line := range lines {
				h.line("%s%s", strings.Repeat(" ", flagColumn), line)
			}
			continue
		}
		h.line("%s%s%s", styled, strings.Repeat(" ", flagColumn-plainWidth), lines[0])
		for _, line := range lines[1:] {
			h.line("%s%s", strings.Repeat(" ", flagColumn), line)
		}
	}
}

// dashes picks the prefix a flag is normally typed with: one dash for the
// single-letter shorthands, two for the long names. The flag package accepts
// either spelling for both.
func dashes(name string) string {
	if utf8.RuneCountInString(name) == 1 {
		return "-"
	}
	return "--"
}

// defaultNote renders a flag's default value the way flag.PrintDefaults does,
// but only when the default is worth stating: a false boolean, an empty string
// or a zero number says nothing the flag's description does not.
func defaultNote(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0", "[]", "map[]":
		return ""
	}
	return fmt.Sprintf("(default %s)", f.DefValue)
}

// ---------------------------------------------------------------------------
// Text wrapping
// ---------------------------------------------------------------------------

// wrapText breaks s into lines no wider than width, on spaces only — a word
// longer than the budget (a URL, a cookie) is left whole and allowed to
// overflow rather than being cut in half. Existing newlines are kept.
//
// It measures runes, not bytes: the help text is full of em dashes and bullets.
func wrapText(s string, width int) []string {
	if width < 20 {
		width = 20
	}
	var out []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		lineLen := utf8.RuneCountInString(line)
		for _, word := range words[1:] {
			wordLen := utf8.RuneCountInString(word)
			if lineLen+1+wordLen > width {
				out = append(out, line)
				line, lineLen = word, wordLen
				continue
			}
			line += " " + word
			lineLen += 1 + wordLen
		}
		out = append(out, line)
	}
	return out
}

// ---------------------------------------------------------------------------
// Flag groups
// ---------------------------------------------------------------------------

// mainFlagGroups lays the download command's flags out by what they do, rather
// than in the order they happen to be declared. Anything missing from this list
// still appears — see groupedFlags — so the help never hides a flag.
var mainFlagGroups = []flagGroup{
	{title: "Output:", names: []string{
		"o", "output", "container", "force", "dry-run",
	}},
	{title: "What to download:", names: []string{
		"q", "quality", "seasons", "episodes", "audio", "subs", "subs-external", "subs-only",
	}},
	{title: "Interactive selection:", names: []string{
		"i", "interactive", "video-menu", "audio-menu", "subs-menu",
	}},
	{title: "Site, network and authentication:", names: []string{
		"site", "no-domain-rewrite", "proxy", "cookie", "user-agent", "header",
		"browser-cookies", "feed-file",
	}},
	{title: "Transfer and ffmpeg:", names: []string{
		"c", "concurrency", "no-chunked", "ffmpeg", "ffmpeg-args", "x",
	}},
	{title: "Output and diagnostics:", names: []string{
		"v", "verbosity", "log-file", "color", "no-color", "version",
	}},
}
