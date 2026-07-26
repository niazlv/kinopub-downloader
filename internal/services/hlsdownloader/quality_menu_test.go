// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func TestVideoQualitiesFrom_RanksBestFirst(t *testing.T) {
	got := videoQualitiesFrom([]Variant{
		{Height: 720, Bandwidth: 1800000, Codecs: "avc1.4d401f"},
		{Height: 1080, Bandwidth: 4200000, Codecs: "avc1.640028"},
		{Height: 480, Bandwidth: 900000, Codecs: "avc1.42c01e"},
	})

	want := []int{1080, 720, 480}
	if len(got) != len(want) {
		t.Fatalf("want %d options, got %d", len(want), len(got))
	}
	for i, h := range want {
		if got[i].Height != h {
			t.Errorf("option %d: want %dp, got %dp", i, h, got[i].Height)
		}
	}
}

// A source usually lists several bitrates of the same resolution and codec.
// They are indistinguishable when picking from a list, and keeping them would
// also make two options share one Quality selector.
func TestVideoQualitiesFrom_CollapsesSameResolutionAndCodec(t *testing.T) {
	got := videoQualitiesFrom([]Variant{
		{Height: 1080, Bandwidth: 3000000, Codecs: "avc1.640028"},
		{Height: 1080, Bandwidth: 4200000, Codecs: "avc1.640028"},
		{Height: 1080, Bandwidth: 2500000, Codecs: "avc1.640028"},
	})

	if len(got) != 1 {
		t.Fatalf("want 1 collapsed option, got %d: %+v", len(got), got)
	}
	// The survivor must be the highest bitrate of the group.
	if got[0].BitrateKbps != 4200 {
		t.Errorf("want the 4200 kbps variant kept, got %d", got[0].BitrateKbps)
	}
}

// The same resolution in a different codec is a genuinely different choice.
func TestVideoQualitiesFrom_KeepsCodecsApart(t *testing.T) {
	got := videoQualitiesFrom([]Variant{
		{Height: 1080, Bandwidth: 4200000, Codecs: "avc1.640028"},
		{Height: 1080, Bandwidth: 2500000, Codecs: "hvc1.1.6.L120"},
	})

	if len(got) != 2 {
		t.Fatalf("want both codecs, got %d: %+v", len(got), got)
	}
	codecs := map[string]bool{got[0].Codec: true, got[1].Codec: true}
	if !codecs["h264"] || !codecs["h265"] {
		t.Errorf("want h264 and h265, got %+v", codecs)
	}
}

// Every option must round-trip through SelectVariant back to itself: the menu
// hands its choice on as an ordinary --quality string, so a selector that
// resolves elsewhere would silently download the wrong rendition.
func TestVideoQualitiesFrom_SelectorRoundTrips(t *testing.T) {
	variants := []Variant{
		{Height: 1080, Bandwidth: 4200000, Codecs: "avc1.640028", URL: "1080-h264"},
		{Height: 1080, Bandwidth: 2500000, Codecs: "hvc1.1.6.L120", URL: "1080-h265"},
		{Height: 720, Bandwidth: 1800000, Codecs: "avc1.4d401f", URL: "720-h264"},
		{Height: 480, Bandwidth: 900000, Codecs: "avc1.42c01e", URL: "480-h264"},
	}

	for _, q := range videoQualitiesFrom(variants) {
		picked, err := SelectVariant(variants, q.Quality)
		if err != nil {
			t.Errorf("%s: %v", q.Quality, err)
			continue
		}
		gotCodec := "h264"
		if picked.IsH265() {
			gotCodec = "h265"
		}
		if picked.Height != q.Height || gotCodec != q.Codec {
			t.Errorf("selector %q resolved to %dp/%s, want %dp/%s",
				q.Quality, picked.Height, gotCodec, q.Height, q.Codec)
		}
	}
}

func TestVideoQualitiesFrom_Empty(t *testing.T) {
	if got := videoQualitiesFrom(nil); len(got) != 0 {
		t.Errorf("want no options, got %+v", got)
	}
}

// Indices must be contiguous from zero: the chooser returns a position in this
// slice, so a gap would select the wrong entry.
func TestVideoQualitiesFrom_IndicesAreContiguous(t *testing.T) {
	got := videoQualitiesFrom([]Variant{
		{Height: 1080, Bandwidth: 4200000, Codecs: "avc1.640028"},
		{Height: 1080, Bandwidth: 3000000, Codecs: "avc1.640028"}, // collapsed away
		{Height: 720, Bandwidth: 1800000, Codecs: "avc1.4d401f"},
	})

	for i, q := range got {
		if q.Index != i {
			t.Errorf("option %d carries Index %d", i, q.Index)
		}
	}
}

func TestVideoQualityInfo_Label(t *testing.T) {
	q := domain.VideoQualityInfo{Height: 1080, Codec: "h265", BitrateKbps: 2500}
	if got, want := q.Label(), "1080p/h265 (2500 kbps)"; got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}
