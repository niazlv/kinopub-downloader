// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package audiomenu provides an interactive, time-boxed CLI picker for the
// selectable tracks of an episode. It implements both domain.AudioChooser and
// domain.SubtitleChooser: the user is shown the available tracks and given a
// bounded window to pick which to keep. If they make no choice in time (or
// input is not a terminal), all tracks are kept.
//
// Audio and subtitle tracks share one implementation because they share one
// shape (domain.TrackInfo) and one interaction — only the wording differs.
package audiomenu

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// DefaultTimeout is the window the picker waits for input before defaulting to
// "keep all".
const DefaultTimeout = 90 * time.Second

// Chooser renders an interactive audio picker over in/out. It is constructed
// with the program's stdin and a writer for the menu (usually stderr).
type Chooser struct {
	in          io.Reader
	out         io.Writer
	interactive bool
	st          termx.Styler
}

// Option customizes a Chooser at construction.
type Option func(*Chooser)

// WithColor turns ANSI coloring of the menu on or off. It is off by default so
// that a caller which has not thought about the terminal writes plain text.
func WithColor(on bool) Option {
	return func(c *Chooser) { c.st = termx.NewStyler(on) }
}

// New builds a Chooser. interactive should be true only when in is a real TTY;
// when false, ChooseAudio immediately keeps all tracks without prompting.
func New(in io.Reader, out io.Writer, interactive bool, opts ...Option) *Chooser {
	c := &Chooser{in: in, out: out, interactive: interactive}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// rawTTY reports whether in is a real terminal whose file descriptor we can
// switch into raw mode for single-keystroke input (so TAB is seen immediately
// without waiting for Enter). When in is a pipe, a *strings.Reader (tests), or
// any non-terminal source, this returns (nil, false) and the caller falls back
// to buffered line reading.
func (c *Chooser) rawTTY() (*os.File, bool) {
	f, ok := c.in.(*os.File)
	if !ok {
		return nil, false
	}
	if !term.IsTerminal(int(f.Fd())) {
		return nil, false
	}
	return f, true
}

// ChooseAudio implements domain.AudioChooser. It prints the track list and
// reads a selection line from in, waiting at most timeout. The selection
// syntax mirrors the season/episode selectors: comma-separated 1-based indices
// and ranges, e.g. "1,3" or "1-2". "all" (or empty input / timeout) keeps
// everything; "none" is treated as "all" so the output always carries audio.
//
// Returned indices are 0-based (into tracks). A nil result means "keep all".
func (c *Chooser) ChooseAudio(tracks []domain.AudioTrackInfo, timeout time.Duration) ([]int, error) {
	return c.chooseTracks(tracks, timeout, trackKind{noun: "audio", fallbackLabel: "Audio"})
}

// ChooseSubtitles implements domain.SubtitleChooser. It behaves exactly like
// ChooseAudio — same selection syntax, same timeout, same defaults — over the
// episode's subtitle tracks.
func (c *Chooser) ChooseSubtitles(tracks []domain.SubtitleTrackInfo, timeout time.Duration) ([]int, error) {
	return c.chooseTracks(tracks, timeout, trackKind{noun: "subtitle", fallbackLabel: "Subtitles"})
}

// ChooseVideo implements domain.VideoChooser. Unlike the audio and subtitle
// pickers this accepts exactly one option, because a run muxes one video
// stream: a range or a comma-separated list is rejected rather than silently
// using its first entry.
//
// It returns the chosen 0-based index, or -1 to keep the automatic selection —
// which is also what an empty line, TAB, "auto", a timeout, a non-terminal
// input, or an unparseable answer produce.
func (c *Chooser) ChooseVideo(qualities []domain.VideoQualityInfo, timeout time.Duration) (int, error) {
	if len(qualities) <= 1 || !c.interactive {
		return -1, nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	fmt.Fprintf(c.out, "\n%s %s\n",
		c.st.Bold("Available video qualities"),
		c.st.Gray(fmt.Sprintf("(choose within %s, Enter or TAB = automatic):", timeout.Round(time.Second))))
	for i, q := range qualities {
		fmt.Fprintf(c.out, "  %s %s\n", c.st.Cyan(fmt.Sprintf("%d.", i+1)), q.Label())
	}
	fmt.Fprintf(c.out, "%s %s ",
		c.st.Bold("Selection"),
		c.st.Gray("(e.g. 1; Enter/TAB or 'auto' to keep automatic):"))

	line, ok := c.readSelection(timeout)
	if !ok {
		fmt.Fprintln(c.out, "\n"+c.st.Yellow("No selection — keeping the automatic quality."))
		return -1, nil
	}

	sel := strings.ToLower(strings.TrimSpace(line))
	switch sel {
	case "", "auto", "automatic", "optimal":
		fmt.Fprintln(c.out, c.st.Gray("Keeping the automatic quality."))
		return -1, nil
	}

	n, err := strconv.Atoi(sel)
	if err != nil || n < 1 || n > len(qualities) {
		fmt.Fprintf(c.out, "%s\n", c.st.Yellow("Invalid selection — keeping the automatic quality."))
		return -1, nil
	}
	fmt.Fprintf(c.out, "%s\n", c.st.Green(fmt.Sprintf("Using %s for every episode.", qualities[n-1].Label())))
	return n - 1, nil
}

// trackKind carries the only thing that differs between the audio and subtitle
// pickers: what to call the tracks.
type trackKind struct {
	noun          string // used in prompts, e.g. "audio" → "all audio tracks"
	fallbackLabel string // shown when a track has neither name nor language
}

// chooseTracks is the shared picker behind ChooseAudio and ChooseSubtitles.
func (c *Chooser) chooseTracks(tracks []domain.TrackInfo, timeout time.Duration, kind trackKind) ([]int, error) {
	if len(tracks) <= 1 || !c.interactive {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	c.render(tracks, timeout, kind)

	line, ok := c.readSelection(timeout)
	if !ok {
		fmt.Fprintf(c.out, "\n%s\n", c.st.Yellow(fmt.Sprintf("No selection — keeping all %s tracks.", kind.noun)))
		return nil, nil
	}

	sel := strings.ToLower(strings.TrimSpace(line))
	switch sel {
	case "", "all", "*", "none":
		fmt.Fprintf(c.out, "%s\n", c.st.Gray(fmt.Sprintf("Keeping all %s tracks.", kind.noun)))
		return nil, nil
	}

	idx, err := parseIndexSelection(sel, len(tracks))
	if err != nil {
		fmt.Fprintf(c.out, "%s\n", c.st.Yellow(fmt.Sprintf("Invalid selection (%v) — keeping all %s tracks.", err, kind.noun)))
		return nil, nil
	}
	if len(idx) == 0 {
		return nil, nil
	}
	return idx, nil
}

// render prints the prompt and track list.
func (c *Chooser) render(tracks []domain.TrackInfo, timeout time.Duration, kind trackKind) {
	fmt.Fprintf(c.out, "\n%s %s\n",
		c.st.Bold(fmt.Sprintf("Available %s tracks", kind.noun)),
		c.st.Gray(fmt.Sprintf("(choose within %s, Enter or TAB = all):", timeout.Round(time.Second))))
	for i, t := range tracks {
		label := t.Name
		if label == "" {
			label = t.Language
		}
		if label == "" {
			label = kind.fallbackLabel
		}
		if t.Language != "" && !strings.Contains(strings.ToLower(label), strings.ToLower(t.Language)) {
			label = fmt.Sprintf("%s [%s]", label, t.Language)
		}
		fmt.Fprintf(c.out, "  %s %s\n", c.st.Cyan(fmt.Sprintf("%d.", i+1)), label)
	}
	fmt.Fprintf(c.out, "%s %s ",
		c.st.Bold("Selection"),
		c.st.Gray("(e.g. 1,3 or 2-3; Enter/TAB or 'all' to keep everything):"))
}

// readSelection reads the user's selection within timeout. When in is a real
// terminal it switches to raw mode and reads keystrokes one at a time, so a
// single TAB (or Enter on an empty line) immediately accepts the default and
// auto-continues with all tracks — no Enter required. For non-terminal input
// (pipes, tests) it falls back to buffered line reading.
//
// It returns (line, true) when input was gathered, or ("", false) on timeout.
// A TAB keystroke is reported as an empty line, which the caller treats as
// "keep all".
func (c *Chooser) readSelection(timeout time.Duration) (string, bool) {
	if f, ok := c.rawTTY(); ok {
		return readKeysWithTimeout(f, c.out, timeout)
	}
	return readLineWithTimeout(c.in, timeout)
}

// readLineWithTimeout reads a single line from r, returning (line, true) if a
// line arrives within d, or ("", false) on timeout. The background read may
// outlive the timeout; that is acceptable for a short-lived CLI prompt.
func readLineWithTimeout(r io.Reader, d time.Duration) (string, bool) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		reader := bufio.NewReader(r)
		line, err := reader.ReadString('\n')
		ch <- result{line: line, err: err}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case res := <-ch:
		if res.err != nil && res.line == "" {
			return "", false
		}
		return res.line, true
	case <-timer.C:
		return "", false
	}
}

// readKeysWithTimeout reads keystrokes from a terminal in raw mode so that a
// single TAB or Enter is acted on immediately, without the user pressing Enter.
//
// Behavior:
//   - TAB              → auto-continue, returns ("", true) → caller keeps all.
//   - Enter (CR/LF)    → returns the line typed so far.
//   - Ctrl-C / Ctrl-D  → returns ("", false) → caller keeps all (treated like
//     "no selection").
//   - Backspace/DEL    → erases the last typed character.
//   - Printable bytes  → appended to the line and echoed.
//
// On timeout it returns ("", false). The terminal is always restored to its
// prior mode before returning.
func readKeysWithTimeout(f *os.File, out io.Writer, d time.Duration) (string, bool) {
	fd := int(f.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Can't enter raw mode — fall back to line-buffered reading.
		return readLineWithTimeout(f, d)
	}
	defer term.Restore(fd, oldState)

	type result struct {
		line string
		ok   bool
	}
	ch := make(chan result, 1)

	go func() {
		line, ok := decodeKeystrokes(f, out)
		ch <- result{line: line, ok: ok}
	}()

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case res := <-ch:
		// Move to a fresh line so subsequent output isn't appended to the
		// raw-mode prompt line.
		fmt.Fprint(out, "\r\n")
		return res.line, res.ok
	case <-timer.C:
		return "", false
	}
}

