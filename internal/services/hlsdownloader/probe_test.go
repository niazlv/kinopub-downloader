// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// probeSite serves a master with one variant, two audio tracks and one
// subtitle track. Segment sizes are chosen so the sampled bitrates are exact:
// audio segments of 10 s and 160 000 bytes give 128 kbps, subtitle segments of
// 10 s and 1 250 bytes give 1 kbps. The second audio track refuses HEAD, so the
// ranged-GET fallback is exercised too; with brokenSecondAudio its playlist
// points at a segment that does not exist.
func probeSite(t *testing.T, brokenSecondAudio bool) *httptest.Server {
	t.Helper()
	playlist := func(prefix string, segments int) string {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n")
		for i := 0; i < segments; i++ {
			b.WriteString("#EXTINF:10.0,\n")
			b.WriteString(prefix + "-" + string(rune('0'+i)) + ".ts\n")
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		return b.String()
	}
	segment := func(w http.ResponseWriter, r *http.Request, size int, allowHead bool) {
		if r.Method == http.MethodHead && !allowHead {
			http.Error(w, "no HEAD here", http.StatusMethodNotAllowed)
			return
		}
		http.ServeContent(w, r, "seg.ts", time.Time{}, bytes.NewReader(make([]byte, size)))
	}
	a2 := playlist("a2", 6)
	if brokenSecondAudio {
		a2 = "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10.0,\nmissing.ts\n#EXT-X-ENDLIST\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/master.m3u8":
			w.Write([]byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio1080",NAME="01. StudioBand (RUS)",LANGUAGE="rus",CHANNELS="2",DEFAULT=YES,URI="a1.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio1080",NAME="02. Original (JPN)",LANGUAGE="jpn",DEFAULT=NO,URI="a2.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="RUS",LANGUAGE="rus",URI="s1.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=3805000,RESOLUTION=1920x1080,FRAME-RATE=24.000,CODECS="avc1.640028,mp4a.40.2",AUDIO="audio1080",SUBTITLES="subs"
1080.m3u8
`))
		case p == "/a1.m3u8":
			w.Write([]byte(playlist("a1", 6)))
		case p == "/a2.m3u8":
			w.Write([]byte(a2))
		case p == "/s1.m3u8":
			w.Write([]byte(playlist("s1", 6)))
		case strings.HasPrefix(p, "/a1-"):
			segment(w, r, 160000, true)
		case strings.HasPrefix(p, "/a2-"):
			segment(w, r, 160000, false)
		case strings.HasPrefix(p, "/s1-"):
			segment(w, r, 1250, true)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeTrackStats_SamplesBitrateAndProjectsSize(t *testing.T) {
	srv := probeSite(t, false)
	d := New(srv.Client(), domain.RequestAuth{}, nopLogger{})

	audio, subs, err := d.ProbeTrackStats(context.Background(), srv.URL+"/master.m3u8", "1080p")
	if err != nil {
		t.Fatalf("ProbeTrackStats: %v", err)
	}
	if len(audio) != 2 || len(subs) != 1 {
		t.Fatalf("got %d audio and %d subtitle stats", len(audio), len(subs))
	}
	for i, a := range audio {
		if a.BitrateKbps != 128 || a.SizeBytes != 960000 || a.Duration != 60*time.Second {
			t.Errorf("audio %d: %+v, want 128 kbps, 960000 bytes, 60s", i+1, a)
		}
		if a.Codec != "mp4a.40.2" {
			t.Errorf("audio %d: codec %q, want mp4a.40.2 from the variant's CODECS", i+1, a.Codec)
		}
	}
	if audio[0].Channels != 2 || audio[1].Channels != 0 {
		t.Errorf("channels = %d/%d, want 2 and unknown", audio[0].Channels, audio[1].Channels)
	}
	if subs[0].BitrateKbps != 1 || subs[0].SizeBytes != 7500 {
		t.Errorf("subtitles: %+v, want 1 kbps and 7500 bytes", subs[0])
	}
}

// A track that cannot be sampled stays blank; the others are still reported,
// and the codec, which comes from the master, survives the failed sample.
func TestProbeTrackStats_OneFailureLeavesOneBlank(t *testing.T) {
	srv := probeSite(t, true)
	d := New(srv.Client(), domain.RequestAuth{}, nopLogger{})

	audio, _, err := d.ProbeTrackStats(context.Background(), srv.URL+"/master.m3u8", "1080p")
	if err != nil {
		t.Fatalf("ProbeTrackStats: %v", err)
	}
	if audio[0].BitrateKbps != 128 {
		t.Errorf("healthy track lost its stats: %+v", audio[0])
	}
	if audio[1].BitrateKbps != 0 || audio[1].SizeBytes != 0 {
		t.Errorf("broken track reported stats: %+v", audio[1])
	}
	if audio[1].Codec != "mp4a.40.2" {
		t.Errorf("codec must survive a failed sample: %+v", audio[1])
	}
}

func TestParseMasterPlaylist_FrameRateAndChannels(t *testing.T) {
	master, err := parseMasterPlaylist(strings.NewReader(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Atmos",CHANNELS="6/JOC",URI="a.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=1280x720,FRAME-RATE=23.976,AUDIO="a"
v.m3u8
`), "https://cdn.example/")
	if err != nil {
		t.Fatal(err)
	}
	if master.Variants[0].FrameRate != 23.976 {
		t.Errorf("frame rate = %v, want 23.976", master.Variants[0].FrameRate)
	}
	if master.Audio[0].Channels != 6 {
		t.Errorf("channels = %d, want 6 (the JOC suffix dropped)", master.Audio[0].Channels)
	}
}

func TestAudioCodecOf(t *testing.T) {
	cases := map[string]string{
		"avc1.640028,mp4a.40.2":   "mp4a.40.2",
		"hvc1.1.6.L120.90,ec-3":   "ec-3",
		"avc1.640028":             "",
		"mp4a.40.2, avc1.4d401f":  "mp4a.40.2",
		"av01.0.08M.08,opus,ac-3": "opus,ac-3",
		"":                        "",
	}
	for in, want := range cases {
		if got := audioCodecOf(in); got != want {
			t.Errorf("audioCodecOf(%q) = %q, want %q", in, got, want)
		}
	}
}
