// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/fsutil"
)

// engine orchestrates the download workflow using injected dependencies.
type engine struct {
	deps Dependencies

	// retryBackoff computes the wait before an episode's next attempt. When
	// nil, episodeRetryBackoff is used. It exists as a field so tests can
	// shrink the backoff; production code leaves it nil.
	retryBackoff func(attempts int) time.Duration
}

// backoffFor returns the retry backoff for the given attempt count, using the
// engine's override when present.
func (e *engine) backoffFor(attempts int) time.Duration {
	if e.retryBackoff != nil {
		return e.retryBackoff(attempts)
	}
	return episodeRetryBackoff(attempts)
}

// consecutiveFailLimit is the number of consecutive media resolution failures
// before the engine stops trying further episodes. This prevents hammering the
// CDN when links have expired or the server is blocking.
const consecutiveFailLimit = 3

// run executes the full download pipeline with parallel downloads:
// parse feed → filter → resolve + download N episodes concurrently.
// Each worker does lazy resolution immediately before downloading so CDN links
// stay fresh. MaxConcurrency controls the parallelism (default 2).
func (e *engine) run(ctx context.Context, cfg domain.RunConfig) (domain.RunResult, error) {
	log := e.deps.Logger.Component("engine")

	// Try the HLS pipeline first when the input is a page link we can scrape.
	// An explicit --feed-file is honored instead: the user asked for that feed
	// to be the source of truth, and scraping the live page would ignore it.
	if e.deps.HLSDownloader != nil && e.deps.PageScraper != nil && cfg.InputURL != "" &&
		!cfg.NoChunked && cfg.FeedFile == "" {
		result, err := e.runHLS(ctx, cfg)
		if err == nil {
			return result, nil
		}
		// A cancelled run is not a pipeline failure. Falling back here would
		// start the RSS pipeline with an already-dead context, discarding the
		// partial result and reporting confusing errors.
		if ctx.Err() != nil {
			return result, err
		}
		// AppMode has no RSS equivalent: the JSON API is the authoritative
		// source and the RSS feed path needs a cookie the run does not carry.
		// Surface the real error (e.g. an expired token) instead of masking it
		// behind a confusing feed failure.
		if cfg.AppMode {
			return result, err
		}
		log.Warn("HLS pipeline failed, falling back to RSS pipeline",
			domain.F("error", err.Error()),
		)
	}

	return e.runRSS(ctx, cfg)
}

