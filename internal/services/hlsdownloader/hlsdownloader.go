// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package hlsdownloader implements HLS segment-based downloading with
// quality selection, per-segment retry, and resume capability.
package hlsdownloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/fsutil"
	"github.com/niazlv/kinopub-downloader/internal/lib/httpx"
	"github.com/niazlv/kinopub-downloader/internal/lib/webvtt"
)

const (
	// maxSegmentRetries is the max retry count for a single segment.
	maxSegmentRetries = 5

	// segmentRetryDelay is the base delay between segment retries.
	segmentRetryDelay = 2 * time.Second

	// defaultConcurrency is the default number of segments fetched in parallel
	// across all tracks of an episode.
	defaultConcurrency = 4

	// variantMarkerName is the file written inside the resume temp dir to record
	// which renditions the cached segments were downloaded from.
	variantMarkerName = "variant.id"
)

// Downloader downloads HLS streams by fetching individual segments.
type Downloader struct {
	client      *http.Client
	auth        domain.RequestAuth
	logger      domain.Logger
	concurrency int
	proxyURL    *url.URL

	mu        sync.RWMutex
	audioPref domain.AudioPreference
	subsPref  domain.SubtitlePreference
}

// Option configures the Downloader.
type Option func(*Downloader)

// WithConcurrency sets the number of segments fetched in parallel across all
// tracks (video + audio) of an episode. Values < 1 fall back to the default.
func WithConcurrency(n int) Option {
	return func(d *Downloader) {
		if n >= 1 {
			d.concurrency = n
		}
	}
}

// WithProxy sets the proxy URL for CDN segment requests.
func WithProxy(proxyURL *url.URL) Option {
	return func(d *Downloader) {
		d.proxyURL = proxyURL
	}
}

// New creates a new HLS Downloader.
// It uses a browser-fingerprint HTTP client (uTLS) to bypass CDN throttling.
func New(client *http.Client, auth domain.RequestAuth, logger domain.Logger, opts ...Option) *Downloader {
	d := &Downloader{
		auth:        auth,
		logger:      logger.Component("hls"),
		concurrency: defaultConcurrency,
	}
	for _, o := range opts {
		o(d)
	}
	// Use browser-fingerprint client for CDN requests, routing through proxy if set.
	// The regular Go HTTP client gets throttled by Cloudflare/CDN due to
	// its distinctive TLS fingerprint.
	d.client = httpx.NewBrowserClient(d.proxyURL)
	return d
}

// DownloadResult contains info about the completed download (internal use).
type DownloadResult struct {
	SelectedVariant Variant
	TotalSegments   int
	TotalBytes      int64
}

// DownloadEpisode downloads an episode via HLS segments.
// It fetches the master playlist, selects quality, downloads all segments,
// concatenates them into a single .ts file at outPath.
//
// The caller is responsible for remuxing the .ts file into the final container.
func (d *Downloader) DownloadEpisode(
	ctx context.Context,
	manifestURL string,
	quality domain.Quality,
	outPath string,
	key domain.EpisodeKey,
	sink domain.ProgressSink,
) (*domain.HLSDownloadResult, error) {
	return d.downloadEpisodeInternal(ctx, manifestURL, quality, outPath, key, sink)
}

// SetAudioPreference sets the audio-track filter applied to subsequent
// DownloadEpisode calls. Safe for concurrent use.
func (d *Downloader) SetAudioPreference(pref domain.AudioPreference) {
	d.mu.Lock()
	d.audioPref = pref
	d.mu.Unlock()
}

// audioPreference returns the current audio preference under a read lock.
func (d *Downloader) audioPreference() domain.AudioPreference {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.audioPref
}

// ListAudioTracks fetches the master playlist and reports the audio renditions
// available for the selected quality variant, without downloading segments.
func (d *Downloader) ListAudioTracks(ctx context.Context, manifestURL string, quality domain.Quality) ([]domain.AudioTrackInfo, error) {
	master, err := FetchMasterPlaylist(ctx, d.client, manifestURL, d.auth, d.logger)
	if err != nil {
		return nil, fmt.Errorf("master playlist: %w", err)
	}
	if len(master.Variants) == 0 {
		return nil, fmt.Errorf("no variants found in master playlist")
	}
	selected, err := SelectVariant(master.Variants, quality)
	if err != nil {
		return nil, fmt.Errorf("quality selection: %w", err)
	}
	renditions := audioRenditionsFor(master, selected)
	infos := make([]domain.AudioTrackInfo, len(renditions))
	for i, a := range renditions {
		infos[i] = domain.AudioTrackInfo{Index: i, Name: a.Name, Language: a.Language}
	}
	return infos, nil
}

