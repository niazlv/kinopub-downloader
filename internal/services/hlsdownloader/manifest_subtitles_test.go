// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"strings"
	"testing"
)

const masterWithSubs = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="AniLibria",LANGUAGE="rus",URI="audio/rus.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Русские полные",LANGUAGE="rus",DEFAULT=YES,AUTOSELECT=YES,URI="subs/rus.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Русские форсированные",LANGUAGE="rus",FORCED=YES,URI="subs/rus_forced.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="eng",URI="subs/eng.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=4200000,RESOLUTION=1920x1080,CODECS="avc1.640028",AUDIO="aud",SUBTITLES="subs"
video/1080.m3u8
`

func parseMaster(t *testing.T, body, base string) *MasterPlaylist {
	t.Helper()
	m, err := parseMasterPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func TestParseMaster_SubtitleRenditions(t *testing.T) {
	m := parseMaster(t, masterWithSubs, "https://cdn.example/hls/master.m3u8")

	if len(m.Subtitles) != 3 {
		t.Fatalf("want 3 subtitle renditions, got %d", len(m.Subtitles))
	}
	// Audio parsing must be unaffected by the new branch.
	if len(m.Audio) != 1 {
		t.Fatalf("want 1 audio rendition, got %d", len(m.Audio))
	}

	first := m.Subtitles[0]
	if first.Name != "Русские полные" || first.Language != "rus" || first.GroupID != "subs" {
		t.Errorf("unexpected first rendition: %+v", first)
	}
	if !first.Default {
		t.Error("DEFAULT=YES must set Default")
	}
	if first.Forced {
		t.Error("absent FORCED must leave Forced false")
	}
	if want := "https://cdn.example/hls/subs/rus.m3u8"; first.URI != want {
		t.Errorf("URI: want %q, got %q", want, first.URI)
	}
	if !m.Subtitles[1].Forced {
		t.Error("FORCED=YES must set Forced")
	}
}

func TestParseMaster_VariantLinksSubtitleGroup(t *testing.T) {
	m := parseMaster(t, masterWithSubs, "https://cdn.example/hls/master.m3u8")

	if len(m.Variants) != 1 {
		t.Fatalf("want 1 variant, got %d", len(m.Variants))
	}
	if got := m.Variants[0].SubsGroup; got != "subs" {
		t.Errorf("SubsGroup: want %q, got %q", "subs", got)
	}
	if got := m.Variants[0].AudioGroup; got != "aud" {
		t.Errorf("AudioGroup: want %q, got %q", "aud", got)
	}
}

// A master playlist without any SUBTITLES rendition must stay valid — most
// sources have none, and that is not an error.
func TestParseMaster_NoSubtitles(t *testing.T) {
	const body = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1280x720,CODECS="avc1.4d401f"
video/720.m3u8
`
	m := parseMaster(t, body, "https://cdn.example/hls/master.m3u8")
	if len(m.Subtitles) != 0 {
		t.Errorf("want no subtitles, got %d", len(m.Subtitles))
	}
	if m.Variants[0].SubsGroup != "" {
		t.Errorf("want empty SubsGroup, got %q", m.Variants[0].SubsGroup)
	}
}

// TYPE values this downloader does not handle must be ignored rather than
// misfiled as audio or subtitles.
func TestParseMaster_IgnoresOtherMediaTypes(t *testing.T) {
	const body = `#EXTM3U
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="CC1",INSTREAM-ID="CC1"
#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1280x720
video/720.m3u8
`
	m := parseMaster(t, body, "https://cdn.example/hls/master.m3u8")
	if len(m.Subtitles) != 0 || len(m.Audio) != 0 {
		t.Errorf("CLOSED-CAPTIONS must be ignored: subs=%d audio=%d", len(m.Subtitles), len(m.Audio))
	}
}