// runRSS is the original RSS-based download pipeline.
func (e *engine) runRSS(ctx context.Context, cfg domain.RunConfig) (domain.RunResult, error) {
	log := e.deps.Logger.Component("engine")

	// Audio-track selection is implemented only for the HLS pipeline, which
	// downloads each rendition separately. This path muxes whatever the source
	// file already contains, so say so rather than silently keeping every track.
	if !cfg.AudioPref.IsAll() || cfg.AudioMenu {
		log.Warn("audio track selection applies to page-link (HLS) downloads only; " +
			"this run keeps all audio tracks")
	}

	// The RSS pipeline downloads a finished file and has no separate subtitle
	// renditions to pick from or save. --subs-only cannot be honoured here at
	// all, so it fails rather than silently downloading full episodes — the
	// opposite of what was asked for.
	if cfg.SubtitlesOnly {
		return domain.RunResult{}, fmt.Errorf(
			"--subs-only requires the HLS pipeline: pass a page link (…/item/view/…) with valid credentials")
	}
	if !cfg.SubsPref.IsAll() || cfg.SubsMenu || cfg.SubsExternal {
		log.Warn("subtitle selection applies to page-link (HLS) downloads only; " +
			"this run keeps whatever subtitles the source file already contains")
	}

	// 1. Resolve input → FeedSource.
	var feedSrc domain.FeedSource
	if cfg.FeedFile != "" {
		log.Info("using local feed file", domain.F("path", cfg.FeedFile))
		feedSrc = domain.FeedSource{LocalPath: cfg.FeedFile}
		if cfg.InputURL != "" {
			if resolved, rerr := e.deps.InputResolver.Resolve(ctx, cfg.InputURL); rerr == nil {
				feedSrc.ID = resolved.ID
				feedSrc.Token = resolved.Token
			} else if id := domain.SeriesIDFromURL(cfg.InputURL); id != "" {
				// Resolving needs credentials (it fetches the page to mint a feed
				// token); without them we can still recover the series id from the
				// URL itself. It is what keys the state file, so losing it would
				// make an existing series look unstarted and re-download it.
				feedSrc.ID = string(id)
				log.Debug("feed token unavailable; using series id from the input URL",
					domain.F("series_id", string(id)),
					domain.F("error", rerr.Error()),
				)
			} else {
				log.Warn("could not determine the series id from the input URL; "+
					"resume tracking is disabled for this run",
					domain.F("error", rerr.Error()),
				)
			}
		}
	} else {
		log.Info("resolving input URL", domain.F("url", cfg.InputURL))
		resolved, err := e.deps.InputResolver.Resolve(ctx, cfg.InputURL)
		if err != nil {
			return domain.RunResult{}, err
		}
		feedSrc = resolved
	}

	// 2. Parse feed → Series
	log.Info("parsing feed", domain.F("feed_id", feedSrc.ID))
	series, err := e.deps.FeedParser.Parse(ctx, feedSrc)
	if err != nil {
		return domain.RunResult{}, err
	}

	// 2b. Now that we know the series title, point the state store at the
	// series download directory so the state file lives alongside the media.
	seriesDir := e.seriesDirPath(cfg.OutputPath, series)
	if ss, ok := e.deps.StateStore.(interface{ SetSeriesDir(string) }); ok {
		ss.SetSeriesDir(seriesDir)
	}

	// 3. Load state for the series
	state, err := e.deps.StateStore.Load(ctx, series.ID)
	if err != nil {
		return domain.RunResult{}, err
	}

	// 4. Filter episodes by SeasonSel/EpisodeSel, skip completed
	allMatching := e.matchingEpisodes(series, cfg)
	selected := e.filterCompleted(allMatching, state, cfg)
	if len(selected) == 0 {
		log.Info("no episodes to download")
		return domain.RunResult{Total: 0}, nil
	}

	alreadyCompleted := len(allMatching) - len(selected)
	log.Info("episodes selected for download",
		domain.F("count", len(selected)),
		domain.F("already_completed", alreadyCompleted),
		domain.F("concurrency", cfg.MaxConcurrency),
	)

	// 5. DryRun: just list what would be downloaded.
	if cfg.DryRun {
		log.Info("dry run — listing episodes without downloading")
		for _, ep := range selected {
			log.Info(fmt.Sprintf("  %s %s", ep.Key.Label(), ep.Title))
		}
		return domain.RunResult{Total: len(selected)}, nil
	}

	// 5b. Persist series-level metadata for provenance/recovery.
	feedURL := cfg.InputURL
	if cfg.FeedFile != "" {
		feedURL = cfg.FeedFile
	}
	seriesMeta := domain.SeriesMetadata{
		Title:         series.Title,
		OriginalTitle: series.OriginalTitle,
		Description:   series.Description,
		PosterURL:     series.PosterURL,
		FeedURL:       feedURL,
		InputURL:      cfg.InputURL,
		UpdatedAt:     time.Now(),
	}
	if err := e.deps.StateStore.SetMetadata(ctx, series.ID, seriesMeta); err != nil {
		log.Warn("failed to persist series metadata", domain.F("error", err.Error()))
	}

	// 6. Download series poster for embedding as cover art.
	var posterPath string
	if series.PosterURL != "" {
		p, err := e.downloadPoster(ctx, series.PosterURL, seriesDir)
		if err != nil {
			log.Debug("poster download failed, skipping cover art embedding",
				domain.F("error", err.Error()))
		} else {
			posterPath = p
			defer os.Remove(posterPath)
		}
	}

	// 7. Start progress reporting.
	plan := domain.SeriesPlan{
		Title:              series.Title,
		Total:              len(allMatching),
		Seasons:            countSeasons(allMatching),
		AlreadyCompleted:   alreadyCompleted,
		CompletedPerSeason: countCompletedPerSeason(allMatching, state, e.deps.StateStore, cfg.ForceRedownload),
	}
	e.deps.ProgressReporter.Start(plan)
	defer e.deps.ProgressReporter.Stop()

	// 8. Parallel download with worker pool.
	// Each worker picks an episode from the channel, resolves media, downloads.
	concurrency := cfg.MaxConcurrency
	if concurrency < 1 {
		concurrency = 2
	}
	if concurrency > len(selected) {
		concurrency = len(selected)
	}

	// Channel to dispatch episodes to workers.
	jobCh := make(chan domain.Episode, concurrency)

	// Collect outcomes thread-safely.
	var mu sync.Mutex
	var (
		succeeded        int
		failed           int
		skipped          int
		consecutiveFails int
		stopDispatch     bool
		outcomes         []domain.JobOutcome
	)

	// Worker function.
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ep := range jobCh {
				// Check context.
				if ctx.Err() != nil {
					mu.Lock()
					outcomes = append(outcomes, domain.JobOutcome{
						Key: ep.Key, Succeeded: false, Err: ctx.Err(), Attempts: 1,
					})
					failed++
					mu.Unlock()
					continue
				}

				log.Info("resolving media",
					domain.F("episode", ep.Key.Label()),
				)

				// Resolve media (ffprobe / HLS fetch).
				media, err := e.deps.MediaResolver.Resolve(ctx, ep, cfg.Quality)
				if err != nil {
					mu.Lock()
					consecutiveFails++
					cf := consecutiveFails
					log.Warn("media resolution failed",
						domain.F("episode", ep.Key.Label()),
						domain.F("error", err.Error()),
						domain.F("consecutive_fails", cf),
					)
					if cf >= consecutiveFailLimit {
						stopDispatch = true
						log.Warn("stopping: too many consecutive failures — CDN links may have "+
							"expired. Try re-fetching the feed for fresh links.",
							domain.F("succeeded_so_far", succeeded),
						)
					}
					outcomes = append(outcomes, domain.JobOutcome{
						Key: ep.Key, Succeeded: false, Err: err, Attempts: 1,
					})
					failed++
					mu.Unlock()
					e.deps.ProgressReporter.EpisodeFailed(ep.Key, err)
					continue
				}

				// Success resets the consecutive fail counter.
				mu.Lock()
				consecutiveFails = 0
				mu.Unlock()

				// Log resolved quality info.
				qualityInfo := media.Video.Resolution
				if media.Video.BitRate > 0 {
					qualityInfo = fmt.Sprintf("%s @ %d kbps", media.Video.Resolution, media.Video.BitRate)
				}
				log.Info("media resolved",
					domain.F("episode", ep.Key.Label()),
					domain.F("quality", qualityInfo),
					domain.F("audio_tracks", len(media.Audio)),
				)

				// Build output path.
				outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, ep)
				if err != nil {
					log.Warn("output path failed, skipping",
						domain.F("episode", ep.Key.Label()),
						domain.F("error", err.Error()),
					)
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}
				if err := e.deps.OutputLayout.EnsureDirs(outPath); err != nil {
					log.Warn("cannot create directory, skipping",
						domain.F("path", outPath), domain.F("error", err.Error()),
					)
					mu.Lock()
					skipped++
					mu.Unlock()
					continue
				}

				job := domain.Job{
					Episode:     ep,
					Media:       media,
					OutPath:     outPath,
					PosterPath:  posterPath,
					SeriesTitle: series.Title,
				}

				// Download with retry: CDN may truncate streams under parallel
				// load. We retry up to 2 additional times with increasing pauses.
				const maxDownloadAttempts = 3
				var dlErr error
				for attempt := 1; attempt <= maxDownloadAttempts; attempt++ {
					if ctx.Err() != nil {
						dlErr = ctx.Err()
						break
					}

					e.deps.ProgressReporter.EpisodeStarted(ep.Key)
					dlErr = e.deps.Downloader.Download(ctx, job, e.deps.ProgressReporter)
					if dlErr == nil {
						break
					}

					log.Warn("download attempt failed",
						domain.F("episode", ep.Key.Label()),
						domain.F("attempt", attempt),
						domain.F("max_attempts", maxDownloadAttempts),
						domain.F("error", dlErr.Error()),
					)
					e.deps.ProgressReporter.EpisodeFailed(ep.Key, dlErr)

					if attempt < maxDownloadAttempts {
						// Increasing pause before retry (10s, 20s) to let CDN recover.
						delay := time.Duration(attempt) * 10 * time.Second
						select {
						case <-ctx.Done():
							dlErr = ctx.Err()
						case <-time.After(delay):
						}
					}
				}

				if dlErr != nil {
					mu.Lock()
					outcomes = append(outcomes, domain.JobOutcome{
						Key: ep.Key, Succeeded: false, Err: dlErr, Attempts: maxDownloadAttempts,
					})
					failed++
					mu.Unlock()
					continue
				}

				// Mark completed with full metadata.
				info, statErr := os.Stat(job.OutPath)
				var fileSize int64
				if statErr == nil {
					fileSize = info.Size()
				}
				completedInfo := domain.CompletedInfo{
					Key:        ep.Key,
					Path:       job.OutPath,
					Bytes:      fileSize,
					Title:      ep.Title,
					Quality:    ep.Quality,
					Resolution: job.Media.Video.Resolution,
					BitRate:    job.Media.Video.BitRate,
					PageLink:   ep.PageLink,
					MediaURL:   job.Media.Source.URL,
				}
				if err := e.deps.StateStore.MarkCompleted(ctx, completedInfo); err != nil {
					log.Warn("failed to persist state",
						domain.F("episode", ep.Key.Label()),
						domain.F("error", err.Error()),
					)
				}
				mu.Lock()
				outcomes = append(outcomes, domain.JobOutcome{
					Key: ep.Key, Succeeded: true, Attempts: 1,
				})
				succeeded++
				mu.Unlock()
				e.deps.ProgressReporter.EpisodeCompleted(ep.Key)
			}
		}()
	}

	// Dispatch episodes to workers.
	for _, ep := range selected {
		// Check context (SIGINT).
		if ctx.Err() != nil {
			log.Info("interrupted, stopping dispatch")
			break
		}

		// Check if we should stop due to consecutive failures.
		mu.Lock()
		shouldStop := stopDispatch
		mu.Unlock()
		if shouldStop {
			mu.Lock()
			skipped++
			mu.Unlock()
			continue
		}

		select {
		case jobCh <- ep:
		case <-ctx.Done():
			log.Info("interrupted, stopping dispatch")
			mu.Lock()
			skipped++
			mu.Unlock()
		}
	}
	close(jobCh)

	// Wait for all workers to finish.
	wg.Wait()

	return domain.RunResult{
		Total:     len(selected),
		Succeeded: succeeded,
		Failed:    failed,
		Skipped:   skipped,
		Outcomes:  outcomes,
	}, nil
}

