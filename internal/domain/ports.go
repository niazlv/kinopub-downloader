// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

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

	// Site is the origin the feed lives on, carried over from the URL the
	// source was resolved from so mirrors keep working. The zero value means
	// DefaultSiteHost.
	Site Site

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
	// Classify inspects an input URL (Req 1.1). Any host is accepted — see Site.
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
	MarkCompleted(ctx context.Context, info CompletedInfo) error
	SetMetadata(ctx context.Context, series SeriesID, meta SeriesMetadata) error
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

// HLSDownloader downloads episodes via HLS segment-based streaming.
// It fetches the master playlist, selects quality, downloads segments,
// and concatenates them into local files.
type HLSDownloader interface {
	// DownloadEpisode downloads an episode's video and audio streams via HLS
	// segments to local files. Returns paths in HLSDownloadResult; the caller
	// must remux them and remove HLSDownloadResult.TempDir afterwards.
	//
	// Only the audio tracks selected by the preference set via
	// SetAudioPreference are downloaded.
	DownloadEpisode(ctx context.Context, manifestURL string, quality Quality,
		outPath string, key EpisodeKey, sink ProgressSink) (*HLSDownloadResult, error)

	// ListAudioTracks fetches the master playlist and reports the audio tracks
	// available for the selected quality, without downloading anything. It lets
	// the caller present an interactive picker and derive language preferences.
	ListAudioTracks(ctx context.Context, manifestURL string, quality Quality) ([]AudioTrackInfo, error)

	// SetAudioPreference sets the audio-track filter applied to subsequent
	// DownloadEpisode calls. The zero AudioPreference keeps every track.
	SetAudioPreference(pref AudioPreference)

	// ListSubtitleTracks reports the subtitle tracks available for the selected
	// quality, without downloading anything. An episode with no subtitles yields
	// an empty list rather than an error — subtitles are optional.
	ListSubtitleTracks(ctx context.Context, manifestURL string, quality Quality) ([]SubtitleTrackInfo, error)

	// SetSubtitlePreference sets the subtitle-track filter applied to subsequent
	// DownloadEpisode calls. The zero SubtitlePreference keeps every track.
	SetSubtitlePreference(pref SubtitlePreference)

	// ListVideoQualities reports the video renditions the master playlist
	// offers, best first and with duplicates of the same resolution and codec
	// collapsed, so they can be presented as a menu.
	ListVideoQualities(ctx context.Context, manifestURL string) ([]VideoQualityInfo, error)

	// ProbeTrackStats samples the audio and subtitle renditions of the selected
	// quality: each media playlist is read for its duration and the first
	// segments are measured for a bitrate, from which a size is projected. It
	// exists for listings and costs a few requests per track. The results
	// align with ListAudioTracks and ListSubtitleTracks; a track that could not
	// be sampled has zero stats rather than failing the whole probe.
	ProbeTrackStats(ctx context.Context, manifestURL string, quality Quality) (audio, subtitles []TrackStats, err error)
}

// AudioChooser presents the available audio tracks to the user and returns the
// indices to keep. Implementations may block for input up to a timeout; on
// timeout or non-interactive input they should keep all tracks (return nil).
type AudioChooser interface {
	// ChooseAudio shows tracks and returns the selected indices. A nil/empty
	// result means "keep all tracks".
	ChooseAudio(tracks []AudioTrackInfo, timeout time.Duration) ([]int, error)
}

// SubtitleChooser presents the available subtitle tracks to the user and returns
// the indices to keep. Implementations may block for input up to a timeout; on
// timeout or non-interactive input they should keep all tracks (return nil).
type SubtitleChooser interface {
	// ChooseSubtitles shows tracks and returns the selected indices. A nil/empty
	// result means "keep all tracks".
	ChooseSubtitles(tracks []SubtitleTrackInfo, timeout time.Duration) ([]int, error)
}

// SubtitleSidecarWriter writes subtitle tracks as separate files next to the
// episode instead of muxing them into the container. It backs both
// --subs-external and --subs-only.
type SubtitleSidecarWriter interface {
	// WriteSubtitleSidecars converts each track to SubRip and writes it beside
	// job.OutPath, returning the paths written. Existing files are replaced.
	WriteSubtitleSidecars(ctx context.Context, job Job, tracks []HLSSubtitleTrack) ([]string, error)
}

// HLSMuxer muxes downloaded HLS video + audio files into a final container.
type HLSMuxer interface {
	// MuxHLS combines the video file and audio tracks into job.OutPath using
	// ffmpeg (-c copy), applying labels, languages, and metadata.
	MuxHLS(ctx context.Context, job Job, hls *HLSDownloadResult) error
}

// HLSDownloadResult contains info about a completed HLS download.
type HLSDownloadResult struct {
	Resolution  string // e.g. "1920x1080"
	BitrateKbps int    // selected variant bitrate
	Codec       string // "h264" or "h265"
	TotalBytes  int64

	// VideoPath is the local path to the concatenated video .ts file.
	VideoPath string
	// AudioTracks are the local audio files downloaded separately (demuxed HLS).
	// Empty when audio is muxed into the video stream.
	AudioTracks []HLSAudioTrack
	// SubtitleTracks are the local subtitle files downloaded separately. Empty
	// when the source carries no subtitles or the preference kept none.
	SubtitleTracks []HLSSubtitleTrack
	// TempDir is the directory holding the intermediate files; the caller
	// should remove it after remuxing.
	TempDir string
}

// HLSAudioTrack describes a downloaded audio rendition.
type HLSAudioTrack struct {
	Path     string // local .ts/.aac file path
	Name     string // studio/track label, e.g. "MVO, AniLibria"
	Language string // language tag, e.g. "ru"
}

// HLSSubtitleTrack describes a downloaded subtitle rendition.
type HLSSubtitleTrack struct {
	Path     string // local .vtt file path (segments already merged)
	Name     string // track label, e.g. "Русские полные"
	Language string // language tag, e.g. "rus"
}

// PageScraper extracts playlist data from site pages.
type PageScraper interface {
	// ExtractAllSeasons fetches all seasons' playlists from a page URL.
	ExtractAllSeasons(ctx context.Context, baseURL string) (*PagePlaylist, error)
}

// PagePlaylist holds extracted playlist data from a site page.
type PagePlaylist struct {
	ItemID   int
	Title    string
	Poster   string
	Episodes []PageEpisode
	Seasons  []PageSeason
}

// PageEpisode is a single episode from the page playlist.
type PageEpisode struct {
	ManifestURL  string
	MediaID      int
	EpisodeTitle string
	Duration     int // seconds
	Season       int
	Episode      int
}

// PageSeason is season metadata from the page.
type PageSeason struct {
	Season int
	Count  int
}
