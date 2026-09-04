// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/lib/usercfg"
)

// ---------------------------------------------------------------------------
// Subcommand: config
// ---------------------------------------------------------------------------

// settingKey describes one preference `kinopub config` can read and write.
// Keeping them in a table means adding a preference is one entry rather than a
// new branch in each of show/get/set/unset — and the completion scripts and the
// help text list the same names the code accepts.
type settingKey struct {
	name string
	desc string
	// values names the accepted input, for help and error messages.
	values string
	// get returns the effective value and whether the file actually says so
	// (as opposed to the built-in default).
	get func(usercfg.Settings) (value string, explicit bool)
	// set parses a user-supplied value into the settings.
	set func(*usercfg.Settings, string) error
	// unset drops the key, returning the setting to its default.
	unset func(*usercfg.Settings)
}

// settingKeys is every preference the file understands.
var settingKeys = []settingKey{
	{
		name:   "notifications",
		desc:   "system notifications about download progress (Termux / desktop)",
		values: "on, off",
		get: func(s usercfg.Settings) (string, bool) {
			return onOff(s.NotificationsEnabled()), s.Notifications != nil
		},
		set: func(s *usercfg.Settings, v string) error {
			on, err := parseOnOff(v)
			if err != nil {
				return err
			}
			s.SetNotifications(on)
			return nil
		},
		unset: func(s *usercfg.Settings) { s.Notifications = nil },
	},
}

// lookupSettingKey finds a key by name.
func lookupSettingKey(name string) (settingKey, bool) {
	for _, k := range settingKeys {
		if k.name == strings.ToLower(strings.TrimSpace(name)) {
			return k, true
		}
	}
	return settingKey{}, false
}

// settingKeyNames lists the known keys, for help and error messages.
func settingKeyNames() []string {
	names := make([]string, 0, len(settingKeys))
	for _, k := range settingKeys {
		names = append(names, k.name)
	}
	return names
}

// parseOnOff accepts the spellings people actually type for a boolean setting.
func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "y", "1", "enable", "enabled":
		return true, nil
	case "off", "false", "no", "n", "0", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("expected on or off, got %q", v)
	}
}

// onOff renders a boolean the same way the setter accepts it.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// resolveNoNotify decides whether a run suppresses system notifications: the
// flags speak for this run, the saved preference for every run that names
// neither, and the built-in default (notifications on) when nothing says
// otherwise. --notify wins over a saved "off", which is the whole point of
// having it.
func resolveNoNotify(saved usercfg.Settings, notify, noNotify bool) bool {
	switch {
	case notify:
		return false
	case noNotify:
		return true
	default:
		return !saved.NotificationsEnabled()
	}
}

// runConfig implements `kinopub config`, the persistent counterpart to the
// run-scoped flags: a preference saved here applies to every later run, so the
// flag never has to be typed again.
//
// Usage: kinopub config                       show every setting
//
//	kinopub config get <key>
//	kinopub config set <key> <value>
//	kinopub config unset <key>
//	kinopub config path
func runConfig(args []string) int {
	fs := flag.NewFlagSet("kinopub config", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Show and change the saved settings — the preferences that apply to " +
			"every run, so a flag does not have to be repeated each time.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub config", desc: "show every setting and where it comes from"},
			command{name: "kinopub config get <key>", desc: "print one setting's value"},
			command{name: "kinopub config set <key> <value>", desc: "save a setting"},
			command{name: "kinopub config unset <key>", desc: "forget it, back to the default"},
			command{name: "kinopub config path", desc: "print the path of the settings file"},
		)
		h.section("Settings:")
		for _, k := range settingKeys {
			h.bullet(pad(k.name, 16), k.values+" — "+k.desc)
		}
		h.section("Flags:")
		h.flags(fs)
		h.section("Examples:")
		h.example("Never show system notifications again",
			"kinopub config set notifications off")
		h.example("Turn them back on for one run",
			"kinopub --notify https://kino.watch/item/view/38290")
		h.example("Turn them back on for good",
			"kinopub config set notifications on")
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return showConfig()
	}

	switch rest[0] {
	case "show", "list":
		return showConfig()
	case "path":
		path, err := usercfg.Path()
		if err != nil {
			errorf("%v", err)
			return 1
		}
		fmt.Println(path)
		return 0
	case "get":
		if len(rest) != 2 {
			errorf("usage: kinopub config get <key>")
			return 1
		}
		return getConfig(rest[1])
	case "set":
		if len(rest) != 3 {
			errorf("usage: kinopub config set <key> <value>")
			return 1
		}
		return setConfig(rest[1], rest[2])
	case "unset":
		if len(rest) != 2 {
			errorf("usage: kinopub config unset <key>")
			return 1
		}
		return unsetConfig(rest[1])
	default:
		errorf("unknown config command %q. Expected show, get, set, unset or path.", rest[0])
		fmt.Fprintln(os.Stderr)
		fs.Usage()
		return 1
	}
}

