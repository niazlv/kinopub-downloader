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

	"kinopub_downloader/internal/domain"
	"kinopub_downloader/internal/lib/fsutil"
)

// engine orchestrates the download workflow using injected dependencies.
type engine struct {
	deps Dependencies
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

	// Try HLS pipeline first if available and input is a page link.
	if e.deps.HLSDownloader != nil && e.deps.PageScraper != nil && cfg.InputURL != "" && !cfg.NoChunked {
		result, err := e.runHLS(ctx, cfg)
		if err == nil {
			return result, nil
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

	// 1. Resolve input → FeedSource.
	var feedSrc domain.FeedSource
	if cfg.FeedFile != "" {
		log.Info("using local feed file", domain.F("path", cfg.FeedFile))
		feedSrc = domain.FeedSource{LocalPath: cfg.FeedFile}
		if cfg.InputURL != "" {
			if resolved, rerr := e.deps.InputResolver.Resolve(ctx, cfg.InputURL); rerr == nil {
				feedSrc.ID = resolved.ID
				feedSrc.Token = resolved.Token
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
			log.Info(fmt.Sprintf("  S%02dE%02d %s", ep.Key.Season, ep.Key.Episode, ep.Title))
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
		Total:              len(allMatching),
		Seasons:            countSeasons(allMatching),
		AlreadyCompleted:   alreadyCompleted,
		CompletedPerSeason: countCompletedPerSeason(allMatching, state, e.deps.StateStore),
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
					domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				)

				// Resolve media (ffprobe / HLS fetch).
				media, err := e.deps.MediaResolver.Resolve(ctx, ep, cfg.Quality)
				if err != nil {
					mu.Lock()
					consecutiveFails++
					cf := consecutiveFails
					log.Warn("media resolution failed",
						domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
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
					domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
					domain.F("quality", qualityInfo),
					domain.F("audio_tracks", len(media.Audio)),
				)

				// Build output path.
				outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, ep)
				if err != nil {
					log.Warn("output path failed, skipping",
						domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
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
						domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
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
						domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
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
			log.Info(fmt.Sprintf("  S%02dE%02d %s", ep.Key.Season, ep.Key.Episode, ep.Title))
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

	// 7. Start progress reporting.
	plan := domain.SeriesPlan{
		Total:              len(allMatching),
		Seasons:            countSeasons(allMatching),
		AlreadyCompleted:   alreadyCompleted,
		CompletedPerSeason: countCompletedPerSeason(allMatching, state, e.deps.StateStore),
	}
	e.deps.ProgressReporter.Start(plan)
	defer e.deps.ProgressReporter.Stop()

	// 8. Download episodes (sequential for HLS — each episode is already segmented).
	var succeeded, failed, skipped int
	var consecutiveCDNFails int
	var outcomes []domain.JobOutcome

	for _, ep := range selected {
		if ctx.Err() != nil {
			log.Info("interrupted")
			break
		}

		manifestURL, ok := manifestMap[ep.Key]
		if !ok {
			log.Warn("no manifest URL for episode, skipping",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
			)
			skipped++
			continue
		}

		// Build output path.
		outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, ep)
		if err != nil {
			log.Warn("output path failed", domain.F("error", err.Error()))
			skipped++
			continue
		}
		if err := e.deps.OutputLayout.EnsureDirs(outPath); err != nil {
			log.Warn("cannot create directory", domain.F("error", err.Error()))
			skipped++
			continue
		}

	retryEpisode:
		// Download via HLS.
		e.deps.ProgressReporter.EpisodeStarted(ep.Key)

		tsPath := outPath + ".ts"
		hlsResult, dlErr := e.deps.HLSDownloader.DownloadEpisode(ctx, manifestURL, cfg.Quality, tsPath, ep.Key, e.deps.ProgressReporter)

		if dlErr != nil {
			log.Warn("HLS download failed",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", dlErr.Error()),
			)

			// If CDN is returning 502/503/504, wait and retry the same episode
			// instead of skipping to the next one (CDN will recover eventually).
			// Keep the partial segments (.hls-tmp) so the retry resumes.
			if strings.Contains(dlErr.Error(), "502") ||
				strings.Contains(dlErr.Error(), "503") ||
				strings.Contains(dlErr.Error(), "504") {
				consecutiveCDNFails++
				waitTime := time.Duration(consecutiveCDNFails) * 30 * time.Second
				if waitTime > 5*time.Minute {
					waitTime = 5 * time.Minute
				}
				log.Info("CDN unavailable, waiting before retry",
					domain.F("wait", waitTime.String()),
					domain.F("attempt", consecutiveCDNFails),
				)
				select {
				case <-ctx.Done():
					return domain.RunResult{Total: len(selected), Succeeded: succeeded, Failed: failed, Skipped: skipped, Outcomes: outcomes}, ctx.Err()
				case <-time.After(waitTime):
				}
				// Retry the same episode — don't advance the loop.
				// We do this by decrementing the loop counter... but we're using range.
				// Instead, just re-attempt inline.
				goto retryEpisode
			}

			// Non-CDN error — skip this episode.
			consecutiveCDNFails = 0
			outcomes = append(outcomes, domain.JobOutcome{Key: ep.Key, Err: dlErr, Attempts: 1})
			failed++
			e.deps.ProgressReporter.EpisodeFailed(ep.Key, dlErr)
			continue
		}
		consecutiveCDNFails = 0

		// Mux downloaded video + audio streams into the final container.
		log.Info("muxing",
			domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
			domain.F("quality", fmt.Sprintf("%s @ %d kbps", hlsResult.Resolution, hlsResult.BitrateKbps)),
			domain.F("audio_tracks", len(hlsResult.AudioTracks)),
		)

		muxJob := domain.Job{
			Episode:     ep,
			OutPath:     outPath,
			PosterPath:  posterPath,
			SeriesTitle: series.Title,
		}

		var remuxErr error
		if muxer, ok := e.deps.Downloader.(domain.HLSMuxer); ok {
			remuxErr = muxer.MuxHLS(ctx, muxJob, hlsResult)
		} else {
			remuxErr = fmt.Errorf("downloader does not support HLS muxing")
		}

		// Clean up temp segment files regardless of mux outcome.
		if hlsResult.TempDir != "" {
			os.RemoveAll(hlsResult.TempDir)
		}

		if remuxErr != nil {
			log.Warn("remux failed",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", remuxErr.Error()),
			)
			outcomes = append(outcomes, domain.JobOutcome{Key: ep.Key, Err: remuxErr, Attempts: 1})
			failed++
			e.deps.ProgressReporter.EpisodeFailed(ep.Key, remuxErr)
			continue
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

		outcomes = append(outcomes, domain.JobOutcome{Key: ep.Key, Succeeded: true, Attempts: 1})
		succeeded++
		e.deps.ProgressReporter.EpisodeCompleted(ep.Key)
	}

	return domain.RunResult{
		Total:     len(selected),
		Succeeded: succeeded,
		Failed:    failed,
		Skipped:   skipped,
		Outcomes:  outcomes,
	}, nil
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

// selectEpisodes filters episodes by season/episode selection and completion state.
func (e *engine) selectEpisodes(series domain.Series, state domain.DownloadState, cfg domain.RunConfig) []domain.Episode {
	return e.filterCompleted(e.matchingEpisodes(series, cfg), state, cfg)
}

// countSeasons counts episodes per season for the progress plan.
func countSeasons(episodes []domain.Episode) map[int]int {
	m := make(map[int]int)
	for _, ep := range episodes {
		m[ep.Key.Season]++
	}
	return m
}

// countCompletedPerSeason counts how many episodes per season are already completed.
func countCompletedPerSeason(allEpisodes []domain.Episode, state domain.DownloadState, store domain.StateStore) map[int]int {
	m := make(map[int]int)
	for _, ep := range allEpisodes {
		if store.IsCompleted(state, ep.Key) {
			m[ep.Key.Season]++
		}
	}
	return m
}

// downloadExecutor adapts the Downloader interface to the JobExecutor interface
// expected by the Scheduler. Kept for compatibility.
type downloadExecutor struct {
	downloader domain.Downloader
	reporter   domain.ProgressReporter
}

// Execute implements domain.JobExecutor.
func (d *downloadExecutor) Execute(ctx context.Context, job domain.Job) error {
	d.reporter.EpisodeStarted(job.Episode.Key)
	return d.downloader.Download(ctx, job, d.reporter)
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
	client := e.deps.ProxyProvider.HTTPClient()

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
