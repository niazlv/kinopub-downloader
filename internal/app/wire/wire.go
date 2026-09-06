// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package wire assembles the real services behind a run: proxy, auth-aware
// HTTP, scrapers, the HLS pipeline, ffmpeg, progress and notifications. It
// used to live in the CLI's main; it moved here so the desktop app and the
// Android shell build the very same pipeline the command line does, instead
// of a second one that drifts.
package wire

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/app/kinopub"
	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/audiomenu"
	"github.com/niazlv/kinopub-downloader/internal/lib/desktopnotify"
	"github.com/niazlv/kinopub-downloader/internal/lib/httpx"
	"github.com/niazlv/kinopub-downloader/internal/lib/kplog"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
	"github.com/niazlv/kinopub-downloader/internal/lib/termuxapi"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
	"github.com/niazlv/kinopub-downloader/internal/services/apiscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/downloader"
	"github.com/niazlv/kinopub-downloader/internal/services/feedparser"
	"github.com/niazlv/kinopub-downloader/internal/services/hlsdownloader"
	"github.com/niazlv/kinopub-downloader/internal/services/inputresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/manifestscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/mediaresolver"
	"github.com/niazlv/kinopub-downloader/internal/services/outputlayout"
	"github.com/niazlv/kinopub-downloader/internal/services/pagescraper"
	"github.com/niazlv/kinopub-downloader/internal/services/platformscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/progress"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
	"github.com/niazlv/kinopub-downloader/internal/services/statestore"
	kpapi "github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

// Options is what the console decides and the wiring only carries: colors,
// whether stderr is a terminal (live bars instead of log lines), whether menus
// may ask questions, and an explicit progress reporter for hosts that draw
// progress themselves (the desktop app, a JSON pipe to the Android shell).
type Options struct {
	Style       termx.Styler
	Console     bool
	Interactive bool
	Reporter    domain.ProgressReporter
	Stderr      io.Writer
	Stdin       io.Reader
}

func (o Options) stderr() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

func (o Options) stdin() io.Reader {
	if o.Stdin != nil {
		return o.Stdin
	}
	return os.Stdin
}

