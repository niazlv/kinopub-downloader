package domain

import (
	"context"
	"net/http"
	"time"
)

// ---------------------------------------------------------------------------
// Structured logging primitives
// ---------------------------------------------------------------------------

// Level is the severity of a log record.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Field is a structured key-value context pair (Req 13.5).
type Field struct {
	Key   string
	Value any
}

// F constructs a Field for structured logging.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

// ---------------------------------------------------------------------------
// Injectable infrastructure interfaces
// ---------------------------------------------------------------------------

// Clock abstracts time for deterministic testing of backoff, rate limiting,
// and grace periods.
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
	After(d time.Duration) <-chan time.Time
}

// Runner abstracts command execution so ffmpeg/ffprobe calls are testable
// without real binaries.
type Runner interface {
	Run(ctx context.Context, name string, args, env []string) error
}

// ---------------------------------------------------------------------------
// FeedSource — normalized feed reference
// ---------------------------------------------------------------------------

// FeedSource represents a normalized podcast feed reference with its numeric
// ID and authentication token.
type FeedSource struct {
	ID    string // numeric podcast id from the URL
	Token string // feed authentication token

	// LocalPath, when non-empty, points to a locally saved RSS feed file that
	// should be read instead of fetching the feed over the network.
	LocalPath string
}

// QualityPref is an alias for Quality used in media resolution preference.
type QualityPref = Quality

// ---------------------------------------------------------------------------
// Component interfaces (ports)
// ---------------------------------------------------------------------------

// Logger is the custom structured, leveled logging subsystem (Req 13, 14).
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)

	// With returns a child logger that attaches the given fields to every
	// subsequent record (Req 13.5).
	With(fields ...Field) Logger

	// Component returns a child logger tagged with a component name (Req 13.5).
	Component(name string) Logger
}

// InputClass distinguishes the type of user-supplied URL.
type InputClass int

const (
	ClassUnclassified InputClass = iota
	ClassPodcastFeed
	ClassPageLink
)

// InputResolver classifies and resolves user-supplied URLs into feed sources
// (Req 1).
type InputResolver interface {
	// Classify inspects a kino.pub URL (Req 1.1).
	Classify(rawURL string) (InputClass, error)

	// Resolve produces a FeedSource. For a page link it derives the tokenized
	// feed (Req 1.2, 1.3); it returns ErrFeedTokenUnavailable when the feed
	// token cannot be obtained (Req 1.6).
	Resolve(ctx context.Context, rawURL string) (FeedSource, error)
}

// FeedParser retrieves and parses an RSS feed into a Series catalog (Req 2).
type FeedParser interface {
	// Parse retrieves (within a 30s timeout) and parses the feed into a Series
	// (Req 2.1, 2.2). Entries whose season/episode cannot be determined are
	// excluded with a warn log (Req 2.8). Returns ErrEmptyFeed when zero
	// episodes parse (Req 2.6), and descriptive errors for retrieval/parse
	// failures (Req 2.5, 2.7).
	Parse(ctx context.Context, src FeedSource) (Series, error)
}

// MediaResolver enumerates tracks for an episode (Req 3).
type MediaResolver interface {
	// Resolve enumerates tracks for an episode within a 30s timeout (Req 3.8).
	// Selects the MediaSource by quality preference, else highest quality
	// (Req 3.6, 3.7). Returns ErrNoVideoTrack if no video track resolves
	// (Req 3.5).
	Resolve(ctx context.Context, ep Episode, pref QualityPref) (ResolvedMedia, error)
}

// Scheduler executes download jobs with bounded concurrency, rate limiting,
// retry and backoff, and graceful shutdown (Req 4, 5).
type Scheduler interface {
	// Run executes all jobs with bounded concurrency, rate limiting, retry
	// and backoff, and graceful shutdown on ctx cancellation (Req 4, 5).
	Run(ctx context.Context, jobs []Job, exec JobExecutor) RunSummary
}

// JobExecutor performs a single attempt of a job (the Downloader supplies this).
type JobExecutor interface {
	Execute(ctx context.Context, job Job) error
}

// Downloader runs ffmpeg for one episode (Req 7, 8, 9).
type Downloader interface {
	// Download runs ffmpeg for one episode: builds the command, streams
	// -progress to the reporter, writes a temp file, verifies size>0, then
	// atomically renames to the final path (Req 7). Sets audio/subtitle
	// metadata labels (Req 8, 9).
	Download(ctx context.Context, job Job, sink ProgressSink) error
}

// ProxyProvider resolves and configures proxy settings (Req 6).
type ProxyProvider interface {
	// HTTPClient returns an *http.Client configured with the resolved proxy
	// (explicit > system > direct) honoring NO_PROXY (Req 6.1-6.3, 6.5).
	HTTPClient() *http.Client

	// FFmpegEnv returns environment entries / args to route ffmpeg through the
	// proxy (http_proxy / -http_proxy). Returns ErrProxyUnsupportedFFmpeg
	// for socks5, which ffmpeg cannot use for HTTP (Req 6.1, 6.6).
	FFmpegEnv() ([]string, error)

	// Mode reports the active proxy mode for logging.
	Mode() ProxyMode
}

// ProgressReporter drives the live or log-based progress display (Req 10).
type ProgressReporter interface {
	// Start begins reporting for the full series plan.
	Start(plan SeriesPlan)

	// EpisodeStarted signals that an episode download has begun.
	EpisodeStarted(key EpisodeKey)

	// TrackProgress reports per-track download progress.
	TrackProgress(key EpisodeKey, track TrackRef, percent int)

	// EpisodeCompleted signals that an episode download finished successfully.
	EpisodeCompleted(key EpisodeKey)

	// EpisodeFailed signals that an episode download failed.
	EpisodeFailed(key EpisodeKey, err error)

	// Stop flushes and tears down any live display.
	Stop()
}

// StateStore persists and queries download completion state (Req 12).
type StateStore interface {
	Load(ctx context.Context, series SeriesID) (DownloadState, error)
	MarkCompleted(ctx context.Context, key EpisodeKey) error
	IsCompleted(state DownloadState, key EpisodeKey) bool
}

// OutputLayout derives filesystem paths for episode output (Req 11).
type OutputLayout interface {
	EpisodePath(root string, series Series, ep Episode) (string, error)
	EnsureDirs(path string) error
}

// DownloadEngine is the programmatic entry point usable without the CLI
// (Req 16.3, 16.4).
type DownloadEngine interface {
	Run(ctx context.Context, cfg RunConfig) (RunResult, error)
}
