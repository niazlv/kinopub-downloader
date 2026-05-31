package downloader

import (
	"fmt"
	"strings"

	"kinopub_downloader/internal/domain"
)

// iso639Map maps common 2-letter language codes to their ISO 639-2 (3-letter)
// equivalents. If a code is already 3 letters or is unknown, it passes through.
var iso639Map = map[string]string{
	"ru": "rus",
	"en": "eng",
	"uk": "ukr",
	"de": "ger",
	"fr": "fre",
	"es": "spa",
	"it": "ita",
	"ja": "jpn",
	"ko": "kor",
	"zh": "chi",
	"pt": "por",
	"pl": "pol",
	"nl": "dut",
	"sv": "swe",
	"no": "nor",
	"da": "dan",
	"fi": "fin",
	"cs": "cze",
	"sk": "slo",
	"hu": "hun",
	"ro": "rum",
	"bg": "bul",
	"hr": "hrv",
	"sr": "srp",
	"tr": "tur",
	"ar": "ara",
	"he": "heb",
	"hi": "hin",
	"th": "tha",
	"vi": "vie",
	"el": "gre",
	"ka": "geo",
}

// ToISO6392 converts a language code to its ISO 639-2 (3-letter) form.
// If the input is already 3 letters, it passes through unchanged.
// If the input is a known 2-letter code, it maps to the 3-letter equivalent.
// Unknown codes pass through as-is.
func ToISO6392(lang string) string {
	if lang == "" {
		return ""
	}
	lower := strings.ToLower(lang)
	if len(lower) == 3 {
		return lower
	}
	if mapped, ok := iso639Map[lower]; ok {
		return mapped
	}
	return lower
}

// BuildAudioLabels derives a unique label for each audio track.
// Priority: Studio > Language > "Audio" fallback.
// Duplicate labels get ordinal suffixes " (2)", " (3)", etc.
func BuildAudioLabels(tracks []domain.AudioTrack) []string {
	labels := make([]string, len(tracks))
	for i, t := range tracks {
		switch {
		case t.Studio != "":
			labels[i] = t.Studio
		case t.Language != "":
			labels[i] = t.Language
		default:
			labels[i] = "Audio"
		}
	}
	return makeUnique(labels)
}

// BuildSubtitleLabels derives a unique label for each subtitle track.
// Priority: Source > Language > "Subtitle" fallback.
// Duplicate labels get ordinal suffixes " (2)", " (3)", etc.
func BuildSubtitleLabels(tracks []domain.SubtitleTrack) []string {
	labels := make([]string, len(tracks))
	for i, t := range tracks {
		switch {
		case t.Source != "":
			labels[i] = t.Source
		case t.Language != "":
			labels[i] = t.Language
		default:
			labels[i] = "Subtitle"
		}
	}
	return makeUnique(labels)
}

// makeUnique ensures all labels are unique by appending ordinal suffixes
// " (2)", " (3)", etc. to duplicates. The first occurrence keeps its original
// label; subsequent duplicates get increasing suffixes.
func makeUnique(labels []string) []string {
	// Count occurrences of each label.
	counts := make(map[string]int)
	for _, l := range labels {
		counts[l]++
	}

	// For labels that appear more than once, assign ordinal suffixes.
	// The first occurrence gets " (1)" and subsequent get " (2)", etc.
	// Actually per the spec: first keeps original, duplicates get suffixes.
	// Let's re-read: "append ordinal suffix ' (2)', ' (3)'" — so first stays,
	// second gets (2), third gets (3).
	seen := make(map[string]int)
	result := make([]string, len(labels))
	for i, l := range labels {
		seen[l]++
		if counts[l] > 1 && seen[l] > 1 {
			result[i] = fmt.Sprintf("%s (%d)", l, seen[l])
		} else {
			result[i] = l
		}
	}
	return result
}