// Build constructs all real service implementations and returns the
// Dependencies struct, a cleanup function, and any error. Console-specific
// choices (colors, live bars, interactive menus) come through Options, so the
// same wiring serves the CLI, the desktop app and a JSON-driven run.
func Build(cfg *domain.RunConfig, o Options) (kinopub.Dependencies, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// Coordinator for TTY line-discipline.
	coord := logx.NewCoordinator(o.stderr())

	// Build logger handlers.
	handlers := ConsoleHandlers(cfg.Verbosity, coord, o)

	// Open log file if configured.
	if cfg.LogFilePath != "" {
		// Owner-only: the log records request URLs and headers, which carry feed
		// tokens and signed media links.
		f, err := os.OpenFile(cfg.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return kinopub.Dependencies{}, cleanup, fmt.Errorf("cannot open log file: %w", err)
		}
		cleanups = append(cleanups, func() { f.Close() })
		handlers = append(handlers, logx.NewFileHandler(f, nil))
	}

	logger := logx.New(handlers)

	// Proxy provider.
	proxyProv, err := proxyprovider.New(cfg.ProxyURL)
	if err != nil {
		return kinopub.Dependencies{}, cleanup, err
	}

	// Build the auth-aware HTTP client: wrap the proxy client so every request
	// carries the configured Cookie / User-Agent / extra headers.
	auth := domain.RequestAuth{
		Cookie:    cfg.Cookie,
		UserAgent: cfg.UserAgent,
		Headers:   cfg.Headers,
		Site:      cfg.Site,
	}
	// Always include a Referer pointing at the site — the CDN (digital-cdn.net)
	// requires it and will hang/timeout without it.
	if auth.Headers == nil {
		auth.Headers = make(map[string]string)
	}
	if auth.Headers["Referer"] == "" {
		auth.Headers["Referer"] = cfg.Site.Referer()
	}
	httpClient := httpx.WithAuth(proxyProv.HTTPClient(), auth)

	// Input resolver — with page scraper when auth is available.
	var resolverOpts []inputresolver.Option
	if auth.HasCredentials() {
		scraper := pagescraper.New(httpClient, logger)
		resolverOpts = append(resolverOpts, inputresolver.WithPageScraper(scraper))
	}
	inputRes := inputresolver.New(logger, resolverOpts...)

	// Feed parser. Links inside the feed that still name a former site domain
	// are moved onto the current one, unless --no-domain-rewrite says otherwise.
	feedPars := feedparser.New(httpClient, logger,
		feedparser.WithDomainRewrite(!cfg.NoDomainRewrite))

	// Media resolver — needs a RunOutput function for ffprobe. Its m3u8 fetches
	// go straight to the CDN, which gates HLS on cf_clearance, so this client
	// carries the cookie everywhere rather than only to the site.
	mediaRes := mediaresolver.New(
		httpx.WithAuthCDNCookies(proxyProv.HTTPClient(), auth),
		MakeRunOutput(),
		logger,
		auth,
	)

	// Output layout.
	layout := outputlayout.New(cfg.Container)

	// State store.
	outputDir := cfg.OutputPath
	if outputDir == "" {
		outputDir, _ = os.Getwd()
	}
	stateStr := statestore.New(outputDir, logger)

	// Downloader.
	dl := downloader.New(
		MakeRunFunc(),
		proxyProv,
		logger,
		downloader.WithFFmpegPath(cfg.FFmpegPath),
		downloader.WithAuth(auth),
		downloader.WithExtraArgs(cfg.FFmpegExtraArgs),
		downloader.WithNoChunked(cfg.NoChunked),
		downloader.WithHTTPClient(httpClient),
	)

	// Progress reporter — choose live or log based on TTY.
	progReporter := o.Reporter
	if progReporter == nil {
		if o.Console {
			progReporter = progress.NewLive(o.stderr(), coord, progress.WithColor(o.Style.Enabled()))
		} else {
			progReporter = progress.NewLog(logger)
		}
	}
	// System notifications, unless the user turned them off for this run
	// (--no-notify) or for good (`kinopub config set notifications off`).
	if !cfg.NoNotify {
		// Wrap with Termux notifications if termux-notification is available.
		progReporter = termuxapi.Wrap(progReporter, termuxapi.WithLogger(logger))
		// And with native desktop notifications on macOS/Linux (no-op elsewhere,
		// and on Termux, which the richer Termux notifier above already covers).
		progReporter = desktopnotify.Wrap(progReporter)
	}

	deps := kinopub.Dependencies{
		Logger:           logger,
		InputResolver:    inputRes,
		FeedParser:       feedPars,
		MediaResolver:    mediaRes,
		Downloader:       dl,
		ProxyProvider:    proxyProv,
		ProgressReporter: progReporter,
		StateStore:       stateStr,
		OutputLayout:     layout,
		AuthedHTTPClient: httpClient,
	}

	// Optional HLS pipeline. Two ways to feed it:
	//   - --api: the JSON API backend, which fetches items with a Bearer token
	//     and yields signed hls4 manifests. No cookie needed; the token and the
	//     app User-Agent were resolved into cfg by prepareAppSession.
	//   - website: the HTML page scraper, available only when a cookie is
	//     present (the player page is behind Cloudflare + auth).
	//   - --from: a platform manifest. The platform already resolved the
	//     source, so this path needs no site credentials at all.
	switch {
	case cfg.FromURL != "":
		// Манифест забирается один раз: ссылка короткоживущая, и ходить по
		// ней повторно незачем.
		mctx, cancelManifest := context.WithTimeout(context.Background(), 30*time.Second)
		m, err := manifestscraper.Fetch(mctx, httpClient, cfg.FromURL)
		cancelManifest()
		if err != nil {
			return deps, cleanup, err
		}
		ApplyManifestSelection(cfg, m, logger)
		deps.PageScraper = manifestscraper.New(m, logger)
		deps.HLSDownloader = hlsdownloader.New(httpClient, auth, logger,
			hlsdownloader.WithConcurrency(cfg.MaxConcurrency),
			hlsdownloader.WithProxy(proxyProv.ProxyURL()),
			hlsdownloader.WithRateLimit(cfg.RateLimit))
	case cfg.AppMode:
		apiCli := kpapi.New(proxyProv.HTTPClient(), cfg.APIBase, cfg.AppToken,
			kpapi.WithUserAgent(cfg.UserAgent), kpapi.WithLogger(kplog.Adapt(logger)))
		deps.PageScraper = apiscraper.New(apiCli, logger,
			apiscraper.WithPreferredCodec(cfg.AppCodec))
		deps.HLSDownloader = hlsdownloader.New(httpClient, auth, logger,
			hlsdownloader.WithConcurrency(cfg.MaxConcurrency),
			hlsdownloader.WithProxy(proxyProv.ProxyURL()),
			hlsdownloader.WithRateLimit(cfg.RateLimit))
	case auth.HasCredentials():
		// A platform page is read through the platform's API, not its HTML:
		// the page is an app shell with nothing in it, and the platform hands
		// a logged-in user the episode list and a manifest per episode.
		var scraper domain.PageScraper = pagescraper.New(httpClient, logger)
		if _, ok := domain.ParsePlatformLink(cfg.InputURL); ok {
			// The selection already carries the link's own season/episode
			// suffix (ApplyURLEpisodeRef), so the scraper asks the platform
			// for exactly the episodes the run will download.
			scraper = platformscraper.New(httpClient, logger,
				platformscraper.WithSelection(cfg.SeasonSel, cfg.EpisodeSel))
		}
		hlsDl := hlsdownloader.New(httpClient, auth, logger,
			hlsdownloader.WithConcurrency(cfg.MaxConcurrency),
			hlsdownloader.WithProxy(proxyProv.ProxyURL()),
			hlsdownloader.WithRateLimit(cfg.RateLimit))
		deps.PageScraper = scraper
		deps.HLSDownloader = hlsDl
	}

	// Interactive audio-track picker. Only meaningful when the menu is enabled
	// and stdin/stderr are a real terminal.
	if cfg.AudioMenu && o.Interactive {
		deps.AudioChooser = audiomenu.New(o.stdin(), o.stderr(), true, audiomenu.WithColor(o.Style.Enabled()))
	}

	// Interactive subtitle-track picker, on the same terms as the audio one.
	if cfg.SubsMenu && o.Interactive {
		deps.SubtitleChooser = audiomenu.New(o.stdin(), o.stderr(), true, audiomenu.WithColor(o.Style.Enabled()))
	}

	// Interactive video-quality picker, likewise.
	if cfg.VideoMenu && o.Interactive {
		deps.VideoChooser = audiomenu.New(o.stdin(), o.stderr(), true, audiomenu.WithColor(o.Style.Enabled()))
	}

	return deps, cleanup, nil
}

