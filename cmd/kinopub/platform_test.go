// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
)

func TestPlatformSessionNeedsRenewal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name string
		s    credstore.SiteSession
		want bool
	}{
		{"browser cookie never", credstore.SiteSession{Cookie: "a=1", ExpiresAt: now.Add(-time.Hour)}, false},
		{"device session, plenty of time", credstore.SiteSession{Cookie: "kino_session=t", RefreshToken: "r",
			ExpiresAt: now.Add(10 * 24 * time.Hour)}, false},
		{"device session, expiring within a day", credstore.SiteSession{Cookie: "kino_session=t", RefreshToken: "r",
			ExpiresAt: now.Add(6 * time.Hour)}, true},
		{"device session, already expired", credstore.SiteSession{Cookie: "kino_session=t", RefreshToken: "r",
			ExpiresAt: now.Add(-48 * time.Hour)}, true},
		{"device session with unknown expiry", credstore.SiteSession{Cookie: "kino_session=t", RefreshToken: "r"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := platformSessionNeedsRenewal(tt.s, now); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceNameCarriesTheVersion(t *testing.T) {
	if name := deviceName(); !strings.HasPrefix(name, "kinopub "+version) {
		t.Errorf("deviceName() = %q", name)
	}
}
