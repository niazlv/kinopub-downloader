// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

const (
	// probeSegments is how many leading segments are measured per track. The
	// first segment alone can be atypical (a short lead-in), so two are sampled.
	probeSegments = 2
	// probeConcurrency bounds the parallel requests of one probe: a listing
	// must not look like a download to the CDN.
	probeConcurrency = 4
	// probeTimeout caps one track's sampling; a slow track simply stays unknown.
	probeTimeout = 15 * time.Second
)

// ProbeTrackStats implements domain.HLSDownloader.
//
// The master playlist states bandwidth only for video variants; audio and
// subtitle renditions carry none. Their media playlists do give the duration,
// and the size of the first segments gives a bitrate, which projects a size
// good enough for a listing. Every track is sampled independently, so one
// failure leaves one row blank rather than emptying the table.
func (d *Downloader) ProbeTrackStats(ctx context.Context, manifestURL string, quality domain.Quality) ([]domain.TrackStats, []domain.TrackStats, error) {
	master, err := FetchMasterPlaylist(ctx, d.client, manifestURL, d.auth, d.logger)
	if err != nil {
		return nil, nil, fmt.Errorf("master playlist: %w", err)
	}
	if len(master.Variants) == 0 {
		return nil, nil, fmt.Errorf("no variants found in master playlist")
	}
	selected, err := SelectVariant(master.Variants, quality)
	if err != nil {
		return nil, nil, fmt.Errorf("quality selection: %w", err)
	}

	audioRenditions := audioRenditionsFor(master, selected)
	subsRenditions := subtitleRenditionsFor(master, selected)
	audio := make([]domain.TrackStats, len(audioRenditions))
	subs := make([]domain.TrackStats, len(subsRenditions))

	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)
	sample := func(uri string, out *domain.TrackStats) {
		defer wg.Done()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-sem }()
		stats, err := d.sampleRendition(ctx, uri)
		if err != nil {
			d.logger.Debug("track probe failed", domain.F("url", uri), domain.F("error", err.Error()))
			return
		}
		out.BitrateKbps, out.SizeBytes, out.Duration = stats.BitrateKbps, stats.SizeBytes, stats.Duration
	}

	codec := audioCodecOf(selected.Codecs)
	for i, r := range audioRenditions {
		audio[i] = domain.TrackStats{Codec: codec, Channels: r.Channels}
		wg.Add(1)
		go sample(r.URI, &audio[i])
	}
	for i, r := range subsRenditions {
		wg.Add(1)
		go sample(r.URI, &subs[i])
	}
	wg.Wait()
	return audio, subs, nil
}

// sampleRendition reads one media playlist and measures its first segments.
func (d *Downloader) sampleRendition(ctx context.Context, playlistURL string) (domain.TrackStats, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	playlist, err := FetchMediaPlaylist(ctx, d.client, playlistURL, d.auth)
	if err != nil {
		return domain.TrackStats{}, err
	}
	if len(playlist.Segments) == 0 || playlist.TotalDuration <= 0 {
		return domain.TrackStats{}, fmt.Errorf("media playlist has no segments")
	}

	var sampledBytes int64
	var sampledSeconds float64
	for _, seg := range playlist.Segments[:min(probeSegments, len(playlist.Segments))] {
		n, err := d.segmentSize(ctx, seg.URL)
		if err != nil {
			return domain.TrackStats{}, err
		}
		sampledBytes += n
		sampledSeconds += seg.Duration
	}
	if sampledSeconds <= 0 {
		return domain.TrackStats{}, fmt.Errorf("sampled segments have no duration")
	}

	bytesPerSecond := float64(sampledBytes) / sampledSeconds
	return domain.TrackStats{
		BitrateKbps: int(math.Round(bytesPerSecond * 8 / 1000)),
		SizeBytes:   int64(math.Round(bytesPerSecond * playlist.TotalDuration)),
		Duration:    time.Duration(playlist.TotalDuration * float64(time.Second)),
	}, nil
}

// segmentSize learns a segment's size without downloading it: HEAD first, and
// when the CDN answers HEAD without a length, a one-byte ranged GET whose
// Content-Range states the total.
func (d *Downloader) segmentSize(ctx context.Context, segURL string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, segURL, nil)
	if err != nil {
		return 0, err
	}
	applyHLSAuth(req, d.auth)
	if resp, err := d.client.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
			return resp.ContentLength, nil
		}
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, segURL, nil)
	if err != nil {
		return 0, err
	}
	applyHLSAuth(req, d.auth)
	req.Header.Set("Range", "bytes=0-0")
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Content-Range: bytes 0-0/12345
		_, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
		if !ok {
			return 0, fmt.Errorf("segment: no Content-Range in a partial response")
		}
		return strconv.ParseInt(strings.TrimSpace(total), 10, 64)
	case http.StatusOK:
		if resp.ContentLength > 0 {
			return resp.ContentLength, nil
		}
		return 0, fmt.Errorf("segment: no Content-Length")
	default:
		return 0, fmt.Errorf("segment: HTTP %d", resp.StatusCode)
	}
}

// audioCodecOf picks the audio entries out of a variant's CODECS attribute
// ("avc1.640028,mp4a.40.2" → "mp4a.40.2"): the master declares codecs per
// variant, and the audio group the variant references shares them.
func audioCodecOf(codecs string) string {
	var audio []string
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		if c == "" || isVideoCodec(c) {
			continue
		}
		audio = append(audio, c)
	}
	return strings.Join(audio, ",")
}

// isVideoCodec reports whether an RFC 6381 codec string names a video codec.
func isVideoCodec(c string) bool {
	for _, prefix := range []string{"avc1", "avc3", "hvc1", "hev1", "hevc", "av01", "vp09", "vp08", "vp8", "vp9", "dvh1", "dvhe"} {
		if strings.HasPrefix(strings.ToLower(c), prefix) {
			return true
		}
	}
	return false
}