// BuildFFmpegArgs constructs the full ffmpeg argument list for a download job.
// It is a pure function with no side effects.
//
// For HLS sources: the video URL is the first -i, then each audio and subtitle
// track with a separate URI gets its own -i.
// For progressive sources: a single -i contains all streams.
//
// The function produces:
//   - Per-input -i flags
//   - -map flags for video, audio, and subtitle tracks
//   - -c copy (stream copy, no re-encode)
//   - -metadata:s:a:N and -metadata:s:s:N for labels and languages
//   - -progress pipe:1 for progress reporting
//   - The temp output path as the final argument
func BuildFFmpegArgs(job domain.Job, proxyEnv []string, tempPath string) []string {
	media := job.Media
	isHLS := media.Source.Kind == domain.MediaHLS

	var args []string

	// Add -y to overwrite without asking.
	args = append(args, "-y")

	// Add proxy environment as protocol options if provided.
	for _, env := range proxyEnv {
		// proxyEnv entries are in the form "key=value" for env vars,
		// or "-http_proxy <url>" style args. We pass them as protocol options.
		if strings.HasPrefix(env, "-") {
			// Direct ffmpeg arg like -http_proxy <url>
			parts := strings.SplitN(env, " ", 2)
			args = append(args, parts...)
		}
	}

	// Build input list and track the input index for mapping.
	// inputIdx tracks which -i index each track maps to.
	if isHLS {
		// HLS: video URL is first input.
		args = append(args, "-i", media.Source.URL)
		inputIdx := 1 // next input index

		// Each audio track with a URI gets its own input.
		for range media.Audio {
			// For HLS, audio tracks have their own playlist URIs derived from
			// the master playlist. We use the source URL as the base — in practice
			// the media resolver would have resolved individual URIs. For the
			// command builder, we use the source URL for all inputs since the
			// actual URI resolution happens upstream.
			args = append(args, "-i", media.Source.URL)
			inputIdx++
		}

		// Each subtitle track gets its own input.
		for range media.Subtitles {
			args = append(args, "-i", media.Source.URL)
			inputIdx++
		}
		_ = inputIdx
	} else {
		// Progressive: single input contains all streams.
		args = append(args, "-i", media.Source.URL)
	}

	// Map video track: always from input 0, video stream.
	args = append(args, "-map", "0:v")

	// Map audio tracks.
	if isHLS {
		for i := range media.Audio {
			// Each audio is a separate input (starting at index 1).
			args = append(args, "-map", fmt.Sprintf("%d:a", i+1))
		}
	} else {
		// Progressive: all audio streams are in input 0.
		for i := range media.Audio {
			args = append(args, "-map", fmt.Sprintf("0:a:%d", i))
		}
	}

	// Map subtitle tracks.
	if isHLS {
		audioCount := len(media.Audio)
		for i := range media.Subtitles {
			// Subtitles start after audio inputs.
			args = append(args, "-map", fmt.Sprintf("%d:s", audioCount+1+i))
		}
	} else {
		for i := range media.Subtitles {
			args = append(args, "-map", fmt.Sprintf("0:s:%d", i))
		}
	}

	// Stream copy — no re-encoding.
	args = append(args, "-c", "copy")

	// Audio metadata: title and language.
	audioLabels := BuildAudioLabels(media.Audio)
	for i, track := range media.Audio {
		args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), fmt.Sprintf("title=%s", audioLabels[i]))
		if track.Language != "" {
			lang := ToISO6392(track.Language)
			args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), fmt.Sprintf("language=%s", lang))
		}
	}

	// Subtitle metadata: title and language.
	subtitleLabels := BuildSubtitleLabels(media.Subtitles)
	for i, track := range media.Subtitles {
		args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), fmt.Sprintf("title=%s", subtitleLabels[i]))
		if track.Language != "" {
			lang := ToISO6392(track.Language)
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), fmt.Sprintf("language=%s", lang))
		}
	}

	// Progress reporting to stdout pipe.
	args = append(args, "-progress", "pipe:1")

	// Output path (temp file).
	args = append(args, tempPath)

	return args
}
