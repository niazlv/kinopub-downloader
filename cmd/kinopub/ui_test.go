// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// newTestPrinter builds a helpPrinter over a buffer with a fixed width, so the
// rendering under test does not depend on the terminal running the tests.
func newTestPrinter(colored bool) (*helpPrinter, *bytes.Buffer) {
	var buf bytes.Buffer
	return &helpPrinter{w: &buf, st: termx.NewStyler(colored), width: 80}, &buf
}

func TestDetectColorMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want termx.Mode
	}{
		{"no flags", []string{"https://kino.watch/item/view/1"}, termx.ModeAuto},
		{"equals form", []string{"--color=never", "url"}, termx.ModeNever},
		{"space form", []string{"--color", "always", "url"}, termx.ModeAlways},
		{"single dash", []string{"-color=always"}, termx.ModeAlways},
		{"no-color", []string{"--no-color", "url"}, termx.ModeNever},
		{"subcommand", []string{"doctor", "--no-color"}, termx.ModeNever},
		{"invalid value is ignored", []string{"--color=chartreuse"}, termx.ModeAuto},
		{"missing value is ignored", []string{"--color"}, termx.ModeAuto},
		{"last one wins", []string{"--color=always", "--color=never"}, termx.ModeNever},
		{"after a terminator", []string{"--", "--no-color"}, termx.ModeAuto},
		{"positional word", []string{"color", "always"}, termx.ModeAuto},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectColorMode(tt.args); got != tt.want {
				t.Errorf("detectColorMode(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestColorFlag_ParsesAndReports(t *testing.T) {
	defer setColorMode(termx.ModeAuto)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerColorFlags(fs)

	if err := fs.Parse([]string{"--color=never"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if colorMode != termx.ModeNever {
		t.Errorf("colorMode = %v, want never", colorMode)
	}
	if errStyle.Enabled() || outStyle.Enabled() {
		t.Error("--color=never must leave both stream stylers disabled")
	}
}

func TestColorFlag_RejectsUnknownValue(t *testing.T) {
	defer setColorMode(termx.ModeAuto)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerColorFlags(fs)

	if err := fs.Parse([]string{"--color=chartreuse"}); err == nil {
		t.Error("expected an error for an unknown --color value")
	}
}

func TestColorFlag_DefaultIsAutoWhateverTheModeInForce(t *testing.T) {
	// The help screen reports the flag's default, which must stay "auto" even
	// when the run itself was started with --color=always.
	defer setColorMode(termx.ModeAuto)
	setColorMode(termx.ModeAlways)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	registerColorFlags(fs)

	if got := fs.Lookup("color").DefValue; got != "auto" {
		t.Errorf("--color DefValue = %q, want %q", got, "auto")
	}
}

func TestNoColorFlag_NeedsNoValue(t *testing.T) {
	defer setColorMode(termx.ModeAuto)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerColorFlags(fs)
	var url string
	fs.StringVar(&url, "url", "", "")

	// A bool-like flag must not swallow the next argument.
	if err := fs.Parse([]string{"--no-color", "-url", "x"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if colorMode != termx.ModeNever {
		t.Errorf("colorMode = %v, want never", colorMode)
	}
	if url != "x" {
		t.Errorf("--url = %q, want %q", url, "x")
	}
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  []string
	}{
		{"short line", "hello world", 40, []string{"hello world"}},
		{"exact fit", "hello world again fi", 20, []string{"hello world again fi"}},
		{"wraps on spaces", "hello world again friend", 20, []string{"hello world again", "friend"}},
		{"keeps newlines", "one\ntwo", 40, []string{"one", "two"}},
		{"empty", "", 40, []string{""}},
		{"collapses runs of spaces", "a   b", 40, []string{"a b"}},
		{"narrow widths are floored, not honoured", "one two three", 4,
			[]string{"one two three"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrapText(tt.in, tt.width); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("wrapText(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
			}
		})
	}
}

func TestWrapText_LongWordIsNotSplit(t *testing.T) {
	url := "https://kino.watch/podcast/get/38290/0123456789abcdef0123456789abcdef"
	got := wrapText("see "+url, 30)
	if len(got) != 2 || got[1] != url {
		t.Errorf("wrapText kept %q, want the URL whole on its own line", got)
	}
}

func TestWrapText_MeasuresRunesNotBytes(t *testing.T) {
	// Em dashes and bullets are three bytes each; wrapping on bytes would
	// break these lines a third of the way early.
	got := wrapText("— — — — —", 9)
	if len(got) != 1 {
		t.Errorf("wrapText split %q into %d lines, want 1", "— — — — —", len(got))
	}
}

func TestFlags_PairsShorthandWithLongForm(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var output string
	fs.StringVar(&output, "output", "", "output directory path")
	fs.StringVar(&output, "o", "", "output directory path (shorthand)")

	h, buf := newTestPrinter(false)
	h.flags(fs)

	got := buf.String()
	if !strings.Contains(got, "-o, --output string") {
		t.Errorf("help should pair the shorthand with its long form, got:\n%s", got)
	}
	if strings.Contains(got, "(shorthand)") {
		t.Errorf("the paired line should carry the long form's description, got:\n%s", got)
	}
	if n := strings.Count(got, "--output"); n != 1 {
		t.Errorf("--output rendered %d times, want 1:\n%s", n, got)
	}
}

func TestFlags_ShowsDefaultsWorthStating(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var (
		container string
		out       string
		force     bool
		count     int
	)
	fs.StringVar(&container, "container", "mkv", "output container")
	fs.StringVar(&out, "out", "", "output path")
	fs.BoolVar(&force, "force", false, "force it")
	fs.IntVar(&count, "count", 0, "how many")

	h, buf := newTestPrinter(false)
	h.flags(fs)

	got := buf.String()
	if !strings.Contains(got, "(default mkv)") {
		t.Errorf("a meaningful default should be stated, got:\n%s", got)
	}
	if strings.Contains(got, "(default )") || strings.Contains(got, "(default false)") ||
		strings.Contains(got, "(default 0)") {
		t.Errorf("empty, false and zero defaults say nothing and should be omitted, got:\n%s", got)
	}
}

func TestFlags_NoEscapesWhenColorIsOff(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("output", "", "output directory path")

	h, buf := newTestPrinter(false)
	h.flags(fs)

	if strings.Contains(buf.String(), "\033[") {
		t.Errorf("plain rendering must not contain ANSI escapes, got %q", buf.String())
	}
}

func TestFlags_ColorsTheNames(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("output", "", "output directory path")

	h, buf := newTestPrinter(true)
	h.flags(fs)

	if !strings.Contains(buf.String(), termx.Cyan+"--output"+termx.Reset) {
		t.Errorf("flag names should be colored, got %q", buf.String())
	}
}

func TestGroupedFlags_ListsEveryFlagExactlyOnce(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("output", "", "output directory")
	fs.String("proxy", "", "proxy URL")
	fs.Bool("stray", false, "a flag no group mentions")

	h, buf := newTestPrinter(false)
	h.groupedFlags(fs, []flagGroup{
		{title: "Output:", names: []string{"output"}},
		{title: "Network:", names: []string{"proxy", "output"}}, // repeat must not duplicate
	})

	got := buf.String()
	for _, name := range []string{"--output", "--proxy", "--stray"} {
		if n := strings.Count(got, name); n != 1 {
			t.Errorf("%s rendered %d times, want 1:\n%s", name, n, got)
		}
	}
	// A flag no group claims must still reach the user.
	if !strings.Contains(got, "Other flags") {
		t.Errorf("an unlisted flag should be shown under a catch-all heading:\n%s", got)
	}
}

func TestGroupedFlags_SkipsEmptyGroups(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("output", "", "output directory")

	h, buf := newTestPrinter(false)
	h.groupedFlags(fs, []flagGroup{
		{title: "Output:", names: []string{"output"}},
		{title: "Nothing here:", names: []string{"absent"}},
	})

	if strings.Contains(buf.String(), "Nothing here:") {
		t.Errorf("a group with no present flags should be skipped:\n%s", buf.String())
	}
}

func TestMainFlagGroups_NoDuplicateNames(t *testing.T) {
	seen := make(map[string]string)
	for _, g := range mainFlagGroups {
		for _, name := range g.names {
			if other, ok := seen[name]; ok {
				t.Errorf("flag %q is listed in both %q and %q", name, other, g.title)
			}
			seen[name] = g.title
		}
	}
}

func TestLabeled_WrapsAndIndentsUnderTheLabel(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "labeled-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// A temp file is not a terminal, so the width is the 80-column default.
	labeled(f, "Warning:", "Warning:", strings.Repeat("word ", 30))

	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the message to wrap, got %q", data)
	}
	if !strings.HasPrefix(lines[0], "Warning: word") {
		t.Errorf("first line = %q, want it to start with the label", lines[0])
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, strings.Repeat(" ", len("Warning:")+1)+"word") {
			t.Errorf("continuation %q should be indented under the message", line)
		}
		if len(line) > 80 {
			t.Errorf("line %q is %d columns, want at most 80", line, len(line))
		}
	}
}

func TestHelpWidth_Bounds(t *testing.T) {
	// A non-terminal reports the 80-column default, which is inside the bounds.
	f, err := os.CreateTemp(t.TempDir(), "width-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if got := helpWidth(f); got != 80 {
		t.Errorf("helpWidth(regular file) = %d, want 80", got)
	}
}

func TestDashes(t *testing.T) {
	if got := dashes("o"); got != "-" {
		t.Errorf("dashes(%q) = %q, want %q", "o", got, "-")
	}
	if got := dashes("output"); got != "--" {
		t.Errorf("dashes(%q) = %q, want %q", "output", got, "--")
	}
}

// captureStderr runs fn with os.Stderr replaced by a pipe and returns what was
// written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	os.Stderr = orig
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestRun_HelpCoversEveryFlagInAGroup(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	out := captureStderr(t, func() {
		os.Args = []string{"kinopub", "--help"}
		if code := run(); code != 0 {
			t.Errorf("run(--help) = %d, want 0", code)
		}
	})

	// The catch-all heading only appears when a flag belongs to no group. It
	// is there so a new flag is never hidden — but the group it belongs in
	// should be chosen deliberately, which is what this test asks for.
	if strings.Contains(out, "Other flags") {
		t.Errorf("a flag is missing from mainFlagGroups; help rendered:\n%s", out)
	}

	for _, want := range []string{
		"Usage:", "Examples:", "Authentication:",
		"-o, --output", "-q, --quality", "-i, --interactive",
		"--color", "--no-color", "--browser-cookies", "--subs-only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help should mention %q, rendered:\n%s", want, out)
		}
	}
}

func TestRun_HelpIsPlainWhenStderrIsNotATerminal(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()
	defer setColorMode(termx.ModeAuto)

	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("FORCE_COLOR", "")
	setColorMode(termx.ModeAuto)

	out := captureStderr(t, func() {
		os.Args = []string{"kinopub", "--help"}
		run()
	})

	if strings.Contains(out, "\033[") {
		t.Error("help written to a pipe must carry no ANSI escapes")
	}
}
