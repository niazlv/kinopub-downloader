package mediaresolver

import (
	"context"
	"encoding/json"
	"fmt"

	"kinopub_downloader/internal/domain"
)

// ffprobeOutput represents the JSON output of ffprobe -show_streams -print_format json.
type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
}

// ffprobeStream represents a single stream from ffprobe output.
type ffprobeStream struct {
	Index     int    `json:"index"`
	CodecType string `json:"codec_type"` // "video", "audio", "subtitle"
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BitRate   string `json:"bit_rate"`
	Tags      struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
}

// resolveProgressive runs ffprobe on a progressive (direct) stream to
// enumerate video and audio tracks (Req 3.2).
func (r *Resolver) resolveProgressive(ctx context.Context, source domain.MediaSource) (domain.ResolvedMedia, error) {
	r.logger.Debug("probing progressive stream", domain.F("url", source.URL))

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		source.URL,
	}

	output, err := r.runOutput(ctx, "ffprobe", args, nil)
	if err != nil {
		return domain.ResolvedMedia{}, fmt.Errorf("ffprobe: %w", err)
	}

	return parseFFprobeOutput(output, source)
}

// parseFFprobeOutput parses ffprobe JSON output into a ResolvedMedia.
func parseFFprobeOutput(data []byte, source domain.MediaSource) (domain.ResolvedMedia, error) {
	var probe ffprobeOutput
	if err := json.Unmarshal(data, &probe); err != nil {
		return domain.ResolvedMedia{}, fmt.Errorf("parse ffprobe output: %w", err)
	}

	var video domain.VideoTrack
	var audioTracks []domain.AudioTrack
	var hasVideo bool

	audioIndex := 0

	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			if !hasVideo {
				video = domain.VideoTrack{
					Index:      stream.Index,
					Resolution: fmt.Sprintf("%dx%d", stream.Width, stream.Height),
				}
				hasVideo = true
			}
		case "audio":
			track := domain.AudioTrack{
				Index:    audioIndex,
				Language: stream.Tags.Language,
				Studio:   stream.Tags.Title,
			}
			audioTracks = append(audioTracks, track)
			audioIndex++
		}
	}

	if !hasVideo {
		return domain.ResolvedMedia{}, domain.ErrNoVideoTrack
	}

	return domain.ResolvedMedia{
		Source: source,
		Video:  video,
		Audio:  audioTracks,
		// Progressive sources don't have separate subtitle streams in the
		// same way HLS does — subtitle enumeration is HLS-only per design.
		Subtitles: nil,
	}, nil
}