// showConfig prints every setting, its effective value, and whether that value
// was saved or is just the default.
func showConfig() int {
	settings, path, code := loadConfigFor("read")
	if code != 0 {
		return code
	}

	h := newHelpPrinter(os.Stderr, errStyle)
	h.line("%s %s", errStyle.Bold("Settings file:"), errStyle.Cyan(path))
	h.blank()
	for _, k := range settingKeys {
		value, explicit := k.get(settings)
		origin := errStyle.Gray("(default)")
		if explicit {
			origin = errStyle.Green("(saved)")
		}
		h.line("  %s %s %s", pad(k.name, 16), pad(value, 5), origin)
		h.line("  %s %s", strings.Repeat(" ", 16), errStyle.Gray(k.desc))
	}
	if settings.IsEmpty() {
		h.blank()
		h.text("Nothing saved yet. Change a setting with `kinopub config set <key> <value>`, " +
			"e.g. `kinopub config set notifications off`.")
	}
	return 0
}

// getConfig prints one setting's effective value, and nothing else, so it can
// be read by a script.
func getConfig(name string) int {
	key, ok := lookupSettingKey(name)
	if !ok {
		errorf("unknown setting %q. Known settings: %s.", name, strings.Join(settingKeyNames(), ", "))
		return 1
	}
	settings, _, code := loadConfigFor("read")
	if code != 0 {
		return code
	}
	value, _ := key.get(settings)
	fmt.Println(value)
	return 0
}

// setConfig saves one setting.
func setConfig(name, value string) int {
	key, ok := lookupSettingKey(name)
	if !ok {
		errorf("unknown setting %q. Known settings: %s.", name, strings.Join(settingKeyNames(), ", "))
		return 1
	}
	// A malformed file would otherwise be silently replaced by this write,
	// discarding whatever else the user had put in it.
	settings, path, code := loadConfigFor("update")
	if code != 0 {
		return code
	}
	if err := key.set(&settings, value); err != nil {
		errorf("%s: %v (accepted: %s)", key.name, err, key.values)
		return 1
	}
	if err := usercfg.Save(settings); err != nil {
		errorf("%v", err)
		return 1
	}
	saved, _ := key.get(settings)
	fmt.Fprintf(os.Stderr, "%s %s = %s  %s\n", errStyle.Green("✓"), key.name,
		errStyle.Bold(saved), errStyle.Gray("("+path+")"))
	return 0
}

// unsetConfig forgets one setting, returning it to the built-in default. When
// nothing is left the file is removed rather than left behind holding "{}".
func unsetConfig(name string) int {
	key, ok := lookupSettingKey(name)
	if !ok {
		errorf("unknown setting %q. Known settings: %s.", name, strings.Join(settingKeyNames(), ", "))
		return 1
	}
	settings, _, code := loadConfigFor("update")
	if code != 0 {
		return code
	}
	key.unset(&settings)

	var err error
	if settings.IsEmpty() {
		err = usercfg.Clear()
	} else {
		err = usercfg.Save(settings)
	}
	if err != nil {
		errorf("%v", err)
		return 1
	}
	value, _ := key.get(settings)
	fmt.Fprintf(os.Stderr, "%s %s is back to its default (%s).\n", errStyle.Green("✓"), key.name, value)
	return 0
}

// loadConfigFor reads the settings for the `config` subcommand, where an
// unreadable file is fatal — unlike a download, which carries on with the
// defaults rather than refusing to run over a stray character in a config file.
// verb names what the caller was about to do, for the error message.
func loadConfigFor(verb string) (usercfg.Settings, string, int) {
	path, err := usercfg.Path()
	if err != nil {
		errorf("%v", err)
		return usercfg.Settings{}, "", 1
	}
	settings, err := usercfg.Load()
	if err != nil {
		errorf("cannot %s the settings: %v", verb, err)
		return usercfg.Settings{}, path, 1
	}
	return settings, path, 0
}

// pad right-pads s to at least n columns, so a short table lines up without
// pulling in a full text-table layer.
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