// ---------------------------------------------------------------------------
// HLS Pipeline
// ---------------------------------------------------------------------------

// runHLS implements the HLS segment-based download pipeline.
// It scrapes the page for PLAYER_PLAYLIST, selects quality, downloads via HLS segments.
func (e *engine) runHLS(ctx context.Context, cfg domain.RunConfig) (domain.RunResult, error) {
	log := e.deps.Logger.Component("engine-hls")

	// 1. Extract playlist from page.
	log.Info("extracting HLS playlist from page", domain.F("url", cfg.InputURL))
	playlist, err := e.deps.PageScraper.ExtractAllSeasons(ctx, cfg.InputURL)
	if err != nil {
		return domain.RunResult{}, fmt.Errorf("page scrape: %w", err)
	}

	if len(playlist.Episodes) == 0 {
		return domain.RunResult{}, fmt.Errorf("no episodes found in page playlist")
	}

	log.Info("HLS playlist extracted",
		domain.F("title", playlist.Title),
		domain.F("episodes", len(playlist.Episodes)),
		domain.F("seasons", len(playlist.Seasons)),
	)

	// 2. Build a Series from the page playlist (for state store and output layout).
	series := e.buildSeriesFromPlaylist(playlist, cfg)

	// Point state store at series directory.
	seriesDir := e.seriesDirPath(cfg.OutputPath, series)
	if ss, ok := e.deps.StateStore.(interface{ SetSeriesDir(string) }); ok {
		ss.SetSeriesDir(seriesDir)
	}

	// 3. Load state.
	state, err := e.deps.StateStore.Load(ctx, series.ID)
	if err != nil {
		return domain.RunResult{}, err
	}

	// 4. Filter episodes.
	allMatching := e.matchingEpisodes(series, cfg)
	selected := e.filterCompleted(allMatching, state, cfg)
	if len(selected) == 0 {
		log.Info("no episodes to download (all completed)")
		return domain.RunResult{Total: 0}, nil
	}

	alreadyCompleted := len(allMatching) - len(selected)
	log.Info("HLS download starting",
		domain.F("to_download", len(selected)),
		domain.F("already_completed", alreadyCompleted),
	)

	if cfg.DryRun {
		log.Info("dry run — listing episodes")
		for _, ep := range selected {
			log.Info(fmt.Sprintf("  %s %s", ep.Key.Label(), ep.Title))
		}
		return domain.RunResult{Total: len(selected)}, nil
	}

	// 5. Persist series metadata.
	seriesMeta := domain.SeriesMetadata{
		Title:     series.Title,
		PosterURL: playlist.Poster,
		InputURL:  cfg.InputURL,
		UpdatedAt: time.Now(),
	}
	_ = e.deps.StateStore.SetMetadata(ctx, series.ID, seriesMeta)

	// 5b. Download series poster for embedding as cover art.
	var posterPath string
	if playlist.Poster != "" {
		if p, perr := e.downloadPoster(ctx, playlist.Poster, seriesDir); perr == nil {
			posterPath = p
			defer os.Remove(posterPath)
		} else {
			log.Debug("poster download failed, skipping cover art",
				domain.F("error", perr.Error()))
		}
	}

	// 6. Build manifest URL map (episode key → manifest URL).
	manifestMap := make(map[domain.EpisodeKey]string)
	for _, pe := range playlist.Episodes {
		key := domain.EpisodeKey{
			Series:  series.ID,
			Season:  pe.Season,
			Episode: pe.Episode,
		}
		manifestMap[key] = pe.ManifestURL
	}

	// 7. Resolve audio-track preference before starting the live progress
	// display. An explicit --audio preference is applied directly; otherwise,
	// when the interactive menu is enabled, probe the first episode's tracks
	// and let the user choose. The resulting preference is pushed to the HLS
	// downloader for all episodes. This runs before progress rendering so the
	// interactive prompt isn't clobbered by progress redraws.
	// Video quality is resolved first: the audio and subtitle probes below take
	// a quality and report only the renditions belonging to that variant, so
	// choosing afterwards could list tracks that the chosen variant does not
	// carry.
	cfg.Quality = e.resolveVideoQuality(ctx, cfg, selected, manifestMap)

	pref := e.resolveAudioPreference(ctx, cfg, selected, manifestMap)
	e.deps.HLSDownloader.SetAudioPreference(pref)

	subsPref := e.resolveSubtitlePreference(ctx, cfg, selected, manifestMap)
	e.deps.HLSDownloader.SetSubtitlePreference(subsPref)

	// 8. Start progress reporting.
	plan := domain.SeriesPlan{
		Title:              series.Title,
		Total:              len(allMatching),
		Seasons:            countSeasons(allMatching),
		AlreadyCompleted:   alreadyCompleted,
		CompletedPerSeason: countCompletedPerSeason(allMatching, state, e.deps.StateStore, cfg.ForceRedownload),
	}
	e.deps.ProgressReporter.Start(plan)
	defer e.deps.ProgressReporter.Stop()

	// 9. Download episodes with deferred-retry scheduling. New episodes are
	// processed in order; an episode that fails on a transient network error
	// (timeout, EOF, connection reset, 5xx) is not dropped — it is parked in a
	// retry queue and reattempted later, interleaved between new episodes and,
	// once new episodes are exhausted, cycled with backoff until it succeeds or
	// exhausts its attempt budget. Partial segments (.hls-tmp) are preserved so
	// each reattempt resumes instead of restarting.
	var succeeded, failed, skipped int
	var outcomes []domain.JobOutcome

	// Build the initial work list, preserving order and skipping episodes with
	// no manifest URL.
	newQueue := make([]*pendingEpisode, 0, len(selected))
	for _, ep := range selected {
		manifestURL, ok := manifestMap[ep.Key]
		if !ok {
			log.Warn("no manifest URL for episode, skipping",
				domain.F("episode", ep.Key.Label()),
			)
			skipped++
			continue
		}
		newQueue = append(newQueue, &pendingEpisode{ep: ep, manifest: manifestURL})
	}

	var retryQueue []*pendingEpisode

	// processOne runs a single attempt for a pending episode and routes the
	// outcome: success → completed; transient failure → re-park (or give up
	// after the attempt budget); fatal failure → mark failed.
	processOne := func(pe *pendingEpisode) {
		if ctx.Err() != nil {
			return
		}
		pe.attempts++
		epLabel := pe.ep.Key.Label()

		res, err := e.attemptHLSEpisode(ctx, cfg, series, pe.ep, pe.manifest, posterPath)
		switch res {
		case epSuccess:
			succeeded++
			outcomes = append(outcomes, domain.JobOutcome{Key: pe.ep.Key, Succeeded: true, Attempts: pe.attempts})
			e.deps.ProgressReporter.EpisodeCompleted(pe.ep.Key)

		case epRetryable:
			if pe.attempts >= maxEpisodeAttempts {
				log.Warn("giving up on episode after repeated transient failures",
					domain.F("episode", epLabel),
					domain.F("attempts", pe.attempts),
					domain.F("error", err.Error()),
				)
				failed++
				outcomes = append(outcomes, domain.JobOutcome{Key: pe.ep.Key, Err: err, Attempts: pe.attempts})
				e.deps.ProgressReporter.EpisodeFailed(pe.ep.Key, err)
				return
			}
			wait := e.backoffFor(pe.attempts)
			pe.lastErr = err
			pe.nextAt = time.Now().Add(wait)
			retryQueue = append(retryQueue, pe)
			log.Info("episode download interrupted, will retry later",
				domain.F("episode", epLabel),
				domain.F("attempt", pe.attempts),
				domain.F("retry_in", wait.Round(time.Second).String()),
				domain.F("error", err.Error()),
			)
			reportEpisodeDeferred(e.deps.ProgressReporter, pe.ep.Key, err, pe.attempts)

		case epFatal:
			if ctx.Err() != nil {
				return
			}
			log.Warn("episode failed",
				domain.F("episode", epLabel),
				domain.F("error", err.Error()),
			)
			failed++
			outcomes = append(outcomes, domain.JobOutcome{Key: pe.ep.Key, Err: err, Attempts: pe.attempts})
			e.deps.ProgressReporter.EpisodeFailed(pe.ep.Key, err)
		}
	}

	for (len(newQueue) > 0 || len(retryQueue) > 0) && ctx.Err() == nil {
		// Prefer a fresh episode when one is available.
		if len(newQueue) > 0 {
			// Snapshot how many episodes are already parked: only those (which
			// were deferred in a previous iteration) are eligible to be
			// interleaved after this new episode. This yields the desired
			// "new → previously-failed → new → …" cadence instead of retrying
			// an episode in the same step it just failed.
			eligible := len(retryQueue)

			pe := newQueue[0]
			newQueue = newQueue[1:]
			processOne(pe) // may append the just-failed episode to retryQueue

			// Interleave: give one ready, previously-deferred episode another
			// shot (e.g. finished E07 → retry E05 → E08 → …).
			if idx := readyDeferredIndex(retryQueue[:eligible], time.Now()); idx >= 0 {
				pe2 := retryQueue[idx]
				retryQueue = append(retryQueue[:idx], retryQueue[idx+1:]...)
				processOne(pe2)
			}
			continue
		}

		// Only deferred episodes remain — process the earliest-due one,
		// waiting for its backoff window if necessary.
		idx := earliestDeferredIndex(retryQueue)
		pe := retryQueue[idx]
		retryQueue = append(retryQueue[:idx], retryQueue[idx+1:]...)
		if wait := time.Until(pe.nextAt); wait > 0 {
			log.Info("waiting before next retry",
				domain.F("episode", pe.ep.Key.Label()),
				domain.F("wait", wait.Round(time.Second).String()),
			)
			select {
			case <-ctx.Done():
				return domain.RunResult{Total: len(selected), Succeeded: succeeded, Failed: failed, Skipped: skipped, Outcomes: outcomes}, ctx.Err()
			case <-time.After(wait):
			}
		}
		processOne(pe)
	}

	// Episodes still parked when interrupted count as failures for the summary.
	for _, pe := range retryQueue {
		failed++
		err := pe.lastErr
		if err == nil {
			err = ctx.Err()
		}
		outcomes = append(outcomes, domain.JobOutcome{Key: pe.ep.Key, Err: err, Attempts: pe.attempts})
	}

	return domain.RunResult{
		Total:     len(selected),
		Succeeded: succeeded,
		Failed:    failed,
		Skipped:   skipped,
		Outcomes:  outcomes,
	}, nil
}

