// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/browsercookies"
)

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

// headerList is a repeatable string flag that collects "Name: Value" header
// entries supplied via --header.
type headerList []string

// String implements flag.Value.
func (h *headerList) String() string {
	return strings.Join(*h, ", ")
}

// Set implements flag.Value, appending each --header occurrence after checking
// it has a non-empty name (toMap would otherwise silently drop it).
func (h *headerList) Set(v string) error {
	name, _, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("%w: header must be in 'Name: Value' form, got %q", domain.ErrInvalidFlag, v)
	}
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: header name must not be empty, got %q", domain.ErrInvalidFlag, v)
	}
	*h = append(*h, v)
	return nil
}

// toMap parses the collected header entries into a map of header name to value.
func (h headerList) toMap() map[string]string {
	if len(h) == 0 {
		return nil
	}
	m := make(map[string]string, len(h))
	for _, entry := range h {
		name, value, _ := strings.Cut(entry, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if name != "" {
			m[name] = value
		}
	}
	return m
}

// browserCookiesFlag is a flag with an optional value. Used bare
// (--browser-cookies) it defaults to "auto"; with a value
// (--browser-cookies=safari) it selects a specific browser. Implementing
// IsBoolFlag lets the standard flag package accept it without a following
// argument, so a positional URL after it is not mistaken for its value.
type browserCookiesFlag struct {
	set   bool
	value string
}

// String implements flag.Value.
func (b *browserCookiesFlag) String() string { return b.value }

// Set implements flag.Value. An empty value (bare flag) means "auto".
func (b *browserCookiesFlag) Set(v string) error {
	b.set = true
	if v == "" || v == "true" {
		b.value = browsercookies.BrowserAuto
	} else {
		b.value = strings.ToLower(strings.TrimSpace(v))
	}
	return nil
}

// IsBoolFlag tells the flag package the value is optional.
func (b *browserCookiesFlag) IsBoolFlag() bool { return true }

// isKnownBrowser reports whether s names a browser supported for cookie loading.
func isKnownBrowser(s string) bool {
	switch strings.ToLower(s) {
	case browsercookies.BrowserAuto,
		browsercookies.BrowserSafari,
		browsercookies.BrowserChrome,
		browsercookies.BrowserFirefox:
		return true
	default:
		return false
	}
}

// ffmpegExtraList is a repeatable string flag that collects individual ffmpeg
// arguments supplied via --x.
type ffmpegExtraList []string

// String implements flag.Value.
func (f *ffmpegExtraList) String() string {
	return strings.Join(*f, " ")
}

// Set implements flag.Value, appending each --x occurrence.
func (f *ffmpegExtraList) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// splitShellArgs splits a string into arguments respecting simple quoting.
// It handles double-quoted and single-quoted strings, but does not support
// escape sequences within quotes (good enough for ffmpeg args).
func splitShellArgs(s string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for _, r := range s {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
		case r == '"' && !inSingle:
			inDouble = !inDouble
		case (r == ' ' || r == '\t') && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}
