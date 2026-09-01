// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package termuxapi

import "os/exec"

// setProcessGroup is a no-op on Windows: the termux-api helpers only exist on
// Android, so this file exists solely to keep the package building.
func setProcessGroup(*exec.Cmd) {}

// killProcessGroup terminates the helper process.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
