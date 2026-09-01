// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package logx

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

func consoleRecord() Record {
	return Record{
		Time:      time.Date(2026, 9, 1, 12, 34, 56, 0, time.UTC),
		Level:     domain.LevelWarn,
		Component: "engine",
		Message:   "falling back",
		Fields:    []domain.Field{{Key: "attempt", Value: 2}},
	}
}

func TestConsoleHandler_ColorOffKeepsTheLayoutAndDropsTheEscapes(t *testing.T) {
	var buf bytes.Buffer
	NewConsoleHandler(&buf, domain.VerbosityNormal, nil, termx.NewStyler(false)).Handle(consoleRecord())

	got := buf.String()
	if strings.Contains(got, "\033[") {
		t.Errorf("uncolored console output must carry no escapes, got %q", got)
	}
	for _, want := range []string{"12:34:56", "WARN", "[engine]", "falling back", "attempt=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("console output should contain %q, got %q", want, got)
		}
	}
}

func TestConsoleHandler_ColorOnMatchesNewTTYHandler(t *testing.T) {
	var colored, tty bytes.Buffer
	NewConsoleHandler(&colored, domain.VerbosityNormal, nil, termx.NewStyler(true)).Handle(consoleRecord())
	NewTTYHandler(&tty, domain.VerbosityNormal, nil).Handle(consoleRecord())

	if colored.String() != tty.String() {
		t.Errorf("NewTTYHandler should be NewConsoleHandler with color on:\n%q\n%q",
			tty.String(), colored.String())
	}
	if !strings.Contains(colored.String(), termx.Yellow) {
		t.Errorf("a warning should be yellow, got %q", colored.String())
	}
}
