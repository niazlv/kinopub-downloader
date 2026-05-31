package kinopub

import (
	"context"
	"fmt"
	"time"

	"kinopub_downloader/internal/domain"
)

// engine orchestrates the download workflow using injected dependencies.
type engine struct {
	deps Dependencies
}

// consecutiveFailLimit is the number of consecutive media resolution failures
// before the engine stops trying further episodes. This prevents hammering the
// CDN when links have expired or the server is blocking.
const consecutiveFailLimit = 3

// run executes the full download pipeline with LAZY resolution:
// parse feed → filter → for each episode: resolve → download → mark complete.
// This avoids resolving all episodes upfront (CDN links expire quickly).
func (e *engine) run(ctx context.Context, cfg domain.RunConfig) (domain.RunResult, error) {
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

	// 3. Load state for the series
	state, err := e.deps.StateStore.Load(ctx, series.ID)
	if err != nil {
		return domain.RunResult{}, err
	}

	// 4. Filter episodes by SeasonSel/EpisodeSel, skip completed
	selected := e.selectEpisodes(series, state, cfg)
	if len(selected) == 0 {
		log.Info("no episodes to download")
		return domain.RunResult{Total: 0}, nil
	}

	log.Info("episodes selected for download", domain.F("count", len(selected)))

	// 5. DryRun: just list what would be downloaded.
	if cfg.DryRun {
		log.Info("dry run — listing episodes without downloading")
		for _, ep := range selected {
			log.Info(fmt.Sprintf("  S%02dE%02d %s", ep.Key.Season, ep.Key.Episode, ep.Title))
		}
		return domain.RunResult{Total: len(selected)}, nil
	}

	// 6. Lazy resolve + download: process episodes one by one.
	// Resolve media immediately before downloading so CDN links are fresh.
	// If consecutiveFailLimit failures in a row → stop (links expired / blocked).
	var (
		succeeded      int
		failed         int
		skipped        int
		consecutiveFails int
		outcomes       []domain.JobOutcome
	)

	// Start progress reporting.
	plan := domain.SeriesPlan{
		Total:   len(selected),
		Seasons: countSeasons(selected),
	}
	e.deps.ProgressReporter.Start(plan)
	defer e.deps.ProgressReporter.Stop()

	for i, ep := range selected {
		// Check context (SIGINT).
		if ctx.Err() != nil {
			log.Info("interrupted, stopping")
			break
		}

		log.Info("resolving media",
			domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
			domain.F("progress", fmt.Sprintf("%d/%d", i+1, len(selected))),
		)

		// Resolve media (ffprobe / HLS fetch).
		media, err := e.deps.MediaResolver.Resolve(ctx, ep, cfg.Quality)
		if err != nil {
			consecutiveFails++
			log.Warn("media resolution failed",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", err.Error()),
				domain.F("consecutive_fails", consecutiveFails),
			)
			if consecutiveFails >= consecutiveFailLimit {
				log.Warn("stopping: too many consecutive failures — CDN links may have "+
					"expired. Try re-fetching the feed for fresh links.",
					domain.F("succeeded_so_far", succeeded),
					domain.F("remaining", len(selected)-i-1),
				)
				skipped += len(selected) - i - 1
				outcomes = append(outcomes, domain.JobOutcome{
					Key: ep.Key, Succeeded: false, Err: err, Attempts: 1,
				})
				failed++
				break
			}
			outcomes = append(outcomes, domain.JobOutcome{
				Key: ep.Key, Succeeded: false, Err: err, Attempts: 1,
			})
			failed++
			e.deps.ProgressReporter.EpisodeFailed(ep.Key, err)
			continue
		}
		// Success resets the counter.
		consecutiveFails = 0

		// Build output path.
		outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, ep)
		if err != nil {
			log.Warn("output path failed, skipping",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", err.Error()),
			)
			skipped++
			continue
		}
		if err := e.deps.OutputLayout.EnsureDirs(outPath); err != nil {
			log.Warn("cannot create directory, skipping",
				domain.F("path", outPath), domain.F("error", err.Error()),
			)
			skipped++
			continue
		}

		job := domain.Job{Episode: ep, Media: media, OutPath: outPath}

		// Download.
		e.deps.ProgressReporter.EpisodeStarted(ep.Key)
		dlErr := e.deps.Downloader.Download(ctx, job, e.deps.ProgressReporter)
		if dlErr != nil {
			log.Warn("download failed",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", dlErr.Error()),
			)
			outcomes = append(outcomes, domain.JobOutcome{
				Key: ep.Key, Succeeded: false, Err: dlErr, Attempts: 1,
			})
			failed++
			e.deps.ProgressReporter.EpisodeFailed(ep.Key, dlErr)
			// A download failure is not necessarily a CDN block (could be a
			// transient network issue), so we don't increment consecutiveFails.
			continue
		}

		// Mark completed.
		if err := e.deps.StateStore.MarkCompleted(ctx, ep.Key); err != nil {
			log.Warn("failed to persist state",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", ep.Key.Season, ep.Key.Episode)),
				domain.F("error", err.Error()),
			)
		}
		outcomes = append(outcomes, domain.JobOutcome{
			Key: ep.Key, Succeeded: true, Attempts: 1,
		})
		succeeded++
		e.deps.ProgressReporter.EpisodeCompleted(ep.Key)

		// Brief pause between episodes to be gentle on the CDN.
		if i < len(selected)-1 {
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}

	return domain.RunResult{
		Total:     len(selected),
		Succeeded: succeeded,
		Failed:    failed,
		Skipped:   skipped,
		Outcomes:  outcomes,
	}, nil
}

// selectEpisodes filters episodes by season/episode selection and completion state.
func (e *engine) selectEpisodes(series domain.Series, state domain.DownloadState, cfg domain.RunConfig) []domain.Episode {
	var selected []domain.Episode
	for _, season := range series.Seasons {
		if !cfg.SeasonSel.Matches(season.Number) {
			continue
		}
		for _, ep := range season.Episodes {
			if !cfg.EpisodeSel.Matches(ep.Key.Episode) {
				continue
			}
			if !cfg.ForceRedownload && e.deps.StateStore.IsCompleted(state, ep.Key) {
				continue
			}
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

// downloadExecutor adapts the Downloader interface to the JobExecutor interface
// expected by the Scheduler. Kept for compatibility but the lazy engine no
// longer uses the Scheduler for orchestration.
type downloadExecutor struct {
	downloader domain.Downloader
	reporter   domain.ProgressReporter
}

// Execute implements domain.JobExecutor.
func (d *downloadExecutor) Execute(ctx context.Context, job domain.Job) error {
	d.reporter.EpisodeStarted(job.Episode.Key)
	return d.downloader.Download(ctx, job, d.reporter)
}