// decodeKeystrokes consumes one keystroke at a time from r (expected to be a
// terminal in raw mode) and resolves the user's intent, echoing printable
// input to out. It returns when a terminating key is seen:
//
//   - TAB              → ("", true)  — auto-continue with the default.
//   - Enter (CR/LF)    → (typed, true).
//   - Ctrl-C / Ctrl-D  → ("", false) — cancel / no selection.
//   - EOF or error     → (typed, len(typed) > 0).
//
// Backspace/DEL erases the last character; other control bytes are ignored.
// Splitting this out from terminal setup keeps the decode logic unit-testable
// without a real TTY.
func decodeKeystrokes(r io.Reader, out io.Writer) (string, bool) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			switch b := buf[0]; b {
			case '\t': // TAB → auto-continue with the default.
				return "", true
			case '\r', '\n': // Enter → submit what was typed.
				return string(line), true
			case 0x03, 0x04: // Ctrl-C / Ctrl-D → no selection.
				return "", false
			case 0x7f, '\b': // Backspace / DEL.
				if len(line) > 0 {
					line = line[:len(line)-1]
					// Erase the character visually: back up, space, back up.
					fmt.Fprint(out, "\b \b")
				}
			default:
				if b >= 0x20 { // Printable — echo and accumulate.
					line = append(line, b)
					out.Write([]byte{b})
				}
			}
		}
		if rerr != nil {
			return string(line), len(line) > 0
		}
	}
}

