// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "time"

// Quality represents a video quality preference (e.g., "1080p").
// An empty string means auto/highest.
type Quality string

// Verbosity controls the minimum log level displayed on interactive output.
type Verbosity int

const (
	VerbosityUnset   Verbosity = iota // no explicit choice yet — ApplyDefaults fills Normal
	VerbosityQuiet                    // show only warn/error
	VerbosityNormal                   // show info/warn/error (default)
	VerbosityVerbose                  // show debug/info/warn/error
)

// ProxyMode indicates how the proxy was resolved.
type ProxyMode int

const (
	ProxyDirect   ProxyMode = iota // no proxy
	ProxySystem                    // from environment variables
	ProxyExplicit                  // explicitly configured
)

// Container selects the output mux container format.
type Container int

const (
	ContainerMKV Container = iota // default — best multi-audio/subtitle support
	ContainerMP4
)

// RunConfig holds all configuration for a single download run.
type RunConfig struct {
	InputURL string
	// FromURL, when set, is a platform download-manifest URL. The platform has
	// already resolved the source, so the run needs no site credentials: the
	// manifest carries the master playlist and the track list, and every
	// address goes through the platform's own gateway.
	FromURL         string
	OutputPath      string // "" → cwd (Req 11.1)
	MaxConcurrency  int    // [1,16], default 2 (Req 4.1, 4.2)
	ProxyURL        string // explicit proxy; "" → system/direct
	RateLimit       int64  // aggregate download cap in bytes/sec; 0 → unlimited
	Quality         Quality
	Verbosity       Verbosity // default Normal (Req 14.1)
	FFmpegPath      string    // default "ffmpeg" on PATH (Req 7.3)
	LogFilePath     string    // "" → no file sink (Req 13.7)
	Container       Container
	ForceRedownload bool      // (Req 12.4)
	SeasonSel       Selection // (Req 15.5)
	EpisodeSel      Selection // (Req 15.5)
	DryRun          bool      // (Req 15.6)

	// Site is the origin the run targets, derived from InputURL so any mirror
	// works, or set explicitly via --site when there is no URL to derive it
	// from. The zero value means DefaultSiteHost.
	Site Site

	// NoDomainRewrite keeps every URL exactly as given. Links pointing at a
	// former domain of the site (kino.pub) are normally rewritten to the
	// current one before use — the input URL, the target site, and links found
	// inside feeds. --no-domain-rewrite disables that correction, e.g. when the
	// old domain is alive again or the raw links themselves are under scrutiny.
	NoDomainRewrite bool

	// Authentication / request shaping. The site sits behind Cloudflare and may
	// return HTTP 403 for unauthenticated requests. These fields let the user
	// supply credentials captured from a logged-in browser session so the tool
	// and ffmpeg can issue requests that pass Cloudflare and the site's auth.
	Cookie         string            // raw Cookie header value applied to all requests
	UserAgent      string            // User-Agent applied to all requests (must match the cf_clearance UA)
	Headers        map[string]string // extra HTTP headers applied to all requests
	BrowserCookies string            // browser name to auto-load site cookies from ("", "safari", "chrome", "firefox", "auto")

	// FeedFile, when set, is a path to a locally saved RSS feed file. It is used
	// instead of fetching the feed over the network — useful when the feed URL
	// returns 403. The InputURL is still used to derive the SeriesID when present.
	FeedFile string

	// FFmpegExtraArgs are additional arguments passed to ffmpeg before the output
	// path. This allows advanced users to override encoding settings (e.g.
	// transcode on the fly) or add filters.
	FFmpegExtraArgs []string

	// NoNotify suppresses the system notifications a run otherwise posts —
	// Termux notifications on Android, osascript/notify-send banners on the
	// desktop. The terminal progress display is unaffected. It carries the
	// saved `notifications` preference, which --no-notify and --notify override
	// for a single run.
	NoNotify bool

	// NoChunked disables the chunked HTTP download mode. When false (default),
	// progressive MP4 sources are downloaded via HTTP Range requests with
	// resume capability. When true, all downloads go through ffmpeg directly.
	NoChunked bool

	// AudioPref selects which audio tracks to keep. The zero value keeps every
	// track. See AudioPreference for matching semantics. (audio selection)
	AudioPref AudioPreference

	// AudioMenu enables the interactive audio-track picker shown before the
	// first download. When the user makes no choice within AudioMenuTimeout,
	// all tracks are kept. The menu is only shown on a TTY.
	AudioMenu bool
	// AudioMenuTimeout bounds how long the interactive picker waits for input
	// before defaulting to "keep all". Zero means use the package default.
	AudioMenuTimeout time.Duration

	// VideoMenu enables the interactive video-quality picker shown before the
	// first download. The chosen quality replaces Quality for the whole run —
	// unlike audio and subtitles, exactly one video stream is downloaded, so
	// this is a single choice rather than a filter. TTY only.
	VideoMenu bool
	// VideoMenuTimeout bounds how long that picker waits for input before
	// keeping the automatic quality. Zero means use the package default.
	VideoMenuTimeout time.Duration

	// SubsPref selects which subtitle tracks to keep. The zero value keeps
	// every track. See SubtitlePreference for matching semantics.
	SubsPref SubtitlePreference

	// SubsMenu enables the interactive subtitle-track picker, mirroring
	// AudioMenu. The menu is only shown on a TTY.
	SubsMenu bool
	// SubsMenuTimeout bounds how long that picker waits for input before
	// defaulting to "keep all". Zero means use the package default.
	SubsMenuTimeout time.Duration

	// SubsExternal writes the selected subtitles as separate .srt files next to
	// the episode instead of muxing them into the container.
	SubsExternal bool

	// SubtitlesOnly downloads and writes only the selected subtitles, skipping
	// video and audio entirely. Selection becomes strict in this mode: an
	// episode lacking the requested subtitles is an error rather than a
	// fallback, since substituting a different language would defeat the point
	// of the run. Implies SubsExternal.
	SubtitlesOnly bool

	// AppMode runs with the session of the installed kino.pub mobile app rather
	// than a website cookie: the item is fetched from the JSON API with the
	// app's access token as a Bearer credential, and the signed hls4 manifests
	// that come back feed the normal HLS download pipeline. Reusing the app's
	// session also means no additional account device slot is claimed.
	// See ErrAPIUnauthorized.
	AppMode bool

	// AppToken is the app's access token, used as the Bearer credential in
	// AppMode. When empty the CLI falls back to the token saved by
	// `login --app`, and failing that reads it from the installed app (which
	// needs the process to already be root). The tool never refreshes it: a
	// rejected token is reported so the user can renew it by opening the app.
	AppToken string

	// APIBase overrides the JSON API base URL (default
	// "https://api.service-kp.com/v1"). The service rotates domains, so this is
	// configurable without a rebuild.
	APIBase string

	// AppCodec selects which codec family's manifest AppMode hands to the
	// download pipeline when an item offers more than one (e.g. h264 and h265).
	// Empty means the backend default (h264, for broad compatibility).
	AppCodec string
}

