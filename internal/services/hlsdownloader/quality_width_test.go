// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import "testing"

// --list-formats prints the full resolution, so the width must survive the
// collapse into menu options alongside the height.
func TestVideoQualitiesFrom_CarriesResolution(t *testing.T) {
	got := videoQualitiesFrom([]Variant{
		{Height: 406, Width: 720, Bandwidth: 1060000, Codecs: "avc1.640028"},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 option, got %d", len(got))
	}
	if got[0].Width != 720 || got[0].Height != 406 {
		t.Errorf("resolution = %dx%d, want 720x406", got[0].Width, got[0].Height)
	}
}