// audioRenditionsFor returns the audio renditions belonging to the selected
// variant's audio group (in master-playlist order), excluding entries with no
// media URI. When the variant has no audio group, the result is empty (audio is
// muxed into the video stream).
func audioRenditionsFor(master *MasterPlaylist, selected Variant) []AudioRendition {
	var out []AudioRendition
	if selected.AudioGroup == "" {
		return out
	}
	for _, a := range master.Audio {
		if a.GroupID == selected.AudioGroup && a.URI != "" {
			out = append(out, a)
		}
	}
	return out
}

// SetSubtitlePreference sets the subtitle-track filter applied to subsequent
// DownloadEpisode calls. Safe for concurrent use.
func (d *Downloader) SetSubtitlePreference(pref domain.SubtitlePreference) {
	d.mu.Lock()
	d.subsPref = pref
	d.mu.Unlock()
}

// subtitlePreference returns the current subtitle preference under a read lock.
func (d *Downloader) subtitlePreference() domain.SubtitlePreference {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.subsPref
}

// ListSubtitleTracks fetches the master playlist and reports the subtitle
// renditions available for the selected quality variant, without downloading
// segments. An episode with no subtitles yields an empty list, not an error.
func (d *Downloader) ListSubtitleTracks(ctx context.Context, manifestURL string, quality domain.Quality) ([]domain.SubtitleTrackInfo, error) {
	master, err := FetchMasterPlaylist(ctx, d.client, manifestURL, d.auth, d.logger)
	if err != nil {
		return nil, fmt.Errorf("master playlist: %w", err)
	}
	if len(master.Variants) == 0 {
		return nil, fmt.Errorf("no variants found in master playlist")
	}
	selected, err := SelectVariant(master.Variants, quality)
	if err != nil {
		return nil, fmt.Errorf("quality selection: %w", err)
	}
	renditions := subtitleRenditionsFor(master, selected)
	infos := make([]domain.SubtitleTrackInfo, len(renditions))
	for i, s := range renditions {
		infos[i] = domain.SubtitleTrackInfo{Index: i, Name: s.DisplayName(), Language: s.Language}
	}
	return infos, nil
}

// subtitleRenditionsFor returns the subtitle renditions belonging to the
// selected variant's subtitle group (in master-playlist order), excluding
// entries with no media URI. When the variant has no subtitle group, the result
// is empty — unlike audio, subtitles are simply absent rather than muxed in.
func subtitleRenditionsFor(master *MasterPlaylist, selected Variant) []SubtitleRendition {
	var out []SubtitleRendition
	if selected.SubsGroup == "" {
		return out
	}
	for _, s := range master.Subtitles {
		if s.GroupID == selected.SubsGroup && s.URI != "" {
			out = append(out, s)
		}
	}
	return out
}

