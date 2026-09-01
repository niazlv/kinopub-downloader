// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package credstore

// legacySeeds returns seeds older versions may have encrypted with, tried only
// when the primary seed fails. macOS and Windows have always derived the key
// from a stable hardware identifier, so there is no legacy seed there.
func legacySeeds() [][]byte { return nil }

// chownToStoreOwner is a no-op off Linux: neither platform has the
// root-writes-another-user's-store problem that Termux's `su` creates.
func chownToStoreOwner(string) {}
