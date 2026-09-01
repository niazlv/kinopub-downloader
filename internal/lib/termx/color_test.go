// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package termx

import (
	"os"
	"strings"
	"testing"
)

// clearColorEnv neutralizes every variable Supported consults, so a test runs
// the same on a developer's machine as in CI.
func clearColorEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"NO_COLOR", "CLICOLOR_FORCE", "FORCE_COLOR", "CLICOLOR", "TERM"} {
		t.Setenv(name, "")
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeAuto, false},
		{"auto", ModeAuto, false},
		{"AUTO", ModeAuto, false},
		{" auto ", ModeAuto, false},
		{"always", ModeAlways, false},
		{"yes", ModeAlways, false},
		{"1", ModeAlways, false},
		{"never", ModeNever, false},
		{"no", ModeNever, false},
		{"0", ModeNever, false},
		{"sometimes", ModeAuto, true},
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMode(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseMode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseMode_ErrorNamesTheChoices(t *testing.T) {
	_, err := ParseMode("bright")
	if err == nil {
		t.Fatal("expected an error for an unknown mode")
	}
	for _, want := range []string{"bright", "auto", "always", "never"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestModeString(t *testing.T) {
	for mode, want := range map[Mode]string{
		ModeAuto:   "auto",
		ModeAlways: "always",
		ModeNever:  "never",
	} {
		if got := mode.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}

func TestSupported_NoColorWins(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "1")

	if Supported(os.Stdout) {
		t.Error("NO_COLOR must disable color even when CLICOLOR_FORCE is set")
	}
}

func TestSupported_EmptyOrZeroNoColorIsIgnored(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("NO_COLOR", "0")
	t.Setenv("CLICOLOR_FORCE", "1")

	if !Supported(os.Stdout) {
		t.Error("NO_COLOR=0 should not disable color")
	}
}

func TestSupported_ForceOverNonTTY(t *testing.T) {
	f := tempFile(t)

	clearColorEnv(t)
	t.Setenv("CLICOLOR_FORCE", "1")
	if !Supported(f) {
		t.Error("CLICOLOR_FORCE should enable color for a non-terminal")
	}

	clearColorEnv(t)
	t.Setenv("FORCE_COLOR", "1")
	if !Supported(f) {
		t.Error("FORCE_COLOR should enable color for a non-terminal")
	}
}

func TestSupported_DumbTerminal(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("TERM", "dumb")

	if Supported(os.Stdout) {
		t.Error("TERM=dumb must not be colored")
	}
}

func TestSupported_CLICOLORZero(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("CLICOLOR", "0")

	if Supported(os.Stdout) {
		t.Error("CLICOLOR=0 must disable color")
	}
}

func TestSupported_FallsBackToTTYCheck(t *testing.T) {
	clearColorEnv(t)

	// A regular file is never a terminal, whatever the environment says.
	if Supported(tempFile(t)) {
		t.Error("a regular file must not be colored")
	}
}

func TestModeEnabled(t *testing.T) {
	clearColorEnv(t)
	f := tempFile(t)

	if !ModeAlways.Enabled(f) {
		t.Error("ModeAlways must enable color regardless of the stream")
	}
	if ModeNever.Enabled(os.Stdout) {
		t.Error("ModeNever must disable color regardless of the stream")
	}
	if ModeAuto.Enabled(f) {
		t.Error("ModeAuto must not color a regular file")
	}
}

func TestModeNever_BeatsForceEnv(t *testing.T) {
	clearColorEnv(t)
	t.Setenv("CLICOLOR_FORCE", "1")

	if ModeNever.Enabled(os.Stdout) {
		t.Error("--color=never must win over CLICOLOR_FORCE")
	}
}

func TestStyler_Off(t *testing.T) {
	s := NewStyler(false)
	if s.Enabled() {
		t.Error("NewStyler(false).Enabled() = true")
	}
	for name, got := range map[string]string{
		"Bold":   s.Bold("x"),
		"Red":    s.Red("x"),
		"Gray":   s.Gray("x"),
		"Cyan":   s.Cyan("x"),
		"Dim":    s.Dim("x"),
		"Green":  s.Green("x"),
		"Yellow": s.Yellow("x"),
	} {
		if got != "x" {
			t.Errorf("%s with color off = %q, want %q", name, got, "x")
		}
	}
}

func TestStyler_On(t *testing.T) {
	s := NewStyler(true)
	got := s.Bold("x")
	if got != Bold+"x"+Reset {
		t.Errorf("Bold(%q) = %q, want %q", "x", got, Bold+"x"+Reset)
	}
	if s.Red("x") == s.Green("x") {
		t.Error("distinct colors must produce distinct output")
	}
}

func TestStyler_EmptyTextIsNotWrapped(t *testing.T) {
	// A styled empty string would be nothing but escapes: two sequences that
	// take up no columns and can bleed into whatever follows.
	if got := NewStyler(true).Bold(""); got != "" {
		t.Errorf("Bold(\"\") = %q, want \"\"", got)
	}
}

func TestStylerFor(t *testing.T) {
	clearColorEnv(t)
	if StylerFor(ModeNever, os.Stdout).Enabled() {
		t.Error("StylerFor(ModeNever) must be disabled")
	}
	if !StylerFor(ModeAlways, tempFile(t)).Enabled() {
		t.Error("StylerFor(ModeAlways) must be enabled")
	}
}

func TestWidth_NonTTYAndNil(t *testing.T) {
	if got := Width(tempFile(t)); got != defaultWidth {
		t.Errorf("Width(regular file) = %d, want %d", got, defaultWidth)
	}
	if got := Width(nil); got != defaultWidth {
		t.Errorf("Width(nil) = %d, want %d", got, defaultWidth)
	}
}

// tempFile returns a regular file that is closed and removed when the test ends.
func tempFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "termx-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