// maxEpisodeAttempts bounds how many times the engine reattempts a single
// episode that keeps failing on transient network errors before giving up. The
// per-segment retry budget is separate and applies within each attempt.
const maxEpisodeAttempts = 8

// pendingEpisode is a unit of work in the deferred-retry scheduler.
type pendingEpisode struct {
	ep       domain.Episode
	manifest string
	attempts int       // attempts made so far
	nextAt   time.Time // earliest time the next attempt may run (backoff)
	lastErr  error     // error from the most recent attempt
}

// episodeOutcome classifies the result of a single download+mux attempt.
type episodeOutcome int

const (
	epSuccess   episodeOutcome = iota // downloaded and muxed
	epRetryable                       // transient failure — worth retrying later
	epFatal                           // permanent failure — do not retry
)

// episodeRetryBackoff returns how long to wait before the next attempt of an
// episode that has failed `attempts` times. It grows linearly and is capped so
// a stuck CDN segment doesn't stall the whole run indefinitely.
func episodeRetryBackoff(attempts int) time.Duration {
	wait := time.Duration(attempts) * 20 * time.Second
	if wait > 3*time.Minute {
		wait = 3 * time.Minute
	}
	return wait
}

// readyDeferredIndex returns the index of a deferred episode whose backoff
// window has elapsed (earliest-due first), or -1 if none are ready.
func readyDeferredIndex(q []*pendingEpisode, now time.Time) int {
	best := -1
	for i, pe := range q {
		if pe.nextAt.After(now) {
			continue
		}
		if best == -1 || pe.nextAt.Before(q[best].nextAt) {
			best = i
		}
	}
	return best
}

