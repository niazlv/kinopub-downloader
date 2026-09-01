// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubapp

// Source describes how much of a Fingerprint was recovered from the device
// versus filled from the compiled-in baseline.
type Source int

const (
	// SourceBaseline means nothing was read from the device; every value is the
	// baseline. Typical on an unrooted host or when the app is not installed.
	SourceBaseline Source = iota
	// SourceMixed means some values were extracted and others fell back to the
	// baseline (e.g. OS release read but the APK could not be scanned).
	SourceMixed
	// SourceExtracted means the full build fingerprint was read from the
	// installed APK.
	SourceExtracted
)

// String renders the Source for logs.
func (s Source) String() string {
	switch s {
	case SourceExtracted:
		return "extracted"
	case SourceMixed:
		return "mixed"
	default:
		return "baseline"
	}
}

// Fingerprint is the app's request identity: the User-Agent to send and,
// optionally, the OAuth client credentials needed to refresh a token.
type Fingerprint struct {
	Package       string
	VersionName   string
	OSRelease     string
	AppName       string
	ExoVersion    string
	OkHTTPVersion string
	ClientID      string
	ClientSecret  string // sensitive: redact before logging (see Redacted)
	UserAgent     string
	APIBase       string
	Source        Source

	// ext holds only the values actually recovered from the device/APK, so
	// drift can be measured against the baseline. Empty means "not extracted".
	ext extracted
}

type extracted struct {
	versionName string
	appName     string
	exoVersion  string
	okhttp      string
	clientID    string
	// clientSecret was extracted iff non-empty; it is never compared to a
	// baseline (none is stored) and never logged.
	clientSecret string
}

// applyDEX overlays APK-derived facts onto the fingerprint, recording each as
// extracted for drift measurement.
func (f *Fingerprint) applyDEX(d dexFacts) {
	if d.AppName != "" {
		f.AppName, f.ext.appName = d.AppName, d.AppName
	}
	if d.ExoVersion != "" {
		f.ExoVersion, f.ext.exoVersion = d.ExoVersion, d.ExoVersion
	}
	if d.OkHTTPVersion != "" {
		f.OkHTTPVersion, f.ext.okhttp = d.OkHTTPVersion, d.OkHTTPVersion
	}
	if d.ClientID != "" {
		f.ClientID, f.ext.clientID = d.ClientID, d.ClientID
	}
	if d.ClientSecret != "" {
		f.ClientSecret, f.ext.clientSecret = d.ClientSecret, d.ClientSecret
	}
}

// finalize assembles the User-Agent and classifies the Source. Call once after
// all extraction attempts.
func (f *Fingerprint) finalize() {
	f.UserAgent = buildUserAgent(f.AppName, f.VersionName, f.OSRelease, f.ExoVersion)
	f.Source = f.classify()
}

// classify derives the Source from which build facts were extracted. The APK
// facts (appName, ExoPlayer/OkHttp versions, client id) are the meaningful
// signal; OS release is available without root and does not count toward it.
func (f Fingerprint) classify() Source {
	got, total := 0, 4
	if f.ext.appName != "" {
		got++
	}
	if f.ext.exoVersion != "" {
		got++
	}
	if f.ext.okhttp != "" {
		got++
	}
	if f.ext.clientID != "" {
		got++
	}
	switch {
	case got == total:
		return SourceExtracted
	case got == 0:
		return SourceBaseline
	default:
		return SourceMixed
	}
}

// Drift is a single field whose extracted value disagrees with the baseline.
type Drift struct {
	Field     string
	Extracted string
	Baseline  string
}

// DriftFromBaseline reports the build facts that were extracted from the device
// yet differ from the compiled-in baseline — the signal that the app was
// updated since the baseline was captured. Fields that were not extracted are
// not reported (there is nothing to compare). The client secret is never
// compared: no baseline is stored for it.
func (f Fingerprint) DriftFromBaseline() []Drift {
	var d []Drift
	add := func(field, got, base string) {
		if got != "" && got != base {
			d = append(d, Drift{Field: field, Extracted: got, Baseline: base})
		}
	}
	add("versionName", f.ext.versionName, BaselineVersionName)
	add("appName", f.ext.appName, BaselineAppName)
	add("exoPlayerVersion", f.ext.exoVersion, BaselineExoVersion)
	add("okhttpVersion", f.ext.okhttp, BaselineOkHTTPVersion)
	add("clientId", f.ext.clientID, BaselineClientID)
	return d
}

// BaselineUserAgent is the User-Agent built purely from the compiled-in
// baseline, keeping only the device's real OS release. Callers offer it as the
// alternative to the extracted User-Agent when the two disagree.
func (f Fingerprint) BaselineUserAgent() string {
	return buildUserAgent(BaselineAppName, BaselineVersionName, f.OSRelease, BaselineExoVersion)
}

// HasClientSecret reports whether an OAuth client secret was recovered (needed
// only for self-refresh, which the default flow avoids).
func (f Fingerprint) HasClientSecret() bool { return f.ClientSecret != "" }

// Redacted returns a copy safe to log: the client secret is masked to its
// length so its presence is visible without disclosing the value.
func (f Fingerprint) Redacted() Fingerprint {
	if f.ClientSecret != "" {
		f.ClientSecret = mask(f.ClientSecret)
	}
	f.ext.clientSecret = ""
	return f
}

// mask replaces a secret with a fixed-width redaction that still conveys that
// something was present.
func mask(s string) string {
	return "***redacted:" + itoa(len(s)) + "chars***"
}

// itoa avoids pulling strconv into this file for a single small conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
