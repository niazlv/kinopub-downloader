// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/app/kinopub"
	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/desktopnotify"
	"github.com/niazlv/kinopub-downloader/internal/lib/termuxapi"
	"github.com/niazlv/kinopub-downloader/internal/lib/usercfg"
)

// --- parseOnOff ---

func TestParseOnOff(t *testing.T) {
	on := []string{"on", "ON", "true", "yes", "y", "1", "enable", "enabled", "  on  "}
	off := []string{"off", "OFF", "false", "no", "n", "0", "disable", "disabled", " off "}

	for _, v := range on {
		got, err := parseOnOff(v)
		if err != nil {
			t.Errorf("parseOnOff(%q): unexpected error %v", v, err)
			continue
		}
		if !got {
			t.Errorf("parseOnOff(%q) = false, want true", v)
		}
	}
	for _, v := range off {
		got, err := parseOnOff(v)
		if err != nil {
			t.Errorf("parseOnOff(%q): unexpected error %v", v, err)
			continue
		}
		if got {
			t.Errorf("parseOnOff(%q) = true, want false", v)
		}
	}
}

func TestParseOnOffRejectsNonsense(t *testing.T) {
	for _, v := range []string{"", "maybe", "onn", "2", "of", "да"} {
		if _, err := parseOnOff(v); err == nil {
			t.Errorf("parseOnOff(%q): expected an error, got nil", v)
		}
	}
}

// onOff must produce something parseOnOff accepts, or `config get` would print
// a value `config set` refuses.
func TestOnOffRoundTrips(t *testing.T) {
	for _, want := range []bool{true, false} {
		got, err := parseOnOff(onOff(want))
		if err != nil {
			t.Fatalf("parseOnOff(onOff(%v)): %v", want, err)
		}
		if got != want {
			t.Errorf("round trip of %v = %v", want, got)
		}
	}
}

// --- the settings table ---

func TestLookupSettingKey(t *testing.T) {
	if _, ok := lookupSettingKey("notifications"); !ok {
		t.Error("notifications is not in the settings table")
	}
	if _, ok := lookupSettingKey("  Notifications "); !ok {
		t.Error("key lookup must ignore case and surrounding space")
	}
	if _, ok := lookupSettingKey("nonsense"); ok {
		t.Error("unknown key reported as known")
	}
}

func TestNotificationsKeyGetSetUnset(t *testing.T) {
	key, ok := lookupSettingKey("notifications")
	if !ok {
		t.Fatal("notifications key missing")
	}

	// Nothing saved: the default, reported as not explicit.
	var s usercfg.Settings
	if value, explicit := key.get(s); value != "on" || explicit {
		t.Errorf("unset = (%q, %v), want (\"on\", false)", value, explicit)
	}

	if err := key.set(&s, "off"); err != nil {
		t.Fatalf("set off: %v", err)
	}
	if value, explicit := key.get(s); value != "off" || !explicit {
		t.Errorf("after set off = (%q, %v), want (\"off\", true)", value, explicit)
	}

	// An explicit "on" is still an explicit choice, not a return to the default.
	if err := key.set(&s, "on"); err != nil {
		t.Fatalf("set on: %v", err)
	}
	if value, explicit := key.get(s); value != "on" || !explicit {
		t.Errorf("after set on = (%q, %v), want (\"on\", true)", value, explicit)
	}

	key.unset(&s)
	if value, explicit := key.get(s); value != "on" || explicit {
		t.Errorf("after unset = (%q, %v), want (\"on\", false)", value, explicit)
	}
	if !s.IsEmpty() {
		t.Error("unsetting the only key must leave empty settings")
	}
}

func TestSettingKeySetRejectsBadValue(t *testing.T) {
	key, _ := lookupSettingKey("notifications")
	var s usercfg.Settings
	if err := key.set(&s, "sometimes"); err == nil {
		t.Fatal("expected an error for a bad value, got nil")
	}
	if !s.IsEmpty() {
		t.Error("a rejected value must not change the settings")
	}
}

func TestSettingKeyNames(t *testing.T) {
	names := settingKeyNames()
	if len(names) != len(settingKeys) {
		t.Fatalf("settingKeyNames returned %d names for %d keys", len(names), len(settingKeys))
	}
	for i, k := range settingKeys {
		if names[i] != k.name {
			t.Errorf("name %d = %q, want %q", i, names[i], k.name)
		}
	}
}

// --- flag / preference precedence ---

func TestResolveNoNotify(t *testing.T) {
	var unset usercfg.Settings
	var savedOff usercfg.Settings
	savedOff.SetNotifications(false)
	var savedOn usercfg.Settings
	savedOn.SetNotifications(true)

	tests := []struct {
		name     string
		saved    usercfg.Settings
		notify   bool
		noNotify bool
		want     bool // NoNotify: true means "post nothing"
	}{
		{"nothing saved, no flags: notifications on", unset, false, false, false},
		{"saved off, no flags: stays off", savedOff, false, false, true},
		{"saved on, no flags: stays on", savedOn, false, false, false},
		{"--no-notify with nothing saved", unset, false, true, true},
		{"--no-notify over a saved on", savedOn, false, true, true},
		{"--notify over a saved off", savedOff, true, false, false},
		{"--notify with nothing saved", unset, true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveNoNotify(tc.saved, tc.notify, tc.noNotify); got != tc.want {
				t.Errorf("resolveNoNotify = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- the wiring the preference exists for ---

// With notifications turned off, the progress reporter must not be wrapped by
// either notifier — the terminal display is all that is left.
func TestBuildDependenciesSkipsNotifiersWhenOff(t *testing.T) {
	cfg := domain.RunConfig{OutputPath: t.TempDir(), NoNotify: true}
	kinopub.ApplyDefaults(&cfg)

	deps, cleanup, err := buildDependencies(cfg)
	if err != nil {
		t.Fatalf("buildDependencies: %v", err)
	}
	defer cleanup()

	if _, ok := deps.ProgressReporter.(*desktopnotify.Notifier); ok {
		t.Error("desktop notifications were wired up despite NoNotify")
	}
	if _, ok := deps.ProgressReporter.(*termuxapi.Notifier); ok {
		t.Error("Termux notifications were wired up despite NoNotify")
	}
}

// And with them on, the reporter is wrapped wherever a backend exists. On a
// machine with no notifier at all the wrappers are no-ops by design, so there
// is nothing to assert.
func TestBuildDependenciesWiresNotifiersWhenOn(t *testing.T) {
	if !desktopnotify.Available() {
		t.Skip("no desktop notification backend on this machine")
	}
	cfg := domain.RunConfig{OutputPath: t.TempDir()}
	kinopub.ApplyDefaults(&cfg)

	deps, cleanup, err := buildDependencies(cfg)
	if err != nil {
		t.Fatalf("buildDependencies: %v", err)
	}
	defer cleanup()

	if _, ok := deps.ProgressReporter.(*desktopnotify.Notifier); !ok {
		t.Errorf("progress reporter = %T, want it wrapped for desktop notifications", deps.ProgressReporter)
	}
}
