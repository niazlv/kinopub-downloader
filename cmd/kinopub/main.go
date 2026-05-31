// Package main is the CLI entrypoint for the kinopub downloader.
// It parses flags, builds the RunConfig, wires up all services, and
// delegates to the app composition root (Req 1.4, 7.3, 15.1, 15.2, 15.3, 16.3).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"kinopub_downloader/internal/app/kinopub"
	"kinopub_downloader/internal/domain"
	"kinopub_downloader/internal/lib/logx"
	"kinopub_downloader/internal/lib/termx"
	"kinopub_downloader/internal/services/downloader"
	"kinopub_downloader/internal/services/feedparser"
	"kinopub_downloader/internal/services/inputresolver"
	"kinopub_downloader/internal/services/mediaresolver"
	"kinopub_downloader/internal/services/outputlayout"
	"kinopub_downloader/internal/services/progress"
	"kinopub_downloader/internal/services/proxyprovider"
	"kinopub_downloader/internal/services/scheduler"
	"kinopub_downloader/internal/services/statestore"
)

const version = "0.1.0"

func main() {
	os.Exit(run())
}

func run() int {
	// Define flags.
	var (
		output      string
		concurrency int
		retries     int
		proxyURL    string
		quality     string
		verbosity   string
		ffmpegPath  string
		logFile     string
		container   string
		force       bool
		seasons     string
		episodes    string
		dryRun      bool
		minInterval int
		showVersion bool
	)

	fs := flag.NewFlagSet("kinopub", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&output, "output", "", "output directory path")
	fs.StringVar(&output, "o", "", "output directory path (shorthand)")
	fs.IntVar(&concurrency, "concurrency", 0, "max concurrent downloads (default: 2)")
	fs.IntVar(&concurrency, "c", 0, "max concurrent downloads (shorthand)")
	fs.IntVar(&retries, "retries", 0, "max retry count (default: 5)")
	fs.StringVar(&proxyURL, "proxy", "", "proxy URL (http, https, or socks5)")
	fs.StringVar(&quality, "quality", "", "quality preference (e.g. 1080p)")
	fs.StringVar(&quality, "q", "", "quality preference (shorthand)")
	fs.StringVar(&verbosity, "verbosity", "normal", "log verbosity: quiet, normal, verbose")
	fs.StringVar(&verbosity, "v", "normal", "log verbosity (shorthand)")
	fs.StringVar(&ffmpegPath, "ffmpeg", "", "ffmpeg binary path (default: ffmpeg on PATH)")
	fs.StringVar(&logFile, "log-file", "", "log file path")
	fs.StringVar(&container, "container", "mkv", "output container: mkv or mp4")
	fs.BoolVar(&force, "force", false, "force re-download of completed episodes")
	fs.StringVar(&seasons, "seasons", "", "season selection (e.g. 1,3-5)")
	fs.StringVar(&episodes, "episodes", "", "episode selection (e.g. 1,3-5)")
	fs.BoolVar(&dryRun, "dry-run", false, "list episodes without downloading")
	fs.IntVar(&minInterval, "min-interval", 0, "minimum interval between requests in ms")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "kinopub %s — download full-fidelity video from kino.pub\n\n", version)
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  kinopub [flags] <url>\n\n")
		fmt.Fprintf(os.Stderr, "The <url> is either a kino.pub page link or a tokenized podcast RSS feed link.\n")
		fmt.Fprintf(os.Stderr, "It downloads every episode's video, all audio tracks (labeled by dubbing\n")
		fmt.Fprintf(os.Stderr, "studio), and all subtitle tracks, muxing each episode with ffmpeg.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  # Download an entire series into ./downloads\n")
		fmt.Fprintf(os.Stderr, "  kinopub -o ./downloads https://kino.pub/podcast/get/12345/token\n\n")
		fmt.Fprintf(os.Stderr, "  # List what would be downloaded without writing files\n")
		fmt.Fprintf(os.Stderr, "  kinopub --dry-run https://kino.pub/podcast/get/12345/token\n\n")
		fmt.Fprintf(os.Stderr, "  # Only seasons 1 and 3-5, 1080p, through a proxy\n")
		fmt.Fprintf(os.Stderr, "  kinopub --seasons 1,3-5 -q 1080p --proxy socks5://127.0.0.1:1080 <url>\n")
	}

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	// --version
	if showVersion {
		fmt.Printf("kinopub %s\n", version)
		return 0
	}

	// Validate exactly one positional URL argument (Req 1.4).
	args := fs.Args()
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "Error: %s\n\n", domain.ErrExactlyOneURL.Error())
		fs.Usage()
		return 1
	}
	inputURL := args[0]

	// Parse verbosity.
	verb, err := parseVerbosity(verbosity)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Parse container.
	cont, err := parseContainer(container)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Parse season/episode selections.
	seasonSel, err := kinopub.ParseSelection(seasons)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	episodeSel, err := kinopub.ParseSelection(episodes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Build RunConfig.
	cfg := domain.RunConfig{
		InputURL:        inputURL,
		OutputPath:      output,
		MaxConcurrency:  concurrency,
		MaxRetries:      retries,
		MinIntervalMS:   minInterval,
		ProxyURL:        proxyURL,
		Quality:         domain.Quality(quality),
		Verbosity:       verb,
		FFmpegPath:      ffmpegPath,
		LogFilePath:     logFile,
		Container:       cont,
		ForceRedownload: force,
		SeasonSel:       seasonSel,
		EpisodeSel:      episodeSel,
		DryRun:          dryRun,
	}

	// Apply defaults and validate.
	kinopub.ApplyDefaults(&cfg)
	if err := kinopub.ValidateConfig(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Check ffmpeg availability (Req 7.3).
	if _, err := exec.LookPath(cfg.FFmpegPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", domain.ErrFFmpegNotFound.Error())
		return 1
	}

	// Set up signal-driven context for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Wire up services.
	deps, cleanup, err := buildDependencies(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	defer cleanup()

	// Create app and run.
	app, err := kinopub.New(deps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	_, runErr := app.Run(ctx, cfg)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", runErr)
		return 1
	}

	return 0
}

// buildDependencies constructs all real service implementations and returns
// the Dependencies struct, a cleanup function, and any error.
func buildDependencies(cfg domain.RunConfig) (kinopub.Dependencies, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// Coordinator for TTY line-discipline.
	coord := logx.NewCoordinator(os.Stderr)

	// Build logger handlers.
	handlers := buildLogHandlers(cfg, coord)

	// Open log file if configured.
	if cfg.LogFilePath != "" {
		f, err := os.OpenFile(cfg.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
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

	// Input resolver.
	inputRes := inputresolver.New(logger)

	// Feed parser.
	feedPars := feedparser.New(proxyProv.HTTPClient(), logger)

	// Media resolver — needs a RunOutput function for ffprobe.
	mediaRes := mediaresolver.New(
		proxyProv.HTTPClient(),
		makeRunOutput(),
		logger,
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
		makeRunFunc(),
		proxyProv,
		logger,
		downloader.WithFFmpegPath(cfg.FFmpegPath),
	)

	// Scheduler.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	sched := scheduler.New(
		scheduler.Config{
			MaxConcurrency: cfg.MaxConcurrency,
			MaxRetries:     cfg.MaxRetries,
			MinIntervalMS:  cfg.MinIntervalMS,
			GracePeriod:    cfg.GracePeriod,
		},
		&realClock{},
		logger,
		rng,
	)

	// Progress reporter — choose live or log based on TTY.
	var progReporter domain.ProgressReporter
	if termx.IsTTY(os.Stderr) {
		progReporter = progress.NewLive(os.Stderr, coord)
	} else {
		progReporter = progress.NewLog(logger)
	}

	deps := kinopub.Dependencies{
		Logger:           logger,
		InputResolver:    inputRes,
		FeedParser:       feedPars,
		MediaResolver:    mediaRes,
		Scheduler:        sched,
		Downloader:       dl,
		ProxyProvider:    proxyProv,
		ProgressReporter: progReporter,
		StateStore:       stateStr,
		OutputLayout:     layout,
	}

	return deps, cleanup, nil
}

// buildLogHandlers creates the console log handler based on TTY detection and verbosity.
func buildLogHandlers(cfg domain.RunConfig, coord *logx.Coordinator) []logx.Handler {
	if termx.IsTTY(os.Stderr) {
		return []logx.Handler{logx.NewTTYHandler(os.Stderr, cfg.Verbosity, coord)}
	}
	return []logx.Handler{logx.NewPlainHandler(os.Stderr, cfg.Verbosity, coord)}
}

// parseVerbosity converts a string verbosity flag to domain.Verbosity.
func parseVerbosity(s string) (domain.Verbosity, error) {
	switch s {
	case "quiet":
		return domain.VerbosityQuiet, nil
	case "normal", "":
		return domain.VerbosityNormal, nil
	case "verbose":
		return domain.VerbosityVerbose, nil
	default:
		return 0, fmt.Errorf("%w: verbosity must be quiet, normal, or verbose, got %q", domain.ErrInvalidFlag, s)
	}
}

// parseContainer converts a string container flag to domain.Container.
func parseContainer(s string) (domain.Container, error) {
	switch s {
	case "mkv", "":
		return domain.ContainerMKV, nil
	case "mp4":
		return domain.ContainerMP4, nil
	default:
		return 0, fmt.Errorf("%w: container must be mkv or mp4, got %q", domain.ErrInvalidFlag, s)
	}
}

// realClock implements domain.Clock using the real system clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// makeRunOutput creates a RunOutputFunc that executes a command and captures stdout.
func makeRunOutput() mediaresolver.RunOutputFunc {
	return func(ctx context.Context, name string, args, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		return cmd.Output()
	}
}

// makeRunFunc creates a downloader.RunFunc that executes a command, streaming
// stdout to the provided writer.
func makeRunFunc() downloader.RunFunc {
	return func(ctx context.Context, name string, args, env []string, stdout io.Writer) error {
		cmd := exec.CommandContext(ctx, name, args...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		cmd.Stdout = stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
}
