// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package termx

import "os"

// enableVirtualTerminal is a no-op everywhere but Windows: every other
// terminal this program runs on interprets ANSI sequences already.
func enableVirtualTerminal(*os.File) {}