// ConsoleHandlers picks the stderr log handler every subcommand shares: the
// compact, colored layout for a terminal, the timestamped plain one for a file
// or a pipe. --color=always keeps the compact layout in a pipe, because asking
// for color is asking for the terminal presentation.
func ConsoleHandlers(verb domain.Verbosity, coord *logx.Coordinator, o Options) []logx.Handler {
	if o.Console || o.Style.Enabled() {
		return []logx.Handler{logx.NewConsoleHandler(o.stderr(), verb, coord, o.Style)}
	}
	return []logx.Handler{logx.NewPlainHandler(o.stderr(), verb, coord)}
}

// MakeRunOutput creates a RunOutputFunc that executes a command and captures stdout.
// On failure, stderr is included in the error message for diagnostics.
func MakeRunOutput() mediaresolver.RunOutputFunc {
	return func(ctx context.Context, name string, args, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			errMsg := stderr.String()
			if errMsg != "" {
				return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(errMsg))
			}
			return nil, err
		}
		return out, nil
	}
}

// MakeRunFunc creates a downloader.RunFunc that executes a command, streaming
// stdout to the provided writer. ffmpeg stderr is discarded in interactive mode
// to keep the progress display clean — all useful progress info comes via
// -progress pipe:1 on stdout.
func MakeRunFunc() downloader.RunFunc {
	return func(ctx context.Context, name string, args, env []string, stdout io.Writer) error {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		cmd.Stdout = stdout
		// Discard ffmpeg stderr: the verbose codec/stream info clutters the
		// live progress display. Errors are detected via the exit code.
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
}

// ApplyManifestSelection переносит выбор, сделанный в интерфейсе платформы,
// в настройки прогона — но только там, где человек не сказал иного флагом.
// Явный флаг всегда сильнее: он набран сейчас, а манифест выписан раньше.
//
// Дорожки в манифесте отмечены идентификаторами, а отбор в утилите идёт по
// подстроке имени или языка, поэтому идентификаторы переводятся в подписи.
// Отсюда и ограничение: две дорожки с похожими подписями отбором не
// различить — тогда останутся обе, что безопаснее, чем потерять нужную.
func ApplyManifestSelection(cfg *domain.RunConfig, m *manifestscraper.Manifest, logger domain.Logger) {
	log := logger.Component("manifest")

	if cfg.Quality == "" && m.Select.MaxHeight > 0 {
		cfg.Quality = domain.Quality(fmt.Sprintf("%dp", m.Select.MaxHeight))
		log.Debug("quality taken from the manifest", domain.F("quality", string(cfg.Quality)))
	}

	if cfg.AudioPref.IsAll() {
		if pats := patternsFor(m.Audios, m.Select.Audios); len(pats) > 0 {
			cfg.AudioPref = domain.AudioPreference{Include: pats}
			log.Debug("audio selection taken from the manifest", domain.F("tracks", len(pats)))
		}
	}
	if cfg.SubsPref.IsAll() {
		if pats := patternsFor(m.Subtitles, m.Select.Subtitles); len(pats) > 0 {
			cfg.SubsPref = domain.SubtitlePreference{Include: pats}
			log.Debug("subtitle selection taken from the manifest", domain.F("tracks", len(pats)))
		}
	}
}

// patternsFor переводит выбранные идентификаторы в подписи для отбора.
// Пустой список идентификаторов означает «всё» — тогда сужать нечего.
func patternsFor(tracks []manifestscraper.Track, ids []string) []string {
	if len(ids) == 0 || len(ids) == len(tracks) {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, t := range tracks {
		if !want[t.ID] {
			continue
		}
		switch {
		case t.Title != "":
			out = append(out, t.Title)
		case t.Language != "":
			out = append(out, t.Language)
		}
	}
	return out
}
