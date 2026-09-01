// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package termx

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// vtOnce guards the console mode change per handle: the flag is sticky for the
// lifetime of the console, so setting it once is enough and repeated syscalls
// on every styled line would be waste.
var vtOnce sync.Map // windows.Handle → *sync.Once

// enableVirtualTerminal asks the Windows console attached to f to interpret
// ANSI escape sequences rather than print them literally. Windows Terminal and
// ConPTY do this on their own, but the classic conhost.exe still starts with
// the flag off, which is where uncolored-looking garbage comes from.
//
// Failure is silent by design: if the handle is a pipe or the console refuses,
// the caller's escapes are no worse off than before.
func enableVirtualTerminal(f *os.File) {
	if f == nil {
		return
	}
	h := windows.Handle(f.Fd())
	once, _ := vtOnce.LoadOrStore(h, &sync.Once{})
	once.(*sync.Once).Do(func() {
		var mode uint32
		if err := windows.GetConsoleMode(h, &mode); err != nil {
			return
		}
		if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
			return
		}
		_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	})
}
