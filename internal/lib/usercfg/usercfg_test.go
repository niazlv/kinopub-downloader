// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package usercfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// useTempDir points the store at a temporary directory for one test, so nothing
// here touches the real ~/.config/kinopub.
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := configDir
	configDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { configDir = prev })
	return dir
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	useTempDir(t)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load with no file: %v", err)
	}
	if !got.IsEmpty() {
		t.Errorf("Load with no file = %+v, want empty settings", got)
	}
	if !got.NotificationsEnabled() {
		t.Error("notifications must default to enabled when nothing is saved")
	}
}

// Turning a setting off is a decision, so it has to survive the round trip
// rather than reading back as "unset" the way a plain bool field would.
func TestSaveLoadRoundTripOff(t *testing.T) {
	useTempDir(t)

	var want Settings
	want.SetNotifications(false)
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Notifications == nil {
		t.Fatal("notifications read back as unset after saving off")
	}
	if got.NotificationsEnabled() {
		t.Error("notifications read back as enabled after saving off")
	}
	if got.IsEmpty() {
		t.Error("IsEmpty must be false once a preference is saved")
	}
}

func TestSaveLoadRoundTripOn(t *testing.T) {
	useTempDir(t)

	var want Settings
	want.SetNotifications(true)
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Notifications == nil || !got.NotificationsEnabled() {
		t.Errorf("round trip of on = %+v, want an explicit true", got)
	}
}

// The file is documented as hand-editable, so its shape is part of the
// contract: one lower-case key holding a JSON boolean.
func TestSavedFileShape(t *testing.T) {
	dir := useTempDir(t)

	var s Settings
	s.SetNotifications(false)
	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("written file is not valid JSON: %v\n%s", err, data)
	}
	on, ok := raw["notifications"]
	if !ok {
		t.Fatalf("written file has no \"notifications\" key: %s", data)
	}
	if on != false {
		t.Errorf("notifications = %v, want false", on)
	}
	if len(raw) != 1 {
		t.Errorf("written file holds %d keys, want only the one that was set: %s", len(raw), data)
	}
}

// An empty settings object writes no keys at all, so hand-editing the file
// never has to reason about a key that is present but means nothing.
func TestSaveEmptyWritesNoKeys(t *testing.T) {
	dir := useTempDir(t)

	if err := Save(Settings{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if len(raw) != 0 {
		t.Errorf("empty settings wrote %v, want {}", raw)
	}
}

func TestLoadMalformedFileIsAnError(t *testing.T) {
	dir := useTempDir(t)

	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{notifications: off}"), 0600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a malformed file, got nil")
	}
}

// An empty file — a truncated write, or a user emptying it by hand — means the
// same as no file: nothing is set.
func TestLoadEmptyFile(t *testing.T) {
	dir := useTempDir(t)

	for _, content := range []string{"", "  \n\t", "{}"} {
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0600); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		got, err := Load()
		if err != nil {
			t.Fatalf("Load(%q): %v", content, err)
		}
		if !got.IsEmpty() {
			t.Errorf("Load(%q) = %+v, want empty settings", content, got)
		}
	}
}

func TestClearRemovesTheFileAndIsIdempotent(t *testing.T) {
	dir := useTempDir(t)

	var s Settings
	s.SetNotifications(false)
	if err := Save(s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName)); !os.IsNotExist(err) {
		t.Errorf("file still present after Clear (err = %v)", err)
	}
	// Clearing what is already gone is the end state the caller asked for.
	if err := Clear(); err != nil {
		t.Errorf("second Clear: %v", err)
	}
}

func TestSaveOverwritesPreviousValue(t *testing.T) {
	useTempDir(t)

	var off Settings
	off.SetNotifications(false)
	if err := Save(off); err != nil {
		t.Fatalf("Save off: %v", err)
	}
	var on Settings
	on.SetNotifications(true)
	if err := Save(on); err != nil {
		t.Fatalf("Save on: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.NotificationsEnabled() {
		t.Error("the second Save did not replace the first")
	}
}

// The file may hold a token or nothing secret at all, but it lives in a 0700
// directory and is written owner-only like everything else kinopub stores.
func TestSavedFilePermissions(t *testing.T) {
	dir := useTempDir(t)

	if err := Save(Settings{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestPathIsInsideTheConfigDir(t *testing.T) {
	dir := useTempDir(t)

	path, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if want := filepath.Join(dir, "config.json"); path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}