// earliestDeferredIndex returns the index of the deferred episode due soonest.
// The queue must be non-empty.
func earliestDeferredIndex(q []*pendingEpisode) int {
	best := 0
	for i, pe := range q {
		if pe.nextAt.Before(q[best].nextAt) {
			best = i
		}
	}
	return best
}

// reportEpisodeDeferred notifies the progress reporter that an episode is
// parked for a later retry, if the reporter supports it. Reporters without the
// optional hook simply don't show a distinct "deferred" state.
func reportEpisodeDeferred(r domain.ProgressReporter, key domain.EpisodeKey, err error, attempts int) {
	if d, ok := r.(interface {
		EpisodeDeferred(domain.EpisodeKey, error, int)
	}); ok {
		d.EpisodeDeferred(key, err, attempts)
	}
}

// attemptHLSEpisode performs one full download+mux attempt for a single
// episode. It returns epSuccess on completion, epRetryable for transient
// network failures (the .hls-tmp segments are left in place so the next
// attempt resumes), or epFatal for permanent errors. The returned error is
// non-nil for the two failure outcomes.
func (e *engine) attemptHLSEpisode(
	ctx context.Context,
	cfg domain.RunConfig,
	series domain.Series,
	ep domain.Episode,
	manifestURL string,
	posterPath string,
) (episodeOutcome, error) {
	log := e.deps.Logger.Component("engine-hls")
	epLabel := ep.Key.Label()

	outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, ep)
	if err != nil {
		return epFatal, fmt.Errorf("output path: %w", err)
	}
	if err := e.deps.OutputLayout.EnsureDirs(outPath); err != nil {
		return epFatal, fmt.Errorf("create directory: %w", err)
	}

	e.deps.ProgressReporter.EpisodeStarted(ep.Key)

	tsPath := outPath + ".ts"
	hlsResult, dlErr := e.deps.HLSDownloader.DownloadEpisode(ctx, manifestURL, cfg.Quality, tsPath, ep.Key, e.deps.ProgressReporter)
	if dlErr != nil {
		if ctx.Err() != nil {
			return epRetryable, dlErr
		}
		if isTransientDownloadError(dlErr) {
			return epRetryable, dlErr
		}
		return epFatal, dlErr
	}

	muxJob := domain.Job{
		Episode:     ep,
		OutPath:     outPath,
		PosterPath:  posterPath,
		SeriesTitle: series.Title,
	}

	// --subs-only: no video was downloaded, so there is nothing to mux. Write
	// the subtitles out and finish the episode here.
	//
	// Selection was strict, so an empty result means this episode simply does
	// not carry what was asked for. That is fatal for the episode (the run's
	// exit code reflects it) but not retryable — retrying cannot make a missing
	// language appear.
	if cfg.SubtitlesOnly {
		if len(hlsResult.SubtitleTracks) == 0 {
			if hlsResult.TempDir != "" {
				os.RemoveAll(hlsResult.TempDir)
			}
			return epFatal, fmt.Errorf("%w: %s", domain.ErrNoSubtitlesMatched, epLabel)
		}
		written, subsErr := e.writeSubtitleSidecars(ctx, muxJob, hlsResult)
		if hlsResult.TempDir != "" {
			os.RemoveAll(hlsResult.TempDir)
		}
		if subsErr != nil {
			if ctx.Err() != nil {
				return epRetryable, subsErr
			}
			return epFatal, subsErr
		}
		log.Info("subtitles saved",
			domain.F("episode", epLabel),
			domain.F("files", len(written)),
		)
		return epSuccess, nil
	}

	// Mux downloaded video + audio streams into the final container.
	log.Info("muxing",
		domain.F("episode", epLabel),
		domain.F("quality", fmt.Sprintf("%s @ %d kbps", hlsResult.Resolution, hlsResult.BitrateKbps)),
		domain.F("audio_tracks", len(hlsResult.AudioTracks)),
		domain.F("subtitle_tracks", len(hlsResult.SubtitleTracks)),
	)

	// --subs-external keeps subtitles out of the container and writes them as
	// separate files instead, so they must not also be muxed in.
	if cfg.SubsExternal && len(hlsResult.SubtitleTracks) > 0 {
		if _, subsErr := e.writeSubtitleSidecars(ctx, muxJob, hlsResult); subsErr != nil {
			log.Warn("writing external subtitles failed; continuing without them",
				domain.F("episode", epLabel),
				domain.F("error", subsErr.Error()),
			)
		}
		hlsResult.SubtitleTracks = nil
	}

	var remuxErr error
	if muxer, ok := e.deps.Downloader.(domain.HLSMuxer); ok {
		remuxErr = muxer.MuxHLS(ctx, muxJob, hlsResult)
	} else {
		remuxErr = fmt.Errorf("downloader does not support HLS muxing")
	}

	if remuxErr != nil {
		// Keep the segment cache. Muxing is the last step of an episode whose
		// segments are all on disk already, so discarding them here would make
		// the next attempt re-download gigabytes — exactly what the deferred
		// retry design preserves them for. An interrupted mux is not a defect in
		// the episode either, so it stays retryable.
		if ctx.Err() != nil {
			return epRetryable, remuxErr
		}
		log.Warn("remux failed",
			domain.F("episode", epLabel),
			domain.F("error", remuxErr.Error()),
		)
		return epFatal, remuxErr
	}

	// Muxed successfully — the segments have served their purpose.
	if hlsResult.TempDir != "" {
		os.RemoveAll(hlsResult.TempDir)
	}

	// Mark completed.
	info, _ := os.Stat(outPath)
	var fileSize int64
	if info != nil {
		fileSize = info.Size()
	}
	completedInfo := domain.CompletedInfo{
		Key:        ep.Key,
		Path:       outPath,
		Bytes:      fileSize,
		Title:      ep.Title,
		Quality:    fmt.Sprintf("%s/%s", hlsResult.Resolution, hlsResult.Codec),
		Resolution: hlsResult.Resolution,
		BitRate:    hlsResult.BitrateKbps,
		PageLink:   ep.PageLink,
		MediaURL:   manifestURL,
	}
	_ = e.deps.StateStore.MarkCompleted(ctx, completedInfo)

	return epSuccess, nil
}