// downloadEpisodeInternal downloads video segments and (for demuxed HLS) audio
// segments separately. It returns the local paths so the caller can mux them
// together with ffmpeg. The caller is responsible for removing result.TempDir.
func (d *Downloader) downloadEpisodeInternal(
	ctx context.Context,
	manifestURL string,
	quality domain.Quality,
	outPath string,
	key domain.EpisodeKey,
	sink domain.ProgressSink,
) (*domain.HLSDownloadResult, error) {
	epLabel := key.Label()

	// 1. Fetch and parse master playlist.
	d.logger.Info("fetching HLS master playlist", domain.F("episode", epLabel))

	master, err := FetchMasterPlaylist(ctx, d.client, manifestURL, d.auth, d.logger)
	if err != nil {
		return nil, fmt.Errorf("master playlist: %w", err)
	}
	if len(master.Variants) == 0 {
		return nil, fmt.Errorf("no variants found in master playlist")
	}

	// Log available qualities.
	var qualityLabels []string
	for _, v := range master.Variants {
		qualityLabels = append(qualityLabels, v.Label())
	}
	d.logger.Info("available qualities",
		domain.F("episode", epLabel),
		domain.F("variants", strings.Join(qualityLabels, ", ")),
	)

	// 2. Select quality variant.
	selected, err := SelectVariant(master.Variants, quality)
	if err != nil {
		return nil, fmt.Errorf("quality selection: %w", err)
	}

	// Determine which audio renditions belong to the selected variant.
	allRenditions := audioRenditionsFor(master, selected)

	// Apply the audio-track preference (selection / filtering). The preference
	// is matched against rendition names and languages; an empty preference
	// keeps every track.
	pref := d.audioPreference()
	audioRenditions := allRenditions
	if !pref.IsAll() && len(allRenditions) > 0 {
		infos := make([]domain.AudioTrackInfo, len(allRenditions))
		for i, a := range allRenditions {
			infos[i] = domain.AudioTrackInfo{Index: i, Name: a.Name, Language: a.Language}
		}
		keep := domain.SelectAudio(infos, pref)
		filtered := make([]AudioRendition, 0, len(keep))
		var keptLabels []string
		for _, idx := range keep {
			filtered = append(filtered, allRenditions[idx])
			keptLabels = append(keptLabels, allRenditions[idx].Name)
		}
		audioRenditions = filtered
		d.logger.Info("audio tracks selected",
			domain.F("episode", epLabel),
			domain.F("available", len(allRenditions)),
			domain.F("kept", len(audioRenditions)),
			domain.F("tracks", strings.Join(keptLabels, " | ")),
		)
	}

	// Determine which subtitle renditions belong to the selected variant and
	// apply the subtitle preference. Unlike audio, an episode may legitimately
	// have none — subtitles are optional, so an empty result is not an error
	// here. In strict mode (--subs-only) the caller turns it into one.
	subsPref := d.subtitlePreference()
	allSubs := subtitleRenditionsFor(master, selected)
	subsRenditions := allSubs
	if !subsPref.IsAll() && len(allSubs) > 0 {
		infos := make([]domain.SubtitleTrackInfo, len(allSubs))
		for i, s := range allSubs {
			infos[i] = domain.SubtitleTrackInfo{Index: i, Name: s.DisplayName(), Language: s.Language}
		}
		keep := domain.SelectSubtitles(infos, subsPref, subsPref.Strict)
		filtered := make([]SubtitleRendition, 0, len(keep))
		var keptLabels []string
		for _, idx := range keep {
			filtered = append(filtered, allSubs[idx])
			keptLabels = append(keptLabels, allSubs[idx].DisplayName())
		}
		subsRenditions = filtered
		d.logger.Info("subtitle tracks selected",
			domain.F("episode", epLabel),
			domain.F("available", len(allSubs)),
			domain.F("kept", len(subsRenditions)),
			domain.F("tracks", strings.Join(keptLabels, " | ")),
		)
	}

	// --subs-only downloads no video and no audio at all.
	subsOnly := subsPref.Only
	if subsOnly {
		audioRenditions = nil
	}

	d.logger.Info("selected quality",
		domain.F("episode", epLabel),
		domain.F("quality", selected.Label()),
		domain.F("audio_tracks", len(audioRenditions)),
		domain.F("subtitle_tracks", len(subsRenditions)),
		domain.F("subtitles_only", subsOnly),
		domain.F("preference", string(quality)),
	)

	// 3. Create temp directory.
	tmpDir := outPath + ".hls-tmp"
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	// Reconcile any cached segments with the renditions we are about to fetch.
	// Segments are keyed by playlist index alone, so a cache left by a run that
	// used a different video variant or audio track set cannot be reused.
	fingerprint := variantFingerprint(selected, audioRenditions, subsRenditions, subsOnly)
	wiped, err := ensureVariantMarker(tmpDir, fingerprint)
	if err != nil {
		return nil, err
	}
	if wiped {
		d.logger.Info("discarding cached segments from a different rendition set",
			domain.F("episode", epLabel),
			domain.F("quality", selected.Label()),
			domain.F("audio_tracks", len(audioRenditions)),
			domain.F("reason", "resuming with different quality/audio would splice renditions into one corrupt file"),
			domain.F("dir", tmpDir),
		)
	}

	// 4. Fetch video media playlist. Skipped entirely for --subs-only, which
	// exists precisely to avoid pulling the video down.
	var videoPlaylist *MediaPlaylist
	if !subsOnly {
		videoPlaylist, err = FetchMediaPlaylist(ctx, d.client, selected.URL, d.auth)
		if err != nil {
			return nil, fmt.Errorf("media playlist: %w", err)
		}
		if len(videoPlaylist.Segments) == 0 {
			return nil, fmt.Errorf("no segments found in media playlist")
		}
	}

	// 5. Fetch audio media playlists.
	type audioJob struct {
		rendition AudioRendition
		playlist  *MediaPlaylist
		outFile   string
	}
	var audioJobs []audioJob
	for i, a := range audioRenditions {
		ap, err := FetchMediaPlaylist(ctx, d.client, a.URI, d.auth)
		if err != nil {
			d.logger.Warn("audio playlist fetch failed, skipping track",
				domain.F("episode", epLabel),
				domain.F("audio", a.Name),
				domain.F("error", err.Error()),
			)
			continue
		}
		audioJobs = append(audioJobs, audioJob{
			rendition: a,
			playlist:  ap,
			outFile:   filepath.Join(tmpDir, fmt.Sprintf("audio_%d.ts", i)),
		})
	}

	// 5b. Fetch subtitle media playlists. A subtitle track that fails to fetch
	// is skipped with a warning rather than failing the episode — except under
	// --subs-only, where it is the only thing being downloaded and losing it
	// silently would leave an empty result.
	type subsJob struct {
		rendition SubtitleRendition
		playlist  *MediaPlaylist
		outFile   string
	}
	var subsJobs []subsJob
	for i, s := range subsRenditions {
		sp, err := FetchMediaPlaylist(ctx, d.client, s.URI, d.auth)
		if err != nil {
			if subsOnly {
				return nil, fmt.Errorf("subtitle playlist %q: %w", s.DisplayName(), err)
			}
			d.logger.Warn("subtitle playlist fetch failed, skipping track",
				domain.F("episode", epLabel),
				domain.F("subtitle", s.DisplayName()),
				domain.F("error", err.Error()),
			)
			continue
		}
		subsJobs = append(subsJobs, subsJob{
			rendition: s,
			playlist:  sp,
			outFile:   filepath.Join(tmpDir, fmt.Sprintf("subs_%d.vtt", i)),
		})
	}

	// Total segments across video + all audio + all subtitles for progress.
	totalSegments := 0
	if videoPlaylist != nil {
		totalSegments = len(videoPlaylist.Segments)
	}
	for _, aj := range audioJobs {
		totalSegments += len(aj.playlist.Segments)
	}
	for _, sj := range subsJobs {
		totalSegments += len(sj.playlist.Segments)
	}

	videoSegments := 0
	mediaDuration := 0.0
	if videoPlaylist != nil {
		videoSegments = len(videoPlaylist.Segments)
		mediaDuration = videoPlaylist.TotalDuration
	}
	d.logger.Info("segment lists fetched",
		domain.F("episode", epLabel),
		domain.F("video_segments", videoSegments),
		domain.F("audio_tracks", len(audioJobs)),
		domain.F("subtitle_tracks", len(subsJobs)),
		domain.F("total_segments", totalSegments),
		domain.F("duration", fmt.Sprintf("%.0fs", mediaDuration)),
	)

	track := domain.TrackRef{Kind: domain.TrackVideo, Index: 0}

	// Per-track progress tracking for nested display: the video track (when
	// present), then one entry per audio track, then one per subtitle track.
	// --subs-only downloads no video, so the indices are computed rather than
	// assumed — videoTrackIdx is -1 when there is no video track at all.
	trackInfos := make([]domain.TrackProgressInfo, 0, 1+len(audioJobs)+len(subsJobs))

	videoTrackIdx := -1
	if videoPlaylist != nil {
		videoTrackIdx = len(trackInfos)
		trackInfos = append(trackInfos, domain.TrackProgressInfo{
			Label:         "Video",
			TotalSegments: len(videoPlaylist.Segments),
		})
	}

	audioBase := len(trackInfos)
	for _, aj := range audioJobs {
		label := "Audio"
		switch {
		case aj.rendition.Name != "":
			label = "Audio: " + aj.rendition.Name
		case aj.rendition.Language != "":
			label = "Audio: " + aj.rendition.Language
		}
		trackInfos = append(trackInfos, domain.TrackProgressInfo{
			Label:         label,
			TotalSegments: len(aj.playlist.Segments),
		})
	}

	subsBase := len(trackInfos)
	for _, sj := range subsJobs {
		trackInfos = append(trackInfos, domain.TrackProgressInfo{
			Label:         "Subtitles: " + sj.rendition.DisplayName(),
			TotalSegments: len(sj.playlist.Segments),
		})
	}

	// progMu guards trackInfos and serializes progress reports, since segments
	// are downloaded concurrently across tracks.
	var progMu sync.Mutex

	// updateTrack records progress for a single track (by index) and emits a
	// progress report covering the aggregate percent, total estimated size, and
	// the full per-track breakdown. Safe for concurrent use.
	updateTrack := func(trackIdx int, segBytes int64) {
		progMu.Lock()
		defer progMu.Unlock()

		ti := &trackInfos[trackIdx]
		ti.DoneSegments++
		ti.DownloadedBytes += segBytes
		if ti.DoneSegments > 0 && ti.TotalSegments > 0 {
			ti.ApproxTotalBytes = ti.DownloadedBytes / int64(ti.DoneSegments) * int64(ti.TotalSegments)
		}

		if sink == nil {
			return
		}

		// Aggregate across all tracks.
		var (
			doneSegments int
			totalBytes   int64
			approxTotal  int64
		)
		for i := range trackInfos {
			doneSegments += trackInfos[i].DoneSegments
			totalBytes += trackInfos[i].DownloadedBytes
			approxTotal += trackInfos[i].ApproxTotalBytes
		}

		pct := 0
		if totalSegments > 0 {
			pct = doneSegments * 100 / totalSegments
		}
		sink.TrackProgress(key, track, pct)

		if hlsSink, ok := sink.(domain.HLSProgressSink); ok {
			// Send a copy so the consumer can retain it safely.
			snapshot := make([]domain.TrackProgressInfo, len(trackInfos))
			copy(snapshot, trackInfos)
			hlsSink.HLSProgress(key, snapshot)
		}
		if segSink, ok := sink.(domain.SegmentProgressSink); ok {
			segSink.SegmentProgress(key, doneSegments, totalSegments, totalBytes, approxTotal)
		} else if byteSink, ok := sink.(domain.ByteProgressSink); ok {
			byteSink.ByteProgress(key, totalBytes, approxTotal)
		}
	}

	// Shared semaphore bounding the number of segments fetched in parallel
	// across ALL tracks. This lets audio download alongside video instead of
	// waiting for the video track to finish.
	concurrency := d.concurrency
	if concurrency < 1 {
		concurrency = defaultConcurrency
	}
	// Guarantee every track (video + each audio) can have at least one segment
	// in flight simultaneously, so audio always downloads together with video.
	if nTracks := 1 + len(audioJobs) + len(subsJobs); concurrency < nTracks {
		concurrency = nTracks
	}
	sem := make(chan struct{}, concurrency)

	// downloadTrack fetches every segment of a single track into segDir (with
	// resume + bounded concurrency), then hands the segment list to join, which
	// assembles them into outPath.
	//
	// join varies by track kind: media segments are concatenated byte-for-byte,
	// while WebVTT subtitle segments must be parsed and re-serialized, because
	// each one carries its own "WEBVTT" header.
	downloadTrack := func(ctx context.Context, trackIdx int, segments []Segment, segDir, outPath string,
		join func([]Segment, string, string) error) error {
		if err := os.MkdirAll(segDir, 0755); err != nil {
			return fmt.Errorf("create segment dir: %w", err)
		}

		gctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var (
			wg       sync.WaitGroup
			errMu    sync.Mutex
			firstErr error
		)
		setErr := func(err error) {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
				cancel()
			}
			errMu.Unlock()
		}

		for _, seg := range segments {
			if gctx.Err() != nil {
				break
			}
			segPath := filepath.Join(segDir, fmt.Sprintf("seg_%05d.ts", seg.Index))

			// Resume: skip already-downloaded segments. Only a segment that was
			// renamed into place counts, so a run killed mid-copy cannot leave
			// a truncated file here — see hasCompleteSegment / fetchSegment.
			if size, ok := hasCompleteSegment(segPath); ok {
				updateTrack(trackIdx, size)
				continue
			}

			// Acquire a concurrency slot (or stop on cancellation).
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				continue
			}
			if gctx.Err() != nil {
				<-sem // release the slot we just took
				break
			}

			wg.Add(1)
			go func(seg Segment, segPath string) {
				defer wg.Done()
				defer func() { <-sem }()

				n, err := d.downloadSegment(gctx, seg, segPath)
				if err != nil {
					// fetchSegment removes its own temp file on every failure
					// path; this is a belt-and-braces sweep so a leftover
					// partial can never linger next to a segment we failed on.
					// segPath itself is never half-written (atomic rename).
					os.Remove(segmentPartPath(segPath))
					setErr(fmt.Errorf("segment %d failed: %w", seg.Index, err))
					return
				}
				updateTrack(trackIdx, n)
			}(seg, segPath)
		}

		wg.Wait()

		if firstErr != nil {
			return firstErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		return join(segments, segDir, outPath)
	}

	// 6. Download all tracks (video + audio + subtitles) in parallel. Segments
	// within and across tracks share the global concurrency semaphore.
	videoDir := filepath.Join(tmpDir, "video")
	videoPath := filepath.Join(tmpDir, "video.ts")
	resultAudio := make([]domain.HLSAudioTrack, len(audioJobs))
	resultSubs := make([]domain.HLSSubtitleTrack, len(subsJobs))

	// A failing track cancels its siblings so a doomed episode aborts promptly
	// instead of letting the other tracks finish downloading wastefully. The
	// real first error still wins via errOnce; siblings just see context cancel.
	tracksCtx, cancelTracks := context.WithCancel(ctx)
	defer cancelTracks()

	var (
		trackWG  sync.WaitGroup
		trackErr error
		errOnce  sync.Once
	)
	recordErr := func(err error) {
		if err != nil {
			errOnce.Do(func() {
				trackErr = err
				cancelTracks()
			})
		}
	}

	// Video track. Absent under --subs-only, which never fetched its playlist.
	if videoPlaylist != nil {
		trackWG.Add(1)
		go func() {
			defer trackWG.Done()
			if err := downloadTrack(tracksCtx, videoTrackIdx, videoPlaylist.Segments,
				videoDir, videoPath, d.concatenateSegmentsDir); err != nil {
				recordErr(fmt.Errorf("video track: %w", err))
			}
		}()
	}

	// Audio tracks.
	for ai, aj := range audioJobs {
		trackWG.Add(1)
		go func(ai int, aj audioJob) {
			defer trackWG.Done()
			audioDir := filepath.Join(tmpDir, fmt.Sprintf("audio_%d", ai))
			if err := downloadTrack(tracksCtx, audioBase+ai, aj.playlist.Segments,
				audioDir, aj.outFile, d.concatenateSegmentsDir); err != nil {
				recordErr(fmt.Errorf("audio track %d: %w", ai, err))
				return
			}
			resultAudio[ai] = domain.HLSAudioTrack{
				Path:     aj.outFile,
				Name:     aj.rendition.Name,
				Language: aj.rendition.Language,
			}
		}(ai, aj)
	}

	// Subtitle tracks. These join via mergeVTTSegmentsDir rather than raw
	// concatenation — see downloadTrack's join parameter.
	for si, sj := range subsJobs {
		trackWG.Add(1)
		go func(si int, sj subsJob) {
			defer trackWG.Done()
			subsDir := filepath.Join(tmpDir, fmt.Sprintf("subs_%d", si))
			if err := downloadTrack(tracksCtx, subsBase+si, sj.playlist.Segments,
				subsDir, sj.outFile, mergeVTTSegmentsDir); err != nil {
				recordErr(fmt.Errorf("subtitle track %d (%s): %w", si, sj.rendition.DisplayName(), err))
				return
			}
			resultSubs[si] = domain.HLSSubtitleTrack{
				Path:     sj.outFile,
				Name:     sj.rendition.DisplayName(),
				Language: sj.rendition.Language,
			}
		}(si, sj)
	}

	trackWG.Wait()

	if trackErr != nil {
		return nil, trackErr
	}

	var totalBytes int64
	for i := range trackInfos {
		totalBytes += trackInfos[i].DownloadedBytes
	}

	d.logger.Info("HLS download complete",
		domain.F("episode", epLabel),
		domain.F("quality", selected.Label()),
		domain.F("audio_tracks", len(resultAudio)),
		domain.F("concurrency", concurrency),
		domain.F("size", formatHLSBytes(totalBytes)),
	)

	codec := "h264"
	if selected.IsH265() {
		codec = "h265"
	}

	// Under --subs-only there is no video file; leaving a path here would make
	// the caller try to mux one that was never downloaded.
	if subsOnly {
		videoPath = ""
	}

	return &domain.HLSDownloadResult{
		Resolution:     selected.Resolution,
		BitrateKbps:    selected.BitrateKbps(),
		Codec:          codec,
		TotalBytes:     totalBytes,
		VideoPath:      videoPath,
		AudioTracks:    resultAudio,
		SubtitleTracks: resultSubs,
		TempDir:        tmpDir,
	}, nil
}