func TestSubtitleRenditionsFor(t *testing.T) {
	m := parseMaster(t, masterWithSubs, "https://cdn.example/hls/master.m3u8")

	got := subtitleRenditionsFor(m, m.Variants[0])
	if len(got) != 3 {
		t.Fatalf("want 3 renditions for group %q, got %d", m.Variants[0].SubsGroup, len(got))
	}

	// A variant with no subtitle group selects nothing.
	if got := subtitleRenditionsFor(m, Variant{}); len(got) != 0 {
		t.Errorf("want none for empty SubsGroup, got %d", len(got))
	}
	// Renditions of a different group must not leak in.
	if got := subtitleRenditionsFor(m, Variant{SubsGroup: "other"}); len(got) != 0 {
		t.Errorf("want none for unknown group, got %d", len(got))
	}
}

// An entry without a URI cannot be fetched, so it must be dropped rather than
// producing a rendition that fails later.
func TestSubtitleRenditionsFor_DropsEntriesWithoutURI(t *testing.T) {
	const body = `#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Broken",LANGUAGE="rus"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Good",LANGUAGE="eng",URI="subs/eng.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=1280x720,SUBTITLES="subs"
video/720.m3u8
`
	m := parseMaster(t, body, "https://cdn.example/hls/master.m3u8")
	got := subtitleRenditionsFor(m, m.Variants[0])
	if len(got) != 1 {
		t.Fatalf("want 1 usable rendition, got %d", len(got))
	}
	if got[0].Name != "Good" {
		t.Errorf("kept the wrong rendition: %+v", got[0])
	}
}

// kinopub declares the same tracks once per codec family, reusing one group
// id, so a group can hold entries a viewer cannot tell apart. Muxing them all
// would put the same speech (or the same subtitles) into the file twice.
func TestRenditionsFor_CollapsesIndistinguishableDuplicates(t *testing.T) {
	const body = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a480",NAME="01. Одноголосый (RUS)",LANGUAGE="rus",URI="audio/rus-hevc.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a480",NAME="02. Оригинал (ENG)",LANGUAGE="eng",URI="audio/eng-hevc.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a480",NAME="01. Одноголосый (RUS)",LANGUAGE="rus",URI="audio/rus-h264.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a480",NAME="02. Оригинал (ENG)",LANGUAGE="eng",URI="audio/eng-h264.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="RUS #02",LANGUAGE="rus",URI="subs/rus.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="RUS #02",LANGUAGE="rus",URI="subs/rus-again.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=720x524,AUDIO="a480",SUBTITLES="sub"
video/480.m3u8
`
	m := parseMaster(t, body, "https://cdn.example/hls/master.m3u8")

	audio := audioRenditionsFor(m, m.Variants[0])
	if len(audio) != 2 {
		t.Fatalf("want 2 distinct audio renditions, got %d: %+v", len(audio), audio)
	}
	// The first declaration wins, so the order a viewer sees is preserved.
	if audio[0].URI != "https://cdn.example/hls/audio/rus-hevc.m3u8" {
		t.Errorf("kept the wrong copy: %q", audio[0].URI)
	}
	if audio[0].Name == audio[1].Name {
		t.Errorf("both renditions carry the same name: %q", audio[0].Name)
	}

	subs := subtitleRenditionsFor(m, m.Variants[0])
	if len(subs) != 1 {
		t.Fatalf("want 1 distinct subtitle rendition, got %d: %+v", len(subs), subs)
	}

	// Tracks that differ only by name stay separate: they are distinguishable.
	const distinct = `#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="ENG #05",LANGUAGE="eng",URI="subs/e5.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="ENG #06",LANGUAGE="eng",URI="subs/e6.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1800000,RESOLUTION=720x524,SUBTITLES="sub"
video/480.m3u8
`
	d := parseMaster(t, distinct, "https://cdn.example/hls/master.m3u8")
	if got := subtitleRenditionsFor(d, d.Variants[0]); len(got) != 2 {
		t.Errorf("same-language tracks with different names must both survive, got %d", len(got))
	}
}
