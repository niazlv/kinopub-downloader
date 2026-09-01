// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package termuxapi

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the helper in a process group of its own.
//
// The termux-api helpers are shell scripts that broadcast an intent and then
// wait; the broadcast itself runs as a further child. Killing only the script
// leaves that grandchild alive, still holding the inherited descriptors — a
// wedged helper measured 62s to unwind even when its parent was killed after
// 12. Its own group lets the whole tree be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the helper's entire process group. The negative pid
// addresses the group, which is why setProcessGroup must have run first.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or setpgid may not have taken effect;
		// falling back to the direct child is still better than nothing.
		_ = cmd.Process.Kill()
	}
}
