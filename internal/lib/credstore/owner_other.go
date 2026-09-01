// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package credstore

import "os"

// storeHome returns the home directory holding the credential store. Off Linux
// there is no root-reads-another-app's-data flow, so $HOME is always right.
func storeHome() (string, error) { return os.UserHomeDir() }