// parseIndexSelection parses a 1-based selection like "1,3-4" into sorted,
// de-duplicated 0-based indices. Out-of-range values are an error.
func parseIndexSelection(s string, n int) ([]int, error) {
	seen := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if i := strings.Index(part, "-"); i >= 0 {
			lo, err := strconv.Atoi(strings.TrimSpace(part[:i]))
			if err != nil {
				return nil, fmt.Errorf("bad range %q", part)
			}
			hi, err := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if err != nil {
				return nil, fmt.Errorf("bad range %q", part)
			}
			if lo > hi {
				lo, hi = hi, lo
			}
			for v := lo; v <= hi; v++ {
				if v < 1 || v > n {
					return nil, fmt.Errorf("index %d out of range [1,%d]", v, n)
				}
				seen[v-1] = true
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("bad index %q", part)
		}
		if v < 1 || v > n {
			return nil, fmt.Errorf("index %d out of range [1,%d]", v, n)
		}
		seen[v-1] = true
	}

	out := make([]int, 0, len(seen))
	for v := 0; v < n; v++ {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out, nil
}

// Verify Chooser satisfies both ports at compile time.
var (
	_ domain.AudioChooser    = (*Chooser)(nil)
	_ domain.SubtitleChooser = (*Chooser)(nil)
	_ domain.VideoChooser    = (*Chooser)(nil)
)
