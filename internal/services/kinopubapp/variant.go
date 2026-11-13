// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubapp

import (
	"context"
	"sort"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// The service has shipped under two Android clients, and a device may have
// either or both installed and signed in:
//
//   - com.kinopub — the original "KinoPub" client. Its baseline is the set of
//     Baseline* constants in parse.go/fingerprint.go, kept there so external
//     callers that reference them keep working.
//   - watch.kino  — the current "Kino.Watch" client, the service's own rename.
//     It stores its OAuth session in the same login.xml shape and drives the
//     same device/OAuth flow (client_id "android"); only the package, the
//     default API host, and the build fingerprint differ.
//
// Both sessions authorize the same backend — the two API hosts below are
// interchangeable mirrors — so a session read from either app works. A run
// therefore reads whichever apps are installed and uses the freshest one the
// API still accepts; see ReadTokens.
const (
	kinoWatchPackage       = "watch.kino"
	kinoWatchLabel         = "Kino.Watch"
	kinoWatchAppName       = "Android kino.watch"
	kinoWatchVersionName   = "2.25"
	kinoWatchExoVersion    = "2.10.8"
	kinoWatchOkHTTPVersion = "4.12.0"
	kinoWatchClientID      = "android"

	// kinoWatchAPIBase is the Kino.Watch client's preferred JSON API host (the
	// one its api_mirror.xml records as last good). It is a mirror of the
	// kino.pub host and accepts the same tokens.
	kinoWatchAPIBase = "https://api.srvkp.com/v1"

	// kinoPubLabel names the original client in user-facing text.
	kinoPubLabel = "kino.pub"
)

// Baseline is the non-secret build fingerprint of one client release: the facts
// used to shape the User-Agent and to detect, by drift, that the installed app
// has moved past the compiled-in snapshot. The OAuth client secret is never a
// baseline — it is only ever read from the installed APK at runtime.
type Baseline struct {
	AppName       string
	VersionName   string
	ExoVersion    string
	OkHTTPVersion string
	ClientID      string
}

// Variant is one official client of the service: its Android package, a label
// for messages, the JSON API host it talks to by default, and its build
// baseline. Everything the introspection needs that differs between clients
// lives here, so App is written once and parameterized by a Variant.
type Variant struct {
	Package        string
	Label          string
	DefaultAPIBase string
	Baseline       Baseline
}

var (
	// VariantKinoPub is the original com.kinopub client. Its baseline is the
	// package-level Baseline* constants, so this stays in step with them.
	VariantKinoPub = Variant{
		Package:        Package,
		Label:          kinoPubLabel,
		DefaultAPIBase: DefaultAPIBase,
		Baseline: Baseline{
			AppName:       BaselineAppName,
			VersionName:   BaselineVersionName,
			ExoVersion:    BaselineExoVersion,
			OkHTTPVersion: BaselineOkHTTPVersion,
			ClientID:      BaselineClientID,
		},
	}

	// VariantKinoWatch is the current watch.kino ("Kino.Watch") client.
	VariantKinoWatch = Variant{
		Package:        kinoWatchPackage,
		Label:          kinoWatchLabel,
		DefaultAPIBase: kinoWatchAPIBase,
		Baseline: Baseline{
			AppName:       kinoWatchAppName,
			VersionName:   kinoWatchVersionName,
			ExoVersion:    kinoWatchExoVersion,
			OkHTTPVersion: kinoWatchOkHTTPVersion,
			ClientID:      kinoWatchClientID,
		},
	}

	// Variants lists every known client in preference order, used as the
	// tie-break when two installed apps carry equally fresh sessions. kino.pub
	// comes first for continuity with the tool's history; freshness (ReadTokens)
	// overrides this order whenever the sessions differ.
	Variants = []Variant{VariantKinoPub, VariantKinoWatch}
)

// VariantForPackage returns the known Variant for an Android package id, or
// false when the package is not one the tool recognizes. An empty package id
// resolves to the kino.pub variant, so a credential store written before the
// package was recorded reads as the original client.
func VariantForPackage(pkg string) (Variant, bool) {
	if pkg == "" {
		return VariantKinoPub, true
	}
	for _, v := range Variants {
		if v.Package == pkg {
			return v, true
		}
	}
	return Variant{}, false
}

// TokenCandidate is an OAuth session read from one installed client, tagged
// with the Variant it came from so the caller knows which API host and build
// fingerprint it pairs with.
type TokenCandidate struct {
	Variant Variant
	Token   Token
}

// ReadTokens reads the stored session from every known client installed on the
// device and returns them newest-first — "newest" being the token with the
// later stored expiry, which is the one most recently issued or refreshed and
// so the most likely to still be valid.
//
// Clients that are absent or signed out are skipped (their ReadToken fails), so
// an empty result means no app session could be read at all. The ordering is
// only a preference: because an app's stored expiry is advisory, the caller
// still validates candidates against the API and falls back to an older one
// that the freshest-looking token is rejected. Ties (equal expiry, or none
// recorded) keep Variants order.
func ReadTokens(ctx context.Context, su Reader, logger domain.Logger) []TokenCandidate {
	var out []TokenCandidate
	for _, v := range Variants {
		tok, err := NewForVariant(su, v, logger).ReadToken(ctx)
		if err != nil {
			continue
		}
		out = append(out, TokenCandidate{Variant: v, Token: tok})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Token.ExpiresAtMs > out[j].Token.ExpiresAtMs
	})
	return out
}