// transientErrorMarkers are substrings identifying recoverable network/CDN
// failures: the connection or server hiccupped but is likely to recover, so
// the episode should be retried later rather than abandoned.
var transientErrorMarkers = []string{
	"context deadline exceeded", // per-segment timeout
	"deadline exceeded",
	"timeout",
	"timed out",
	"unexpected eof",
	"connection reset",
	"connection refused",
	"broken pipe",
	"eof",
	"no such host",      // transient DNS
	"temporary failure", // transient DNS
	"tls handshake",
	// HTTP status markers are matched in their "http NNN" form only. Bare
	// numbers ("500", "429", …) were removed because a segment index or byte
	// count in an otherwise-permanent error (e.g. "segment 500: invalid
	// manifest") would falsely flip it to retryable.
	"http 429", // rate limited
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"server misbehaving",
}

// isTransientDownloadError reports whether err looks like a recoverable
// network/CDN condition (as opposed to a permanent error such as a bad
// manifest or a 404). The check is substring-based because the underlying
// errors are wrapped and stringified across several layers.
func isTransientDownloadError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range transientErrorMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// Precedence:
//  1. An explicit cfg.AudioPref (from the --audio flag) is used as-is, with
//     Prefer hints enriched from the first episode's tracks so a missing dub
//     falls back within the desired language.
//  2. Otherwise, if the interactive menu is enabled and a chooser is wired,
//     probe the first episode's tracks and prompt the user. The chosen tracks
//     are generalized into a cross-episode preference.
//  3. Otherwise, keep all tracks (zero preference).
func (e *engine) resolveAudioPreference(
	ctx context.Context,
	cfg domain.RunConfig,
	selected []domain.Episode,
	manifestMap map[domain.EpisodeKey]string,
) domain.AudioPreference {
	log := e.deps.Logger.Component("engine")

	// Fast path: no explicit preference and no interactive menu → keep all
	// tracks without probing the network.
	menuActive := cfg.AudioMenu && e.deps.AudioChooser != nil
	if cfg.AudioPref.IsAll() && !menuActive {
		return domain.AudioPreference{}
	}

	// Probe the first episode's audio tracks (best-effort) — used for both the
	// menu and for enriching Prefer hints.
	var tracks []domain.AudioTrackInfo
	if len(selected) > 0 {
		if url, ok := manifestMap[selected[0].Key]; ok && url != "" {
			if t, err := e.deps.HLSDownloader.ListAudioTracks(ctx, url, cfg.Quality); err != nil {
				log.Debug("audio track probe failed", domain.F("error", err.Error()))
			} else {
				tracks = t
			}
		}
	}

	// 1. Explicit --audio preference.
	if !cfg.AudioPref.IsAll() {
		pref := cfg.AudioPref
		if len(pref.Prefer) == 0 && len(tracks) > 0 {
			pref.Prefer = domain.DeriveAudioPrefer(tracks, pref.Include)
		}
		log.Info("audio preference (explicit)",
			domain.F("include", strings.Join(pref.Include, ", ")),
			domain.F("exclude", strings.Join(pref.Exclude, ", ")),
		)
		return pref
	}

	// 2. Interactive menu.
	if menuActive && len(tracks) > 1 {
		chosen, err := e.deps.AudioChooser.ChooseAudio(tracks, cfg.AudioMenuTimeout)
		if err != nil {
			log.Warn("audio menu failed, keeping all tracks", domain.F("error", err.Error()))
			return domain.AudioPreference{}
		}
		if len(chosen) == 0 {
			return domain.AudioPreference{}
		}
		pref := domain.BuildAudioPreference(tracks, chosen)
		log.Info("audio preference (interactive)",
			domain.F("include", strings.Join(pref.Include, ", ")),
			domain.F("selected", len(chosen)),
		)
		return pref
	}

	// 3. Keep everything.
	return domain.AudioPreference{}
}

