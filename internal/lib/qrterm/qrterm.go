// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package qrterm renders a QR code as terminal text, for showing a device-
// authorization link that the user scans with a phone.
package qrterm

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"rsc.io/qr"
)

// quietZone is the number of light modules the QR spec requires around the
// symbol. Without it many scanners refuse to lock on, so it is not optional
// decoration — it is part of a valid code.
const quietZone = 4

// ANSI colours chosen so the symbol carries its own light background instead of
// inheriting the terminal theme. A dark-themed terminal would otherwise render
// an inverted code, which many phone scanners will not read.
const (
	ansiReset  = "\x1b[0m"
	fgDark     = "\x1b[30m"  // black foreground
	fgLight    = "\x1b[97m"  // bright white foreground
	bgDark     = "\x1b[40m"  // black background
	bgLight    = "\x1b[107m" // bright white background
	upperHalf  = "▀"         // foreground paints the top half, background the bottom
	blockFull  = "██"        // monochrome fallback: two cells wide to stay square
	blockEmpty = "  "

	// ecLevel is the error-correction level. L is the lowest, keeping the symbol
	// small; the URL is short and the screen is a clean source, so the extra
	// redundancy of higher levels only makes the code harder to fit in a window.
	ecLevel = qr.L
)

// Render writes text as a scannable QR code.
//
// With colour it uses one terminal row per two module rows via the upper-half
// block, setting an explicit foreground/background per cell — compact and
// theme-independent. Without colour it falls back to two-character blocks,
// which is twice as tall and relies on the terminal having a light background.
func Render(w io.Writer, text string, useColor bool) error {
	code, err := qr.Encode(text, ecLevel)
	if err != nil {
		return fmt.Errorf("encode QR: %w", err)
	}

	bw := bufio.NewWriter(w)
	defer bw.Flush()

	size := code.Size
	total := size + 2*quietZone

	// dark reports whether the module at padded coordinates is dark, treating
	// everything in the quiet zone as light.
	dark := func(x, y int) bool {
		mx, my := x-quietZone, y-quietZone
		if mx < 0 || my < 0 || mx >= size || my >= size {
			return false
		}
		return code.Black(mx, my)
	}

	if !useColor {
		for y := 0; y < total; y++ {
			var b strings.Builder
			for x := 0; x < total; x++ {
				if dark(x, y) {
					b.WriteString(blockFull)
				} else {
					b.WriteString(blockEmpty)
				}
			}
			if _, err := fmt.Fprintln(bw, b.String()); err != nil {
				return err
			}
		}
		return nil
	}

	// Two module rows per terminal row: the glyph's top half takes the
	// foreground colour, its bottom half the background colour.
	for y := 0; y < total; y += 2 {
		var b strings.Builder
		for x := 0; x < total; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			if top {
				b.WriteString(fgDark)
			} else {
				b.WriteString(fgLight)
			}
			if bottom {
				b.WriteString(bgDark)
			} else {
				b.WriteString(bgLight)
			}
			b.WriteString(upperHalf)
		}
		b.WriteString(ansiReset)
		if _, err := fmt.Fprintln(bw, b.String()); err != nil {
			return err
		}
	}
	return nil
}