// mergeVTTSegmentsDir assembles downloaded WebVTT segments into a single .vtt
// file at outPath.
//
// Media segments are joined by concatenateSegmentsDir, which copies bytes
// verbatim. That is wrong for WebVTT: every segment repeats the "WEBVTT" header
// and its own X-TIMESTAMP-MAP, so the result would have headers in the middle
// and players would reject it. Segments are parsed into cues and re-serialized
// instead, which also drops the duplicate cues that straddle segment
// boundaries.
func mergeVTTSegmentsDir(segments []Segment, segDir, outPath string) (err error) {
	files := make([]*os.File, 0, len(segments))
	readers := make([]io.Reader, 0, len(segments))
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()

	for _, seg := range segments {
		segPath := filepath.Join(segDir, fmt.Sprintf("seg_%05d.ts", seg.Index))
		f, oerr := os.Open(segPath)
		if oerr != nil {
			return fmt.Errorf("open subtitle segment %d: %w", seg.Index, oerr)
		}
		files = append(files, f)
		readers = append(readers, f)
	}

	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create subtitle file: %w", err)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close subtitle file: %w", cerr)
		}
	}()

	if err := webvtt.Merge(out, readers); err != nil {
		return fmt.Errorf("merge subtitle segments: %w", err)
	}
	return nil
}