// RequestAuth carries credentials and request-shaping headers applied to every
// outbound HTTP request (and propagated to ffmpeg). It exists so the tool can
// reuse a logged-in browser session to pass Cloudflare and the site's auth.
type RequestAuth struct {
	Cookie    string            // raw Cookie header value
	UserAgent string            // User-Agent (must match the cf_clearance UA)
	Headers   map[string]string // extra headers

	// Site scopes the Cookie: it is only ever sent to hosts the site owns, and
	// never to the CDN, which throttles requests carrying it. The zero value
	// leaves the site unknown, in which case callers fall back to
	// AnyKnownSiteOwns.
	Site Site
}

// IsZero reports whether the auth carries no information.
func (a RequestAuth) IsZero() bool {
	return a.Cookie == "" && a.UserAgent == "" && len(a.Headers) == 0
}

// HasCredentials reports whether the auth actually authenticates a request,
// i.e. carries a Cookie. UserAgent and Headers (e.g. a default Referer) are
// always populated by the CLI, so IsZero is effectively never true there and
// cannot be used to gate cookie-only features like page scraping.
func (a RequestAuth) HasCredentials() bool {
	return a.Cookie != ""
}

// Selection is a parsed set/range expression over season or episode numbers.
type Selection struct {
	All    bool
	Values map[int]bool
	Ranges []SelectionRange
}

// SelectionRange represents a contiguous inclusive range [Lo, Hi].
type SelectionRange struct {
	Lo, Hi int
}

// Matches returns true if n is included in the selection.
// An empty selection (All=false, no Values, no Ranges) matches nothing.
// When All is true, every n matches.
func (s Selection) Matches(n int) bool {
	if s.All {
		return true
	}
	if s.Values[n] {
		return true
	}
	for _, r := range s.Ranges {
		if n >= r.Lo && n <= r.Hi {
			return true
		}
	}
	return false
}
