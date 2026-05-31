package domain

import "time"

// Quality represents a video quality preference (e.g., "1080p").
// An empty string means auto/highest.
type Quality string

// Verbosity controls the minimum log level displayed on interactive output.
type Verbosity int

const (
	VerbosityQuiet   Verbosity = iota // show only warn/error
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
	InputURL        string
	OutputPath      string        // "" → cwd (Req 11.1)
	MaxConcurrency  int           // [1,16], default 2 (Req 4.1, 4.2)
	MaxRetries      int           // default 5 (Req 5.6)
	MinIntervalMS   int           // [0,60000] (Req 4.5)
	ProxyURL        string        // explicit proxy; "" → system/direct
	Quality         Quality
	Verbosity       Verbosity     // default Normal (Req 14.1)
	FFmpegPath      string        // default "ffmpeg" on PATH (Req 7.3)
	LogFilePath     string        // "" → no file sink (Req 13.7)
	Container       Container
	ForceRedownload bool          // (Req 12.4)
	SeasonSel       Selection     // (Req 15.5)
	EpisodeSel      Selection     // (Req 15.5)
	DryRun          bool          // (Req 15.6)
	GracePeriod     time.Duration // default 30s (Req 4.7)
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