// resolveVideoQuality returns the quality the run should use, showing the
// interactive picker when --video-menu is set and leaving cfg.Quality untouched
// otherwise.
//
// The picker resolves to an ordinary --quality selector ("1080p-h265"), so the
// choice applies uniformly to every episode and the rest of the pipeline stays
// unaware a menu happened. Any failure keeps the configured quality: a probe
// that cannot reach the network is no reason to abandon the run.
func (e *engine) resolveVideoQuality(
	ctx context.Context,
	cfg domain.RunConfig,
	selected []domain.Episode,
	manifestMap map[domain.EpisodeKey]string,
) domain.Quality {
	log := e.deps.Logger.Component("engine")

	if !cfg.VideoMenu || e.deps.VideoChooser == nil || len(selected) == 0 {
		return cfg.Quality
	}

	url, ok := manifestMap[selected[0].Key]
	if !ok || url == "" {
		return cfg.Quality
	}

	qualities, err := e.deps.HLSDownloader.ListVideoQualities(ctx, url)
	if err != nil {
		log.Warn("video quality probe failed, keeping the configured quality",
			domain.F("error", err.Error()))
		return cfg.Quality
	}
	if len(qualities) <= 1 {
		return cfg.Quality
	}

	chosen, err := e.deps.VideoChooser.ChooseVideo(qualities, cfg.VideoMenuTimeout)
	if err != nil {
		log.Warn("video menu failed, keeping the configured quality",
			domain.F("error", err.Error()))
		return cfg.Quality
	}
	if chosen < 0 || chosen >= len(qualities) {
		return cfg.Quality
	}

	picked := qualities[chosen]
	log.Info("video quality (interactive)",
		domain.F("quality", picked.Label()),
		domain.F("selector", string(picked.Quality)),
	)
	return picked.Quality
}

// resolveSubtitlePreference mirrors resolveAudioPreference for subtitles: an
// explicit --subs wins, otherwise the interactive picker runs, otherwise every
// track is kept.
//
// The Strict and Only flags always reflect --subs-only, including on the fast
// path, because they govern how the downloader behaves rather than which tracks
// match — a subtitles-only run with no --subs still must not download video.
func (e *engine) resolveSubtitlePreference(
	ctx context.Context,
	cfg domain.RunConfig,
	selected []domain.Episode,
	manifestMap map[domain.EpisodeKey]string,
) domain.SubtitlePreference {
	log := e.deps.Logger.Component("engine")

	withMode := func(p domain.SubtitlePreference) domain.SubtitlePreference {
		p.Strict = cfg.SubtitlesOnly
		p.Only = cfg.SubtitlesOnly
		return p
	}

	menuActive := cfg.SubsMenu && e.deps.SubtitleChooser != nil
	if cfg.SubsPref.IsAll() && !menuActive {
		return withMode(domain.SubtitlePreference{})
	}

	// Probe the first episode's subtitle tracks (best-effort), used for the menu
	// and to enrich Prefer hints.
	var tracks []domain.SubtitleTrackInfo
	if len(selected) > 0 {
		if url, ok := manifestMap[selected[0].Key]; ok && url != "" {
			if t, err := e.deps.HLSDownloader.ListSubtitleTracks(ctx, url, cfg.Quality); err != nil {
				log.Debug("subtitle track probe failed", domain.F("error", err.Error()))
			} else {
				tracks = t
			}
		}
	}

	// 1. Explicit --subs preference.
	if !cfg.SubsPref.IsAll() {
		pref := cfg.SubsPref
		if len(pref.Prefer) == 0 && len(tracks) > 0 {
			pref.Prefer = domain.DeriveSubtitlePrefer(tracks, pref.Include)
		}
		log.Info("subtitle preference (explicit)",
			domain.F("include", strings.Join(pref.Include, ", ")),
			domain.F("exclude", strings.Join(pref.Exclude, ", ")),
		)
		return withMode(pref)
	}

	// 2. Interactive menu.
	if menuActive && len(tracks) > 1 {
		chosen, err := e.deps.SubtitleChooser.ChooseSubtitles(tracks, cfg.SubsMenuTimeout)
		if err != nil {
			log.Warn("subtitle menu failed, keeping all tracks", domain.F("error", err.Error()))
			return withMode(domain.SubtitlePreference{})
		}
		if len(chosen) == 0 {
			return withMode(domain.SubtitlePreference{})
		}
		pref := domain.BuildSubtitlePreference(tracks, chosen)
		log.Info("subtitle preference (interactive)",
			domain.F("include", strings.Join(pref.Include, ", ")),
			domain.F("selected", len(chosen)),
		)
		return withMode(pref)
	}

	// 3. Keep everything.
	return withMode(domain.SubtitlePreference{})
}