// downloadSegment downloads a single segment with retries.
func (d *Downloader) downloadSegment(ctx context.Context, seg Segment, outPath string) (int64, error) {
	var lastErr error

	for attempt := 0; attempt < maxSegmentRetries; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		if attempt > 0 {
			delay := segmentRetryDelay * time.Duration(1<<(attempt-1))
			if delay > 15*time.Second {
				delay = 15 * time.Second
			}
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(delay):
			}
		}

		n, err := d.fetchSegment(ctx, seg.URL, outPath)
		if err == nil {
			return n, nil
		}

		lastErr = err
		d.logger.Debug("segment retry",
			domain.F("segment", seg.Index),
			domain.F("attempt", attempt+1),
			domain.F("error", err.Error()),
		)
	}

	return 0, fmt.Errorf("after %d attempts: %w", maxSegmentRetries, lastErr)
}

// segmentPartPath returns the scratch path a segment is streamed to before it
// is renamed onto its final path. It lives in the same directory as the segment
// so the rename stays within one filesystem (and is therefore atomic).
func segmentPartPath(segPath string) string {
	return segPath + ".part"
}

// hasCompleteSegment reports whether segPath holds a finished segment from an
// earlier run, returning its size for progress accounting.
//
// Completeness is implied by existence: fetchSegment only ever renames a fully
// written, flushed file onto segPath, so an interrupted download leaves a
// ".part" file that this check does not see. Before segment writes were atomic
// a process killed mid-copy left a truncated .ts here, which resume accepted,
// concatenated and muxed — a silently corrupt episode reported as success.
func hasCompleteSegment(segPath string) (int64, bool) {
	info, err := os.Stat(segPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return 0, false
	}
	return info.Size(), true
}

