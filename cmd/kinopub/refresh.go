// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubauth"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// refreshLeeway is how far ahead of expiry a session is renewed, so a long
// download does not lose its token halfway through.
const refreshLeeway = 5 * time.Minute

// storedTokenSource reports where the saved app session came from, or "" when
// there is none. It drives the advice given for an expired token, which differs
// completely between the two kinds.
func storedTokenSource() string {
	stored, err := credstore.Load()
	if err != nil || !stored.HasAppToken() {
		return ""
	}
	return stored.TokenSource()
}

// ensureFreshAppToken renews the stored session before it expires, when this
// tool is allowed to.
//
// Only a session obtained by `login --qr` qualifies: it is ours, so rotating it
// affects nothing else. A session imported with `login --app` belongs to the
// phone, and refreshing it would sign the app out — so it is left alone and its
// expiry is reported instead. Failure is not fatal here: the existing token may
// still work, and the API's answer decides that.
func ensureFreshAppToken(ctx context.Context, cfg *domain.RunConfig) {
	stored, err := credstore.Load()
	if err != nil || !stored.CanRefresh() {
		return
	}
	if !stored.AppTokenExpiringWithin(refreshLeeway) {
		return
	}
	// Only renew the session this run is actually using; an explicit
	// --app-token on the command line is the user's own and is left untouched.
	if cfg.AppToken != "" && cfg.AppToken != stored.AppToken {
		return
	}
	if _, ok := refreshStoredSession(ctx, cfg, stored, "expiring soon"); !ok {
		return
	}
}

// refreshAfterRejection renews the session after the API has rejected the
// current token, and reports whether the caller should retry with a new one.
func refreshAfterRejection(ctx context.Context, cfg *domain.RunConfig) bool {
	stored, err := credstore.Load()
	if err != nil || !stored.CanRefresh() {
		return false
	}
	if cfg.AppToken != "" && cfg.AppToken != stored.AppToken {
		return false
	}
	_, ok := refreshStoredSession(ctx, cfg, stored, "rejected by the API")
	return ok
}

// refreshStoredSession performs the refresh, persists the result and updates
// cfg to use it. reason is only for the log line.
func refreshStoredSession(ctx context.Context, cfg *domain.RunConfig, stored credstore.Credentials, reason string) (credstore.Credentials, bool) {
	clientID := stored.AppClientID
	if clientID == "" {
		clientID = kinopubapp.BaselineClientID
	}
	if stored.AppClientSecret == "" {
		// Without the client credentials the endpoint cannot be called. Say so
		// once rather than silently behaving as if refresh were unavailable.
		warnf("the session needs renewing (%s) but the stored OAuth client secret is missing; "+
			"re-run `%s login --qr` to re-authorize.", reason, os.Args[0])
		return stored, false
	}

	apiBase := cfg.APIBase
	if apiBase == "" {
		apiBase = stored.APIBase
	}
	if apiBase == "" {
		apiBase = kinopubapp.DefaultAPIBase
	}

	pp, err := proxyprovider.New(cfg.ProxyURL)
	if err != nil {
		warnf("could not renew the session: %v", err)
		return stored, false
	}

	ua := stored.AppUserAgent
	if ua == "" {
		ua = kinopubauth.DefaultUserAgent
	}
	auth := kinopubauth.New(pp.HTTPClient(), apiBase, clientID, stored.AppClientSecret,
		kinopubauth.WithUserAgent(ua))

	tok, err := auth.Refresh(ctx, stored.AppRefreshToken)
	if err != nil {
		warnf("could not renew the kino.pub session (%s): %v", reason, err)
		return stored, false
	}

	stored.AppToken = tok.Access
	if tok.Refresh != "" {
		// The endpoint may rotate the refresh token; keep the newest one or the
		// next renewal would present a spent credential.
		stored.AppRefreshToken = tok.Refresh
	}
	stored.AppTokenExpiresAt = tok.ExpiresAt
	stored.AppSavedAt = time.Now()
	if err := credstore.Save(stored); err != nil {
		// The new token is still usable for this run even if it could not be
		// persisted; warn rather than fail.
		warnf("renewed the session but could not save it: %v", err)
	}

	cfg.AppToken = tok.Access
	if cfg.APIBase == "" {
		cfg.APIBase = apiBase
	}
	notef("kino.pub session renewed automatically (%s).", reason)
	return stored, true
}

// reportTokenExpiredFor prints advice for a rejected token, which depends
// entirely on whose session it is.
func reportTokenExpiredFor(source string) {
	if source == credstore.SourceDevice {
		errorf("the kino.pub session has expired and could not be renewed automatically.")
		fmt.Fprintf(os.Stderr, "  Re-authorize this tool:  %s login --qr\n", os.Args[0])
		return
	}
	// An imported app session, or none recorded: the phone owns it.
	errorf("the kino.pub app token has expired — the API rejected it (HTTP 401).")
	fmt.Fprintf(os.Stderr, "  This session belongs to the phone app, so it is deliberately never\n"+
		"  refreshed here: rotating the token would sign the app out.\n")
	fmt.Fprintf(os.Stderr, "  Open the kino.pub app once (it refreshes its own session), then:\n"+
		"    %s login --app\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  Or switch to a session this tool owns and renews by itself:\n"+
		"    %s login --qr\n", os.Args[0])
}