// writeSubtitleSidecars writes the downloaded subtitle tracks as separate files
// next to the episode, via the optional SubtitleSidecarWriter dependency.
func (e *engine) writeSubtitleSidecars(
	ctx context.Context,
	job domain.Job,
	hls *domain.HLSDownloadResult,
) ([]string, error) {
	writer := e.deps.SubtitleSidecarWriter
	if writer == nil {
		// Fall back to the downloader, which implements the port in production
		// wiring; an explicit dependency is only needed to override it.
		w, ok := e.deps.Downloader.(domain.SubtitleSidecarWriter)
		if !ok {
			return nil, fmt.Errorf("writing subtitles to separate files is not supported by this downloader")
		}
		writer = w
	}
	return writer.WriteSubtitleSidecars(ctx, job, hls.SubtitleTracks)
}

// buildSeriesFromPlaylist constructs a domain.Series from page playlist data.
func (e *engine) buildSeriesFromPlaylist(playlist *domain.PagePlaylist, cfg domain.RunConfig) domain.Series {
	series := domain.Series{
		ID:        domain.SeriesID(fmt.Sprintf("%d", playlist.ItemID)),
		Title:     playlist.Title,
		PosterURL: playlist.Poster,
	}

	// Group episodes by season.
	seasonMap := make(map[int][]domain.Episode)
	for _, pe := range playlist.Episodes {
		ep := domain.Episode{
			Key: domain.EpisodeKey{
				Series:  series.ID,
				Season:  pe.Season,
				Episode: pe.Episode,
			},
			Title:    pe.EpisodeTitle,
			Duration: time.Duration(pe.Duration) * time.Second,
			MediaSources: []domain.MediaSource{{
				Kind: domain.MediaHLS,
				URL:  pe.ManifestURL,
			}},
		}
		seasonMap[pe.Season] = append(seasonMap[pe.Season], ep)
	}

	// Build sorted seasons.
	seasonNums := make([]int, 0, len(seasonMap))
	for n := range seasonMap {
		seasonNums = append(seasonNums, n)
	}
	sort.Ints(seasonNums)

	for _, sn := range seasonNums {
		eps := seasonMap[sn]
		sort.Slice(eps, func(i, j int) bool {
			return eps[i].Key.Episode < eps[j].Key.Episode
		})
		series.Seasons = append(series.Seasons, domain.Season{
			Number:   sn,
			Episodes: eps,
		})
	}

	return series
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// matchingEpisodes returns all episodes matching season/episode selection (ignoring completion state).
func (e *engine) matchingEpisodes(series domain.Series, cfg domain.RunConfig) []domain.Episode {
	var matched []domain.Episode
	for _, season := range series.Seasons {
		if !cfg.SeasonSel.Matches(season.Number) {
			continue
		}
		for _, ep := range season.Episodes {
			if !cfg.EpisodeSel.Matches(ep.Key.Episode) {
				continue
			}
			matched = append(matched, ep)
		}
	}
	return matched
}

// filterCompleted removes already-completed episodes from the list (unless ForceRedownload).
func (e *engine) filterCompleted(episodes []domain.Episode, state domain.DownloadState, cfg domain.RunConfig) []domain.Episode {
	if cfg.ForceRedownload {
		return episodes
	}
	var selected []domain.Episode
	for _, ep := range episodes {
		if !e.deps.StateStore.IsCompleted(state, ep.Key) {
			selected = append(selected, ep)
		}
	}
	return selected
}

// countSeasons counts episodes per season for the progress plan.
func countSeasons(episodes []domain.Episode) map[int]int {
	m := make(map[int]int)
	for _, ep := range episodes {
		m[ep.Key.Season]++
	}
	return m
}

// countCompletedPerSeason counts how many episodes per season are already
// completed. A --force run re-downloads everything, so nothing counts as done
// up front — otherwise the season bars would start full while the series bar
// starts empty, and each re-download would push a season past its own total.
func countCompletedPerSeason(allEpisodes []domain.Episode, state domain.DownloadState, store domain.StateStore, force bool) map[int]int {
	m := make(map[int]int)
	if force {
		return m
	}
	for _, ep := range allEpisodes {
		if store.IsCompleted(state, ep.Key) {
			m[ep.Key.Season]++
		}
	}
	return m
}

// seriesDirPath computes the series download directory path using the same
// sanitization logic as OutputLayout. This is used to place the state file
// inside the series folder.
func (e *engine) seriesDirPath(root string, series domain.Series) string {
	fallback := fmt.Sprintf("series_%s", string(series.ID))
	seriesDir := fsutil.SanitizeComponent(series.Title, fallback)
	return filepath.Join(root, seriesDir)
}

// downloadPoster downloads the series poster image to a temporary file in outputDir.
// Returns the path to the downloaded file, or an error if the download fails.
// The caller is responsible for removing the file when done.
func (e *engine) downloadPoster(ctx context.Context, posterURL, outputDir string) (string, error) {
	// Prefer the authenticated client: posters live behind the same Cloudflare
	// and CDN checks as the media, which reject the bare Go user agent and the
	// missing Referer.
	client := e.deps.AuthedHTTPClient
	if client == nil {
		client = e.deps.ProxyProvider.HTTPClient()
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, posterURL, nil)
	if err != nil {
		return "", fmt.Errorf("poster request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("poster download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("poster download: HTTP %d", resp.StatusCode)
	}

	// Write to a temp file in the output directory.
	posterPath := filepath.Join(outputDir, ".poster-cover.jpg")
	f, err := os.Create(posterPath)
	if err != nil {
		return "", fmt.Errorf("poster create file: %w", err)
	}

	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		os.Remove(posterPath)
		return "", fmt.Errorf("poster write: %w", copyErr)
	}
	if closeErr != nil {
		os.Remove(posterPath)
		return "", fmt.Errorf("poster close: %w", closeErr)
	}

	return posterPath, nil
}
