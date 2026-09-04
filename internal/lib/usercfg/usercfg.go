// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package usercfg stores the preferences that belong to the person rather than
// to a single run, so a choice made once does not have to be repeated as a flag
// on every command — "I never want notifications" being the first of them.
//
// The file is plain JSON at ~/.config/kinopub/config.json, next to the
// encrypted credential store but deliberately not encrypted: nothing here is a
// secret, and the file is meant to be readable and editable by hand.
//
//	{
//	  "notifications": false
//	}
//
// A missing file, and a missing key inside it, both mean "no opinion" — the
// built-in default applies. Nothing here ever fails a run: a caller that cannot
// read the file falls back to the defaults.
package usercfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/lib/fsutil"
)

// fileName is the preferences file inside the config directory.
const fileName = "config.json"

// configDir resolves the directory the file lives in. It is a variable so tests
// can point the store at a temporary directory instead of the real home.
var configDir = credstore.ConfigDir

// Settings is the whole preferences file.
//
// Every field is a pointer so that "not set" stays distinguishable from "set to
// the zero value": turning notifications off is a decision, and it must survive
// a round trip through the file rather than reading back as an absent key.
type Settings struct {
	// Notifications controls the system notifications a download posts —
	// Termux notifications on Android, osascript/notify-send banners on the
	// desktop. Unset means enabled, which is the behaviour that predates this
	// file. The terminal progress display is not affected either way.
	Notifications *bool `json:"notifications,omitempty"`
}

// NotificationsEnabled reports whether a run should post system notifications.
func (s Settings) NotificationsEnabled() bool {
	return s.Notifications == nil || *s.Notifications
}

// SetNotifications records an explicit choice about notifications.
func (s *Settings) SetNotifications(on bool) { s.Notifications = &on }

// IsEmpty reports whether no preference is set at all, i.e. the file would say
// nothing that the defaults do not already say.
func (s Settings) IsEmpty() bool { return s.Notifications == nil }

// Dir returns the directory holding the preferences file.
func Dir() (string, error) { return configDir() }

// Path returns the full path of the preferences file. It is reported to the
// user by `kinopub config`, so they can edit or delete the file directly.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Load reads the stored preferences. A file that does not exist is not an
// error: it yields zero Settings, which means "every default applies".
//
// A file that exists but cannot be read or parsed is an error, and the caller
// decides what that is worth — the CLI warns and continues with the defaults
// rather than refusing to download over a typo in a config file.
func Load() (Settings, error) {
	path, err := Path()
	if err != nil {
		return Settings{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Settings{}, nil
		}
		return Settings{}, fmt.Errorf("read %s: %w", path, err)
	}
	s, err := decode(data)
	if err != nil {
		return Settings{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Save writes the preferences, creating the config directory if needed. The
// file is written atomically so an interrupted write cannot leave a truncated
// one behind that the next run would refuse to parse.
func Save(s Settings) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	path := filepath.Join(dir, fileName)

	data, err := encode(s)
	if err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(path, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	// Settings saved while running as root — the documented way to read the
	// mobile app's session — belong to the user who invoked us, not to root,
	// or the next unprivileged run cannot read what it just wrote.
	credstore.ChownToStoreOwner(dir)
	credstore.ChownToStoreOwner(path)
	return nil
}

// Clear removes the preferences file. A file that is not there is success:
// the end state the caller asked for is the one that already holds.
func Clear() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// decode parses the file's bytes. An empty file is treated as an empty object:
// it carries no preference, which is exactly what zero Settings means.
func decode(data []byte) (Settings, error) {
	var s Settings
	if len(trimSpace(data)) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// encode renders the settings as the indented JSON a human would have written,
// since the file is meant to be edited by hand.
func encode(s Settings) ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	return append(data, '\n'), nil
}

// trimSpace reports the content of data with surrounding whitespace removed,
// without pulling in strings conversions for a length check.
func trimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && isSpace(data[start]) {
		start++
	}
	for end > start && isSpace(data[end-1]) {
		end--
	}
	return data[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
