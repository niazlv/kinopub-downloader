// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package kinopubapp introspects the official Android client of the service
// installed on the same device as this CLI. The service ships two such clients
// — the original com.kinopub ("KinoPub") and the current watch.kino
// ("Kino.Watch") — and a device may have either or both; each is described by a
// Variant and read the same way. ReadTokens reads every installed client and
// orders their sessions newest-first so a caller can use the freshest one.
//
// It reads the app's stored OAuth session so a run can reuse the already
// authorized device (and its account device-slot) instead of registering a new
// one, and it recovers the app's build fingerprint — the exact User-Agent and,
// where needed, OAuth client credentials — so the tool's requests are
// indistinguishable from the app's.
//
// Everything here is best-effort and adaptive. With root it reads the app's
// private storage and scans its APK. Without root — or when the app is absent —
// it degrades: the session token must then be supplied by the caller, and the
// fingerprint falls back to the compiled-in baseline (still an app-shaped
// User-Agent). No method requires root to return a usable result.
package kinopubapp

import (
	"context"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// Reader is the on-device capability surface this package needs. It is
// satisfied by *androidsu.Runner and faked in tests.
type Reader interface {
	Available(ctx context.Context) bool
	ReadAppFile(ctx context.Context, pkg, relPath string) ([]byte, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Getprop(ctx context.Context, key string) (string, error)
	PackageAPKPath(ctx context.Context, pkg string) (string, error)
	PackageDump(ctx context.Context, pkg string) ([]byte, error)
}

// App introspects one installed client of the service through a Reader. Which
// client it reads — com.kinopub or watch.kino — is fixed at construction by its
// Variant.
type App struct {
	su      Reader
	variant Variant
	logger  domain.Logger
}

// New builds an App for the original kino.pub client over the given Reader. A
// nil Reader is tolerated: every method then behaves as if root were
// unavailable (token reads fail, the fingerprint is the baseline). Use
// NewForVariant to introspect another client (e.g. Kino.Watch).
func New(su Reader, logger domain.Logger) *App {
	return NewForVariant(su, VariantKinoPub, logger)
}

// NewForVariant builds an App that reads the given client Variant. It is how a
// caller introspects a specific installed app; ReadTokens uses it to read each
// known client in turn.
func NewForVariant(su Reader, variant Variant, logger domain.Logger) *App {
	if logger != nil {
		logger = logger.Component("kinopubapp")
	}
	return &App{su: su, variant: variant, logger: logger}
}

// Variant reports which client this App introspects.
func (a *App) Variant() Variant { return a.variant }

// RootAvailable reports whether the app's private storage and APK can be read,
// i.e. whether the automatic token read and APK fingerprinting are possible.
func (a *App) RootAvailable(ctx context.Context) bool {
	return a.su != nil && a.su.Available(ctx)
}

// ReadToken reads the OAuth session the app persisted in its SharedPreferences.
// It requires root; without it (or without the app installed) the caller must
// obtain the token another way. A missing or tokenless store is reported as
// ErrAppTokenUnavailable so the CLI can print the same actionable message
// regardless of the underlying cause.
func (a *App) ReadToken(ctx context.Context) (Token, error) {
	if a.su == nil {
		return Token{}, domain.ErrAppTokenUnavailable
	}
	data, err := a.su.ReadAppFile(ctx, a.variant.Package, loginPrefsPath)
	if err != nil {
		a.debug("cannot read app login store", domain.F("error", err.Error()))
		return Token{}, domain.ErrAppTokenUnavailable
	}
	tok, err := parseLoginXML(data)
	if err != nil {
		a.debug("cannot parse app login store", domain.F("error", err.Error()))
		return Token{}, domain.ErrAppTokenUnavailable
	}
	return tok, nil
}

// Fingerprint recovers the app's request identity. It never fails: whatever
// cannot be read from the device is filled from the baseline, and Source
// records how much was recovered so the caller can decide whether to warn.
//
// Extraction order:
//   - OS release via getprop (no root needed) — makes the UA device-accurate
//     even on an unrooted device.
//   - versionName via "pm dump" (best effort).
//   - appName / ExoPlayer & OkHttp versions / OAuth client id+secret by
//     scanning the installed APK (root needed to read /data/app).
func (a *App) Fingerprint(ctx context.Context) Fingerprint {
	fp := Fingerprint{
		variant:       a.variant,
		Package:       a.variant.Package,
		VersionName:   a.variant.Baseline.VersionName,
		AppName:       a.variant.Baseline.AppName,
		ExoVersion:    a.variant.Baseline.ExoVersion,
		OkHTTPVersion: a.variant.Baseline.OkHTTPVersion,
		ClientID:      a.variant.Baseline.ClientID,
		APIBase:       a.variant.DefaultAPIBase,
	}

	if a.su == nil {
		fp.finalize()
		return fp
	}

	if rel, err := a.su.Getprop(ctx, "ro.build.version.release"); err == nil && rel != "" {
		fp.OSRelease = rel
	}

	if dump, err := a.su.PackageDump(ctx, a.variant.Package); err == nil {
		if v := parseVersionName(dump); v != "" {
			fp.ext.versionName = v
			fp.VersionName = v
		}
	}

	if apkPath, err := a.su.PackageAPKPath(ctx, a.variant.Package); err == nil {
		if apk, err := a.su.ReadFile(ctx, apkPath); err == nil {
			if facts, err := scanAPK(apk); err == nil {
				fp.applyDEX(facts)
			} else {
				a.debug("apk scan failed", domain.F("error", err.Error()))
			}
		} else {
			a.debug("apk read failed", domain.F("error", err.Error()))
		}
	} else {
		a.debug("apk path lookup failed", domain.F("error", err.Error()))
	}

	fp.finalize()
	return fp
}

func (a *App) debug(msg string, fields ...domain.Field) {
	if a.logger != nil {
		a.logger.Debug(msg, fields...)
	}
}