// variantFingerprint builds a stable identity for the rendition set an episode
// is about to be downloaded from: the selected video variant plus every audio
// rendition that will be fetched, in order.
//
// Cached segments are keyed by playlist index only (seg_00042.ts), so segments
// from two different renditions are indistinguishable on disk. Resuming a 720p
// run with --quality 1080p — or resuming after the CDN re-advertised BANDWIDTH
// so SelectVariant picks differently — would otherwise splice both renditions
// into one file that switches resolution/bitrate mid-stream. Audio URIs are
// included because the same mixing risk applies when the selected track set
// changes: audio_0 would hold segments from two different dubs.
func variantFingerprint(selected Variant, audio []AudioRendition, subs []SubtitleRendition, subsOnly bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "video=%s\n", selected.URL)
	fmt.Fprintf(&b, "resolution=%s\n", selected.Resolution)
	fmt.Fprintf(&b, "bandwidth=%d\n", selected.Bandwidth)
	fmt.Fprintf(&b, "codecs=%s\n", selected.Codecs)
	for _, a := range audio {
		fmt.Fprintf(&b, "audio=%s\n", a.URI)
	}
	// Subtitle renditions and the subtitles-only mode take part too: a cache
	// left by a run with a different subtitle set (or one that skipped video
	// entirely) must not be mistaken for a resumable download of this one.
	for _, s := range subs {
		fmt.Fprintf(&b, "subs=%s\n", s.URI)
	}
	fmt.Fprintf(&b, "subs_only=%t\n", subsOnly)
	return b.String()
}

