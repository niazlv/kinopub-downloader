// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func subsMuxJob() domain.Job {
	return domain.Job{
		Episode:     domain.Episode{Key: domain.EpisodeKey{Season: 1, Episode: 1}},
		OutPath:     "/out/S01E01.mkv",
		SeriesTitle: "Series",
	}
}

func hlsWithSubs() *domain.HLSDownloadResult {
	return &domain.HLSDownloadResult{
		Resolution:  "1920x1080",
		BitrateKbps: 4200,
		VideoPath:   "/tmp/video.ts",
		AudioTracks: []domain.HLSAudioTrack{
			{Path: "/tmp/audio_0.ts", Name: "AniLibria", Language: "rus"},
		},
		SubtitleTracks: []domain.HLSSubtitleTrack{
			{Path: "/tmp/subs_0.vtt", Name: "Русские полные", Language: "rus"},
			{Path: "/tmp/subs_1.vtt", Name: "English", Language: "eng"},
		},
	}
}

// argsAfter returns the argument following the first occurrence of flag.
func argsAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func joined(args []string) string { return strings.Join(args, " ") }

func TestBuildHLSMuxArgs_SubtitleInputsAndMaps(t *testing.T) {
	args := BuildHLSMuxArgs(subsMuxJob(), hlsWithSubs(), "/out/S01E01.mkv.tmp")
	got := joined(args)

	for _, path := range []string{"/tmp/subs_0.vtt", "/tmp/subs_1.vtt"} {
		if !strings.Contains(got, "-i "+path) {
			t.Errorf("subtitle input %q missing:\n%s", path, got)
		}
	}
	// Video is input 0, the single audio track is input 1, so subtitles start
	// at input 2. Getting this offset wrong silently maps the wrong stream.
	for _, want := range []string{"-map 2:s:0", "-map 3:s:0"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Video and audio maps must survive.
	if !strings.Contains(got, "-map 0:v:0") || !strings.Contains(got, "-map 1:a:0") {
		t.Errorf("video/audio maps disturbed:\n%s", got)
	}
}

// WebVTT cannot live in either container as-is, so the subtitle codec must be
// overridden per container while video and audio stay copied.
func TestBuildHLSMuxArgs_SubtitleCodecPerContainer(t *testing.T) {
	t.Run("mkv uses srt", func(t *testing.T) {
		args := BuildHLSMuxArgs(subsMuxJob(), hlsWithSubs(), "/out/S01E01.mkv.tmp")
		if got := argsAfter(args, "-c:s"); got != "srt" {
			t.Errorf("want srt, got %q", got)
		}
	})

	t.Run("mp4 uses mov_text", func(t *testing.T) {
		job := subsMuxJob()
		job.OutPath = "/out/S01E01.mp4"
		args := BuildHLSMuxArgs(job, hlsWithSubs(), "/out/S01E01.mp4.tmp")
		if got := argsAfter(args, "-c:s"); got != "mov_text" {
			t.Errorf("want mov_text, got %q", got)
		}
	})

	t.Run("streams are still copied", func(t *testing.T) {
		args := BuildHLSMuxArgs(subsMuxJob(), hlsWithSubs(), "/out/S01E01.mkv.tmp")
		if !strings.Contains(joined(args), "-c copy") {
			t.Errorf("stream copy lost:\n%s", joined(args))
		}
	})
}

func TestBuildHLSMuxArgs_SubtitleMetadata(t *testing.T) {
	args := BuildHLSMuxArgs(subsMuxJob(), hlsWithSubs(), "/out/S01E01.mkv.tmp")
	got := joined(args)

	if !strings.Contains(got, "-metadata:s:s:0 title=Русские полные") {
		t.Errorf("subtitle title missing:\n%s", got)
	}
	if !strings.Contains(got, "-metadata:s:s:0 language=rus") {
		t.Errorf("subtitle language missing:\n%s", got)
	}
	if !strings.Contains(got, "-metadata:s:s:1 language=eng") {
		t.Errorf("second subtitle language missing:\n%s", got)
	}
}

// Two tracks sharing a name would be indistinguishable in a player's menu.
func TestBuildHLSMuxArgs_SubtitleLabelsDeduplicated(t *testing.T) {
	hls := hlsWithSubs()
	hls.SubtitleTracks = []domain.HLSSubtitleTrack{
		{Path: "/tmp/subs_0.vtt", Name: "Русские", Language: "rus"},
		{Path: "/tmp/subs_1.vtt", Name: "Русские", Language: "rus"},
	}
	args := BuildHLSMuxArgs(subsMuxJob(), hls, "/out/S01E01.mkv.tmp")

	first := argsAfter(args, "-metadata:s:s:0")
	var second string
	for i, a := range args {
		if a == "-metadata:s:s:1" && i+1 < len(args) && strings.HasPrefix(args[i+1], "title=") {
			second = args[i+1]
			break
		}
	}
	if first == second {
		t.Errorf("duplicate subtitle labels not disambiguated: %q vs %q", first, second)
	}
}

// An episode with no subtitles must produce exactly the arguments it did before
// subtitle support existed — no stray -c:s, no stray maps.
func TestBuildHLSMuxArgs_NoSubtitlesIsUnchanged(t *testing.T) {
	hls := hlsWithSubs()
	hls.SubtitleTracks = nil
	got := joined(BuildHLSMuxArgs(subsMuxJob(), hls, "/out/S01E01.mkv.tmp"))

	if strings.Contains(got, "-c:s") {
		t.Errorf("unexpected subtitle codec flag:\n%s", got)
	}
	if strings.Contains(got, ":s:0") {
		t.Errorf("unexpected subtitle mapping:\n%s", got)
	}
}

// With no separate audio, subtitles shift down one input slot.
func TestBuildHLSMuxArgs_SubtitleIndexWithoutAudio(t *testing.T) {
	hls := hlsWithSubs()
	hls.AudioTracks = nil
	got := joined(BuildHLSMuxArgs(subsMuxJob(), hls, "/out/S01E01.mkv.tmp"))

	if !strings.Contains(got, "-map 1:s:0") || !strings.Contains(got, "-map 2:s:0") {
		t.Errorf("subtitle inputs not reindexed after dropping audio:\n%s", got)
	}
	if !strings.Contains(got, "-map 0:a?") {
		t.Errorf("muxed-audio fallback lost:\n%s", got)
	}
}
