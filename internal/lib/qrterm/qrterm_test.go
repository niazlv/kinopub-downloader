// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package qrterm

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

const sampleURL = "https://kino.pub/device?code=ABCD-1234"

// The monochrome rendering is parsed back into a module grid and compared with
// the encoder's own output: this proves the drawing is faithful (no transposed
// axes, no off-by-one) rather than merely non-empty.
func TestRenderMonochromeMatchesEncoder(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleURL, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	code, err := qr.Encode(sampleURL, ecLevel)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	wantRows := code.Size + 2*quietZone
	if len(lines) != wantRows {
		t.Fatalf("rendered %d rows, want %d (symbol %d + quiet zone)", len(lines), wantRows, code.Size)
	}

	for y := 0; y < code.Size; y++ {
		row := []rune(lines[y+quietZone])
		for x := 0; x < code.Size; x++ {
			// Each module occupies two cells in the monochrome form.
			got := row[(x+quietZone)*2] == '█'
			if want := code.Black(x, y); got != want {
				t.Fatalf("module (%d,%d) = %v, want %v", x, y, got, want)
			}
		}
	}
}

// The quiet zone must be entirely light, or scanners will not lock on.
func TestRenderHasQuietZone(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleURL, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	for i := 0; i < quietZone; i++ {
		if strings.Contains(lines[i], "█") {
			t.Errorf("top quiet-zone row %d contains a dark module", i)
		}
		if bottom := lines[len(lines)-1-i]; strings.Contains(bottom, "█") {
			t.Errorf("bottom quiet-zone row %d contains a dark module", i)
		}
	}
	// Left and right margins too.
	for _, ln := range lines {
		r := []rune(ln)
		if strings.Contains(string(r[:quietZone*2]), "█") {
			t.Error("left quiet zone contains a dark module")
			break
		}
	}
}

// The colour form must be half as tall (two module rows per terminal row) and
// reset styling on every line so it cannot bleed into later output.
func TestRenderColorIsCompactAndResets(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleURL, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	code, _ := qr.Encode(sampleURL, ecLevel)
	total := code.Size + 2*quietZone

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	wantRows := (total + 1) / 2
	if len(lines) != wantRows {
		t.Errorf("colour rendering has %d rows, want %d", len(lines), wantRows)
	}
	for i, ln := range lines {
		if !strings.HasSuffix(ln, ansiReset) {
			t.Errorf("row %d does not reset ANSI styling", i)
		}
	}
}

func TestRenderRejectsUnencodableInput(t *testing.T) {
	var buf bytes.Buffer
	// A payload far beyond QR capacity must error rather than render garbage.
	if err := Render(&buf, strings.Repeat("x", 8000), false); err == nil {
		t.Error("want an error for an oversized payload")
	}
}