// ensureVariantMarker reconciles the segment cache in tmpDir with the rendition
// set identified by fingerprint, and reports whether a stale cache was wiped.
//
// On first use it simply records the fingerprint. On resume it compares: an
// exact match keeps the cached segments, anything else wipes tmpDir (segment
// directories plus any already-concatenated track files) and starts over, since
// mixing renditions produces a corrupt file that still reports as a success.
// A missing or unreadable marker also counts as a mismatch — without proof that
// the cache belongs to this rendition set, re-downloading is the only safe move.
func ensureVariantMarker(tmpDir, fingerprint string) (bool, error) {
	markerPath := filepath.Join(tmpDir, variantMarkerName)

	if stored, err := os.ReadFile(markerPath); err == nil && string(stored) == fingerprint {
		return false, nil
	}

	// Report a wipe only if there was actually something cached; on a fresh
	// directory this is just the marker being written for the first time.
	hadCache, err := dirHasEntries(tmpDir)
	if err != nil {
		return false, fmt.Errorf("inspect segment cache: %w", err)
	}

	if err := os.RemoveAll(tmpDir); err != nil {
		return false, fmt.Errorf("wipe stale segment cache: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return false, fmt.Errorf("recreate temp dir: %w", err)
	}
	// Written atomically so a crash here cannot leave a half-written marker
	// that would look like a mismatch (harmless) or, worse, silently truncate
	// to a prefix of some other fingerprint.
	if err := fsutil.AtomicWrite(markerPath, []byte(fingerprint), 0644); err != nil {
		return false, fmt.Errorf("write variant marker: %w", err)
	}

	return hadCache, nil
}

// dirHasEntries reports whether dir exists and contains at least one entry.
// A missing directory is not an error: it simply holds nothing.
func dirHasEntries(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return len(entries) > 0, nil
}

// fetchSegment downloads a single segment to disk.
//
// The body is streamed into a temp file next to outPath, flushed, and only then
// renamed into place. Rename within a directory is atomic, so a process kill or
// a machine crash mid-download can never leave a truncated file at outPath —
// which the resume check would otherwise accept as a finished segment and
// concatenate into a silently corrupt episode.
func (d *Downloader) fetchSegment(ctx context.Context, segURL, outPath string) (int64, error) {
	// Per-segment timeout: 120 seconds for slow CDN connections.
	segCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(segCtx, http.MethodGet, segURL, nil)
	if err != nil {
		return 0, err
	}
	applyHLSAuth(req, d.auth)

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	partPath := segmentPartPath(outPath)
	f, err := os.Create(partPath)
	if err != nil {
		return 0, err
	}

	n, copyErr := io.Copy(f, resp.Body)
	// Flush to stable storage before the rename: without it the rename can
	// reach disk ahead of the data, so a power loss would publish a segment
	// whose tail is missing — exactly the corruption the rename prevents.
	syncErr := f.Sync()
	closeErr := f.Close()

	for _, e := range []error{copyErr, syncErr, closeErr} {
		if e != nil {
			os.Remove(partPath)
			return 0, e
		}
	}

	if err := fsutil.AtomicRename(partPath, outPath); err != nil {
		os.Remove(partPath)
		return 0, err
	}

	return n, nil
}

// concatenateSegmentsDir joins all segment .ts files from segDir into outPath.
// HLS .ts segments are MPEG-TS and can be concatenated byte-by-byte.
func (d *Downloader) concatenateSegmentsDir(segments []Segment, segDir, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer out.Close()

	for _, seg := range segments {
		segPath := filepath.Join(segDir, fmt.Sprintf("seg_%05d.ts", seg.Index))
		f, err := os.Open(segPath)
		if err != nil {
			return fmt.Errorf("open segment %d: %w", seg.Index, err)
		}
		_, err = io.Copy(out, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("copy segment %d: %w", seg.Index, err)
		}
	}

	return nil
}

// formatHLSBytes formats bytes for logging.
func formatHLSBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	default:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	}
}

// Verify that *Downloader satisfies domain.HLSDownloader at compile time.
var _ domain.HLSDownloader = (*Downloader)(nil)
