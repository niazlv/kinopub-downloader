// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package credstore

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// storeHome returns the home directory whose credential store this process
// should use.
//
// Normally that is simply $HOME. The exception is running as root to read
// another app's private data — the documented way to save the kino.pub app's
// session — where $HOME points somewhere the ordinary user never looks:
//
//   - `sudo kinopub …` leaves HOME as root's, with SUDO_USER naming the caller;
//   - Termux's `su` sets HOME to $HOME/.suroot, a directory the unprivileged
//     Termux user does not read from.
//
// Writing there would save credentials the subsequent unprivileged run cannot
// see. So when running as root, resolve the invoking user's home instead.
// It deliberately never orphans a store that already exists. A desktop user who
// has always run under sudo has credentials under root's own home; redirecting
// them unconditionally would leave that file on disk but invisible. So the
// invoking user's home is preferred only when root's own home does not already
// hold a store, or when the invoking user's does.
func storeHome() (string, error) {
	ownHome, err := os.UserHomeDir()
	if os.Geteuid() != 0 {
		return ownHome, err
	}
	invoker, ok := invokingUserHome()
	return chooseStoreHome(ownHome, invoker, ok, hasStore), err
}

// chooseStoreHome decides, for a root process, whose home holds the store. It
// is separated from the environment lookups so the precedence — which must not
// orphan an existing store on upgrade — can be tested directly.
func chooseStoreHome(ownHome, invokerHome string, invokerKnown bool, exists func(string) bool) string {
	if !invokerKnown || invokerHome == "" {
		return ownHome
	}
	switch {
	case exists(invokerHome):
		// The user's own store is the one later unprivileged runs will read.
		return invokerHome
	case exists(ownHome):
		// Nothing on the user's side, but root already has credentials: this is
		// a long-standing `sudo kinopub` setup, so keep using them.
		return ownHome
	default:
		// Neither exists: write where the unprivileged run will look.
		return invokerHome
	}
}

// invokingUserHome resolves the home of the user who invoked this root process,
// when that can be determined unambiguously.
func invokingUserHome() (string, bool) {
	// sudo: SUDO_USER names the account that invoked us.
	if name := os.Getenv("SUDO_USER"); name != "" && name != "root" {
		if u, err := user.Lookup(name); err == nil && u.HomeDir != "" {
			return u.HomeDir, true
		}
	}

	// Termux: $PREFIX is preserved across `su`, and the user's home is its
	// sibling — /data/data/com.termux/files/{usr,home}. This holds regardless
	// of the .suroot HOME that su substitutes, and unlike /root it is the same
	// account's home, not a separate one.
	if prefix := os.Getenv("PREFIX"); prefix != "" {
		home := filepath.Join(filepath.Dir(prefix), "home")
		if fi, err := os.Stat(home); err == nil && fi.IsDir() {
			return home, true
		}
	}

	return "", false
}

// hasStore reports whether a credential file already exists under home.
func hasStore(home string) bool {
	if home == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(home, ".config", "kinopub", "credentials.enc"))
	return err == nil
}

// chownToStoreOwner hands a file written by root to the user who owns the
// store, so the later unprivileged run can read — and rewrite — it. It is a
// best-effort courtesy: any failure leaves the file owned by root, which the
// caller surfaces as an ordinary permission error if it matters.
func chownToStoreOwner(path string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, ok := storeOwnerIDs()
	if !ok {
		return
	}
	_ = os.Chown(path, uid, gid)
}

// storeOwnerIDs reports the uid/gid that should own the store: the sudo caller
// when known, otherwise whoever owns the resolved home directory (which is how
// the Termux case is identified, since su leaves no caller hint).
func storeOwnerIDs() (uid, gid int, ok bool) {
	if s := os.Getenv("SUDO_UID"); s != "" {
		if u, err := strconv.Atoi(s); err == nil {
			g := u
			if s := os.Getenv("SUDO_GID"); s != "" {
				if parsed, err := strconv.Atoi(s); err == nil {
					g = parsed
				}
			}
			return u, g, true
		}
	}

	home, err := storeHome()
	if err != nil {
		return 0, 0, false
	}
	fi, err := os.Stat(home)
	if err != nil {
		return 0, 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
