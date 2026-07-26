// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// qv is a quality-specific helper to build a Variant with the fields that
// matter for selection logic (height, bandwidth, codecs).
func qv(height, bandwidth int, codecs string) Variant {
	return Variant{
		Height:    height,
		Bandwidth: bandwidth,
		Codecs:    codecs,
	}
}

func TestQualityAbs(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 0},
		{5, 5},
		{-5, 5},
		{-1, 1},
		{1, 1},
	}
	for _, c := range cases {
		if got := abs(c.in); got != c.want {
			t.Errorf("abs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestQualityVariantCodecHelpers(t *testing.T) {
	cases := []struct {
		codecs   string
		wantH265 bool
		wantH264 bool
	}{
		{"avc1.640028,mp4a.40.2", false, true},
		{"hvc1.1.6.L120", true, false},
		{"hev1.1.6.L120", true, false},
		{"hevc", true, false},
		{"", false, true}, // empty codecs counts as h264 per IsH264
	}
	for _, c := range cases {
		v := Variant{Codecs: c.codecs}
		if got := v.IsH265(); got != c.wantH265 {
			t.Errorf("IsH265(%q) = %v, want %v", c.codecs, got, c.wantH265)
		}
		if got := v.IsH264(); got != c.wantH264 {
			t.Errorf("IsH264(%q) = %v, want %v", c.codecs, got, c.wantH264)
		}
	}
}

func TestQualityBitrateKbpsAndLabel(t *testing.T) {
	v := qv(1080, 2500000, "avc1.640028")
	if got := v.BitrateKbps(); got != 2500 {
		t.Errorf("BitrateKbps() = %d, want 2500", got)
	}
	if got := v.Label(); got != "1080p/h264 (2500 kbps)" {
		t.Errorf("Label() = %q, want %q", got, "1080p/h264 (2500 kbps)")
	}

	h265 := qv(2160, 8000000, "hvc1.1.6.L150")
	if got := h265.Label(); got != "2160p/h265 (8000 kbps)" {
		t.Errorf("Label() = %q, want %q", got, "2160p/h265 (8000 kbps)")
	}
}

func TestSelectVariantEmpty(t *testing.T) {
	_, err := SelectVariant(nil, domain.Quality("max"))
	if err == nil {
		t.Fatal("expected error for empty variants, got nil")
	}
}

func TestSelectVariantSingle(t *testing.T) {
	only := qv(480, 800000, "avc1")
	// Across several preferences, a single variant must always be returned.
	for _, pref := range []string{"", "optimal", "max", "1080p", "720p", "480p", "4k", "garbage"} {
		got, err := SelectVariant([]Variant{only}, domain.Quality(pref))
		if err != nil {
			t.Fatalf("pref %q: unexpected error: %v", pref, err)
		}
		if got != only {
			t.Errorf("pref %q: got %+v, want the single variant %+v", pref, got, only)
		}
	}
}

func TestSelectVariantMax(t *testing.T) {
	variants := []Variant{
		qv(480, 800000, "avc1"),
		qv(1080, 5000000, "avc1"),
		qv(720, 2000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("MAX"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bandwidth != 5000000 {
		t.Errorf("max: got bandwidth %d, want 5000000", got.Bandwidth)
	}
}

func TestSelectMaxFirstWinsOnTie(t *testing.T) {
	// selectMax uses strict >, so the first of equal bandwidths is kept.
	variants := []Variant{
		qv(1080, 4000000, "avc1"),
		qv(720, 4000000, "hvc1"),
	}
	got := selectMax(variants)
	if got.Height != 1080 {
		t.Errorf("selectMax tie: got height %d, want 1080 (first)", got.Height)
	}
}

func TestSelectOptimal1080pH264UnderBudget(t *testing.T) {
	// Two 1080p h264 candidates under 3000 kbps; closest to 2500 wins.
	variants := []Variant{
		qv(1080, 2000000, "avc1"), // 2000 kbps, diff 500
		qv(1080, 2600000, "avc1"), // 2600 kbps, diff 100 -> winner
		qv(1080, 5000000, "avc1"), // over budget, excluded
		qv(720, 1500000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality(""))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bandwidth != 2600000 {
		t.Errorf("optimal: got bandwidth %d, want 2600000", got.Bandwidth)
	}
}

func TestSelectOptimalExcludesH265At1080(t *testing.T) {
	// 1080p is only available as h265 -> not eligible for the first branch;
	// falls through to 720p h264.
	variants := []Variant{
		qv(1080, 2500000, "hvc1"), // h265, excluded from 1080 branch
		qv(720, 1500000, "avc1"),  // 720p h264
		qv(720, 2200000, "avc1"),  // higher bitrate 720p -> winner
	}
	got, err := SelectVariant(variants, domain.Quality("optimal"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 720 || got.Bandwidth != 2200000 {
		t.Errorf("optimal: got %+v, want 720p@2200000", got)
	}
}

func TestSelectOptimalFallbackClosestTo2500(t *testing.T) {
	// Neither 1080p-h264-under-budget nor 720p-h264 exists.
	variants := []Variant{
		qv(2160, 8000000, "hvc1"), // 8000 kbps, diff 5500
		qv(540, 1000000, "avc1"),  // 1000 kbps, diff 1500
		qv(480, 3000000, "avc1"),  // 3000 kbps, diff 500 -> winner
	}
	got, err := SelectVariant(variants, domain.Quality("optimal"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bandwidth != 3000000 {
		t.Errorf("optimal fallback: got bandwidth %d, want 3000000", got.Bandwidth)
	}
}

func TestSelectExplicitExactHeightLowestBitrate(t *testing.T) {
	// At 1080p the lowest bandwidth wins, among the h264 candidates: the h265
	// entry is filtered out before the bitrate comparison.
	variants := []Variant{
		qv(1080, 5000000, "avc1"),
		qv(1080, 3000000, "avc1"), // lowest -> winner
		qv(1080, 4000000, "hvc1"),
		qv(720, 1000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("1080p"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bandwidth != 3000000 {
		t.Errorf("1080p: got bandwidth %d, want 3000000", got.Bandwidth)
	}
}

func TestSelectExplicit720pPicksHighestBitrate(t *testing.T) {
	// Documented contract: "720p" means the best 720p rendition available, not
	// the cheapest one. Below 1080p the low-bitrate renditions are visibly bad
	// for a saving that no longer matters.
	variants := []Variant{
		qv(720, 800000, "avc1"),
		qv(720, 2400000, "avc1"), // highest 720p -> winner
		qv(720, 1500000, "avc1"),
		qv(1080, 6000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("720p"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 720 || got.Bandwidth != 2400000 {
		t.Errorf("720p: got %+v, want 720p@2400000 (highest)", got)
	}
}

func TestSelectExplicitSubHDPicksHighestBitrate(t *testing.T) {
	// The same rule applies to every height below 1080p.
	for _, c := range []struct {
		pref   string
		height int
	}{{"480p", 480}, {"360p", 360}} {
		variants := []Variant{
			qv(c.height, 500000, "avc1"),
			qv(c.height, 1200000, "avc1"), // winner
			qv(1080, 5000000, "avc1"),
		}
		got, err := SelectVariant(variants, domain.Quality(c.pref))
		if err != nil {
			t.Fatalf("%s: %v", c.pref, err)
		}
		if got.Height != c.height || got.Bandwidth != 1200000 {
			t.Errorf("%s: got %+v, want %dp@1200000 (highest)", c.pref, got, c.height)
		}
	}
}

func TestSelectExplicitAtOrAbove1080pPicksLowestBitrate(t *testing.T) {
	// At 1080p and above the resolution already carries the detail, so the
	// cheapest rendition is the documented (and bandwidth-friendly) choice.
	for _, c := range []struct {
		pref   string
		height int
	}{{"1080p", 1080}, {"2160p", 2160}} {
		variants := []Variant{
			qv(c.height, 6000000, "avc1"),
			qv(c.height, 3500000, "avc1"), // winner
			qv(720, 1000000, "avc1"),
		}
		got, err := SelectVariant(variants, domain.Quality(c.pref))
		if err != nil {
			t.Fatalf("%s: %v", c.pref, err)
		}
		if got.Height != c.height || got.Bandwidth != 3500000 {
			t.Errorf("%s: got %+v, want %dp@3500000 (lowest)", c.pref, got, c.height)
		}
	}
}

func TestSelectExplicitPrefersH264WithoutCodecSuffix(t *testing.T) {
	// h265 is cheaper but does not decode on every device, so an unqualified
	// height must never hand back HEVC while an h264 rendition exists — even
	// when the h265 one would win the bitrate rule on its own.
	t.Run("1080p skips cheaper h265", func(t *testing.T) {
		variants := []Variant{
			qv(1080, 2000000, "hvc1.1.6.L120"), // cheapest, but HEVC
			qv(1080, 3500000, "avc1.640028"),   // winner: cheapest h264
			qv(1080, 5000000, "avc1.640028"),
		}
		got, err := SelectVariant(variants, domain.Quality("1080p"))
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsH264() || got.Bandwidth != 3500000 {
			t.Errorf("1080p: got %+v, want the cheapest h264 (3500000)", got)
		}
	})

	t.Run("720p skips higher-bitrate h265", func(t *testing.T) {
		variants := []Variant{
			qv(720, 4000000, "hev1.1.6.L93"), // highest, but HEVC
			qv(720, 2400000, "avc1.4d401f"),  // winner: highest h264
			qv(720, 900000, "avc1.4d401f"),
		}
		got, err := SelectVariant(variants, domain.Quality("720p"))
		if err != nil {
			t.Fatal(err)
		}
		if !got.IsH264() || got.Bandwidth != 2400000 {
			t.Errorf("720p: got %+v, want the highest h264 (2400000)", got)
		}
	})

	t.Run("falls back to h265 when it is the only codec", func(t *testing.T) {
		variants := []Variant{
			qv(2160, 12000000, "hvc1.2.4.L153"),
			qv(2160, 9000000, "hvc1.2.4.L153"), // lowest at 2160p -> winner
			qv(1080, 3000000, "avc1"),
		}
		got, err := SelectVariant(variants, domain.Quality("2160p"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Height != 2160 || got.Bandwidth != 9000000 {
			t.Errorf("2160p: got %+v, want the h265 fallback at 9000000", got)
		}
	})

	t.Run("variant without CODECS counts as h264", func(t *testing.T) {
		variants := []Variant{
			qv(1080, 2000000, "hvc1"),
			qv(1080, 4000000, ""), // no CODECS attribute -> treated as h264
		}
		got, err := SelectVariant(variants, domain.Quality("1080p"))
		if err != nil {
			t.Fatal(err)
		}
		if got.Bandwidth != 4000000 {
			t.Errorf("got %+v, want the codec-less variant", got)
		}
	})
}

func TestSelectExplicitCodecSuffixOverridesH264Preference(t *testing.T) {
	// An explicit "-h265" is the user asking for HEVC on purpose; the h264
	// preference must not override it. Bitrate follows the height rule.
	variants := []Variant{
		qv(720, 3000000, "hvc1"),
		qv(720, 1200000, "hvc1"),
		qv(720, 2000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("720p-h265"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsH265() {
		t.Fatalf("720p-h265: got %+v, want an h265 variant", got)
	}
	if got.Bandwidth != 3000000 {
		t.Errorf("720p-h265: got bandwidth %d, want 3000000 (highest below 1080p)", got.Bandwidth)
	}
}

func TestSelectExplicitHeightStringForms(t *testing.T) {
	variants := []Variant{
		qv(2160, 8000000, "hvc1"),
		qv(1080, 3000000, "avc1"),
		qv(720, 2000000, "avc1"),
		qv(480, 1000000, "avc1"),
		qv(360, 600000, "avc1"),
	}
	cases := []struct {
		pref       string
		wantHeight int
	}{
		{"1080p", 1080},
		{"1080", 1080},
		{"720p", 720},
		{"720", 720},
		{"480p", 480},
		{"480", 480},
		{"360p", 360},
		{"360", 360},
		{"2160p", 2160},
		{"4k", 2160},
		{"4K", 2160}, // case-insensitive via SelectVariant lowercasing
	}
	for _, c := range cases {
		got, err := SelectVariant(variants, domain.Quality(c.pref))
		if err != nil {
			t.Fatalf("pref %q: %v", c.pref, err)
		}
		if got.Height != c.wantHeight {
			t.Errorf("pref %q: got height %d, want %d", c.pref, got.Height, c.wantHeight)
		}
	}
}

func TestSelectExplicitNumericFallbackParse(t *testing.T) {
	// "1440p" / "1440" aren't in the switch; Sscanf parses the number.
	variants := []Variant{
		qv(1440, 5000000, "avc1"),
		qv(1080, 3000000, "avc1"),
	}
	for _, pref := range []string{"1440p", "1440"} {
		got, err := SelectVariant(variants, domain.Quality(pref))
		if err != nil {
			t.Fatalf("pref %q: %v", pref, err)
		}
		if got.Height != 1440 {
			t.Errorf("pref %q: got height %d, want 1440", pref, got.Height)
		}
	}
}

func TestSelectExplicitWithCodecSuffix(t *testing.T) {
	variants := []Variant{
		qv(1080, 3000000, "avc1"), // h264
		qv(1080, 4000000, "hvc1"), // h265
	}
	gotH265, err := SelectVariant(variants, domain.Quality("1080p-h265"))
	if err != nil {
		t.Fatal(err)
	}
	if !gotH265.IsH265() {
		t.Errorf("1080p-h265: got %+v, want h265 variant", gotH265)
	}

	gotH264, err := SelectVariant(variants, domain.Quality("1080p-h264"))
	if err != nil {
		t.Fatal(err)
	}
	if !gotH264.IsH264() {
		t.Errorf("1080p-h264: got %+v, want h264 variant", gotH264)
	}
}

func TestSelectExplicitNoMatchClosestHeight(t *testing.T) {
	// Request 1080p but only 720 and 480 exist -> closestToHeight (720).
	variants := []Variant{
		qv(480, 1000000, "avc1"),
		qv(720, 2000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("1080p"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 720 {
		t.Errorf("no-match: got height %d, want 720 (closest)", got.Height)
	}
}

func TestSelectExplicitCodecNoMatchClosestHeight(t *testing.T) {
	// 1080p exists but only as h264; requesting h265 yields zero candidates,
	// so closestToHeight over ALL variants runs (still height 1080 wins).
	variants := []Variant{
		qv(1080, 3000000, "avc1"),
		qv(720, 2000000, "avc1"),
	}
	got, err := SelectVariant(variants, domain.Quality("1080p-h265"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Height != 1080 {
		t.Errorf("codec no-match: got height %d, want 1080 (closest)", got.Height)
	}
}

func TestSelectExplicitUnparseableHeightZero(t *testing.T) {
	// A preference that parses to height 0 means wantHeight stays 0, so every
	// variant matches (no height filter). With no height to reason about, the
	// original lowest-bitrate rule stands — "max" is the spelling for biggest.
	variants := []Variant{
		qv(1080, 3000000, "avc1"),
		qv(720, 1500000, "avc1"), // lowest -> winner
	}
	got, err := SelectVariant(variants, domain.Quality("garbage"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bandwidth != 1500000 {
		t.Errorf("unparseable: got bandwidth %d, want 1500000 (lowest)", got.Bandwidth)
	}
}

func TestClosestToBitrate(t *testing.T) {
	variants := []Variant{
		qv(480, 1000000, "avc1"), // 1000
		qv(720, 2400000, "avc1"), // 2400, diff 100
		qv(1080, 2600000, "avc1"),
	}
	t.Run("nearest_below", func(t *testing.T) {
		got := closestToBitrate(variants, 2400)
		if got.Bandwidth != 2400000 {
			t.Errorf("got %d, want 2400000", got.Bandwidth)
		}
	})
	t.Run("nearest_above_target_low", func(t *testing.T) {
		got := closestToBitrate(variants, 500)
		if got.Bandwidth != 1000000 {
			t.Errorf("got %d, want 1000000", got.Bandwidth)
		}
	})
	t.Run("exact", func(t *testing.T) {
		got := closestToBitrate(variants, 1000)
		if got.Bandwidth != 1000000 {
			t.Errorf("got %d, want 1000000", got.Bandwidth)
		}
	})
}

func TestClosestToBitrateTiePrefersHigherBandwidth(t *testing.T) {
	// Target 2000 kbps; 1500 and 2500 are both 500 away. Tie -> higher bandwidth.
	variants := []Variant{
		qv(720, 1500000, "avc1"),  // diff 500
		qv(1080, 2500000, "avc1"), // diff 500, higher bandwidth -> winner
	}
	got := closestToBitrate(variants, 2000)
	if got.Bandwidth != 2500000 {
		t.Errorf("tie: got bandwidth %d, want 2500000 (higher)", got.Bandwidth)
	}

	// Reverse the order to ensure the tie-break is by bandwidth, not position.
	reversed := []Variant{
		qv(1080, 2500000, "avc1"),
		qv(720, 1500000, "avc1"),
	}
	got2 := closestToBitrate(reversed, 2000)
	if got2.Bandwidth != 2500000 {
		t.Errorf("tie reversed: got bandwidth %d, want 2500000 (higher)", got2.Bandwidth)
	}
}

func TestClosestToHeight(t *testing.T) {
	variants := []Variant{
		qv(360, 600000, "avc1"),
		qv(720, 2000000, "avc1"),
		qv(1080, 3000000, "avc1"),
	}
	t.Run("nearest_below", func(t *testing.T) {
		got := closestToHeight(variants, 800) // 720 closer than 1080
		if got.Height != 720 {
			t.Errorf("got %d, want 720", got.Height)
		}
	})
	t.Run("nearest_above", func(t *testing.T) {
		got := closestToHeight(variants, 1000) // 1080 closer than 720
		if got.Height != 1080 {
			t.Errorf("got %d, want 1080", got.Height)
		}
	})
	t.Run("exact", func(t *testing.T) {
		got := closestToHeight(variants, 720)
		if got.Height != 720 {
			t.Errorf("got %d, want 720", got.Height)
		}
	})
}

func TestClosestToHeightTiePrefersHigherBandwidth(t *testing.T) {
	// Target 600; 480 and 720 are both 120 away. Tie -> higher bandwidth.
	variants := []Variant{
		qv(480, 1000000, "avc1"), // diff 120
		qv(720, 2000000, "avc1"), // diff 120, higher bandwidth -> winner
	}
	got := closestToHeight(variants, 600)
	if got.Height != 720 {
		t.Errorf("tie: got height %d (bw %d), want 720 (higher bandwidth)", got.Height, got.Bandwidth)
	}

	reversed := []Variant{
		qv(720, 2000000, "avc1"),
		qv(480, 1000000, "avc1"),
	}
	got2 := closestToHeight(reversed, 600)
	if got2.Height != 720 {
		t.Errorf("tie reversed: got height %d, want 720 (higher bandwidth)", got2.Height)
	}
}
