package kinopub

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kinopub_downloader/internal/domain"
)

// ValidateConfig validates all config fields and returns ErrInvalidFlag with a
// descriptive message for out-of-range or invalid values.
func ValidateConfig(cfg *domain.RunConfig) error {
	if cfg.MaxConcurrency < 1 || cfg.MaxConcurrency > 16 {
		return fmt.Errorf("%w: max concurrency must be in [1,16], got %d", domain.ErrInvalidFlag, cfg.MaxConcurrency)
	}

	if cfg.MaxRetries < 0 {
		return fmt.Errorf("%w: max retries must be >= 0, got %d", domain.ErrInvalidFlag, cfg.MaxRetries)
	}

	if cfg.MinIntervalMS < 0 || cfg.MinIntervalMS > 60000 {
		return fmt.Errorf("%w: min interval must be in [0,60000] ms, got %d", domain.ErrInvalidFlag, cfg.MinIntervalMS)
	}

	switch cfg.Verbosity {
	case domain.VerbosityQuiet, domain.VerbosityNormal, domain.VerbosityVerbose:
		// valid
	default:
		return fmt.Errorf("%w: verbosity must be quiet, normal, or verbose, got %d", domain.ErrInvalidFlag, cfg.Verbosity)
	}

	if cfg.ProxyURL != "" {
		if err := validateProxyURL(cfg.ProxyURL); err != nil {
			return err
		}
	}

	switch cfg.Container {
	case domain.ContainerMKV, domain.ContainerMP4:
		// valid
	default:
		return fmt.Errorf("%w: container must be mkv or mp4, got %d", domain.ErrInvalidFlag, cfg.Container)
	}

	return nil
}

// validateProxyURL checks that a proxy URL has a valid scheme and host.
func validateProxyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: proxy URL is malformed: %v", domain.ErrInvalidFlag, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		// valid scheme
	default:
		return fmt.Errorf("%w: proxy URL scheme must be http, https, or socks5, got %q", domain.ErrInvalidFlag, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: proxy URL must have a host", domain.ErrInvalidFlag)
	}
	return nil
}

// ApplyDefaults fills in default values for unset fields in the config.
func ApplyDefaults(cfg *domain.RunConfig) {
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 2
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}
	if cfg.Verbosity == 0 {
		cfg.Verbosity = domain.VerbosityNormal
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.Container == 0 {
		cfg.Container = domain.ContainerMKV
	}
	if cfg.GracePeriod == 0 {
		cfg.GracePeriod = 30 * time.Second
	}
	if !cfg.SeasonSel.All && len(cfg.SeasonSel.Values) == 0 && len(cfg.SeasonSel.Ranges) == 0 {
		cfg.SeasonSel = domain.Selection{All: true}
	}
	if !cfg.EpisodeSel.All && len(cfg.EpisodeSel.Values) == 0 && len(cfg.EpisodeSel.Ranges) == 0 {
		cfg.EpisodeSel = domain.Selection{All: true}
	}
}

// ParseSelection parses a selection string like "1,3-5,8" into a Selection.
// An empty string returns Selection{All: true}.
// Supports single numbers ("1,3,5"), ranges ("1-5"), and mixed ("1,3-5,8").
func ParseSelection(s string) (domain.Selection, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return domain.Selection{All: true}, nil
	}

	sel := domain.Selection{
		Values: make(map[int]bool),
	}

	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return domain.Selection{}, fmt.Errorf("%w: empty element in selection %q", domain.ErrInvalidFlag, s)
		}

		if idx := strings.Index(part, "-"); idx >= 0 {
			loStr := strings.TrimSpace(part[:idx])
			hiStr := strings.TrimSpace(part[idx+1:])

			lo, err := strconv.Atoi(loStr)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid range start %q in selection %q", domain.ErrInvalidFlag, loStr, s)
			}
			hi, err := strconv.Atoi(hiStr)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid range end %q in selection %q", domain.ErrInvalidFlag, hiStr, s)
			}
			if lo > hi {
				return domain.Selection{}, fmt.Errorf("%w: range start %d > end %d in selection %q", domain.ErrInvalidFlag, lo, hi, s)
			}
			sel.Ranges = append(sel.Ranges, domain.SelectionRange{Lo: lo, Hi: hi})
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return domain.Selection{}, fmt.Errorf("%w: invalid number %q in selection %q", domain.ErrInvalidFlag, part, s)
			}
			sel.Values[n] = true
		}
	}

	return sel, nil
}
