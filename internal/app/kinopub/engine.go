package kinopub

import (
	"context"

	"kinopub_downloader/internal/domain"
)

// engine orchestrates the download workflow using injected dependencies.
type engine struct {
	deps Dependencies
}

// run executes the full download pipeline:
// classify → parse → load state → filter → resolve media → build jobs → submit → collect outcomes.
func (e *engine) run(ctx context.Context, cfg domain.RunConfig) (domain.RunResult, error) {
	log := e.deps.Logger.Component("engine")

	// 1. Resolve input → FeedSource.
	// When a local feed file is configured, use it directly and only try to
	// derive the SeriesID from the URL when one is supplied. Otherwise classify
	// and resolve the URL as usual.
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

	// 4. Filter episodes by SeasonSel/EpisodeSel, skip completed (unless ForceRedownload)
	selected := e.selectEpisodes(series, state, cfg)
	if len(selected) == 0 {
		log.Info("no episodes to download")
		return domain.RunResult{Total: 0}, nil
	}

	// 5. Resolve media for each selected episode
	type resolvedEpisode struct {
		episode domain.Episode
		media   domain.ResolvedMedia
	}
	var resolved []resolvedEpisode
	for _, ep := range selected {
		log.Debug("resolving media", domain.F("season", ep.Key.Season), domain.F("episode", ep.Key.Episode))
		media, err := e.deps.MediaResolver.Resolve(ctx, ep, cfg.Quality)
		if err != nil {
			log.Warn("media resolution failed, skipping episode",
				domain.F("season", ep.Key.Season),
				domain.F("episode", ep.Key.Episode),
				domain.F("error", err.Error()),
			)
			continue
		}
		resolved = append(resolved, resolvedEpisode{episode: ep, media: media})
	}

	if len(resolved) == 0 {
		log.Info("no episodes could be resolved")
		return domain.RunResult{Total: len(selected), Skipped: len(selected)}, nil
	}

	// 6. Build jobs (with output paths from OutputLayout)
	var jobs []domain.Job
	for _, r := range resolved {
		outPath, err := e.deps.OutputLayout.EpisodePath(cfg.OutputPath, series, r.episode)
		if err != nil {
			log.Warn("output path derivation failed, skipping episode",
				domain.F("season", r.episode.Key.Season),
				domain.F("episode", r.episode.Key.Episode),
				domain.F("error", err.Error()),
			)
			continue
		}
		if err := e.deps.OutputLayout.EnsureDirs(outPath); err != nil {
			log.Warn("cannot create output directory, skipping episode",
				domain.F("path", outPath),
				domain.F("error", err.Error()),
			)
			continue
		}
		jobs = append(jobs, domain.Job{
			Episode: r.episode,
			Media:   r.media,
			OutPath: outPath,
		})
	}

	// 7. If DryRun: return listing without downloading
	if cfg.DryRun {
		log.Info("dry run — listing jobs without downloading", domain.F("count", len(jobs)))
		outcomes := make([]domain.JobOutcome, len(jobs))
		for i, j := range jobs {
			outcomes[i] = domain.JobOutcome{
				Key:       j.Episode.Key,
				Succeeded: false,
			}
		}
		return domain.RunResult{
			Total:    len(selected),
			Skipped:  len(selected) - len(jobs),
			Outcomes: outcomes,
		}, nil
	}

	// Start progress reporting
	plan := buildSeriesPlan(jobs)
	e.deps.ProgressReporter.Start(plan)
	defer e.deps.ProgressReporter.Stop()

	// 8. Submit jobs to Scheduler with Downloader as executor
	log.Info("starting downloads", domain.F("jobs", len(jobs)))
	executor := &downloadExecutor{
		downloader: e.deps.Downloader,
		reporter:   e.deps.ProgressReporter,
	}
	summary := e.deps.Scheduler.Run(ctx, jobs, executor)

	// 9. For each succeeded job: mark completed in StateStore
	for _, outcome := range summary.Outcomes {
		if outcome.Succeeded {
			if err := e.deps.StateStore.MarkCompleted(ctx, outcome.Key); err != nil {
				log.Warn("failed to persist completion state",
					domain.F("season", outcome.Key.Season),
					domain.F("episode", outcome.Key.Episode),
					domain.F("error", err.Error()),
				)
			}
			e.deps.ProgressReporter.EpisodeCompleted(outcome.Key)
		} else {
			e.deps.ProgressReporter.EpisodeFailed(outcome.Key, outcome.Err)
		}
	}

	// 10. Return RunResult
	skipped := len(selected) - len(jobs)
	return domain.RunResult{
		Total:     len(selected),
		Succeeded: summary.Succeeded,
		Failed:    summary.Failed,
		Skipped:   skipped,
		Outcomes:  summary.Outcomes,
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
			// Skip completed unless ForceRedownload
			if !cfg.ForceRedownload && e.deps.StateStore.IsCompleted(state, ep.Key) {
				continue
			}
			selected = append(selected, ep)
		}
	}
	return selected
}

// buildSeriesPlan constructs a SeriesPlan from the set of jobs to execute.
func buildSeriesPlan(jobs []domain.Job) domain.SeriesPlan {
	seasons := make(map[int]int)
	for _, j := range jobs {
		seasons[j.Episode.Key.Season]++
	}
	return domain.SeriesPlan{
		Total:   len(jobs),
		Seasons: seasons,
	}
}

// downloadExecutor adapts the Downloader interface to the JobExecutor interface
// expected by the Scheduler.
type downloadExecutor struct {
	downloader domain.Downloader
	reporter   domain.ProgressReporter
}

// Execute implements domain.JobExecutor.
func (d *downloadExecutor) Execute(ctx context.Context, job domain.Job) error {
	d.reporter.EpisodeStarted(job.Episode.Key)
	return d.downloader.Download(ctx, job, d.reporter)
}
