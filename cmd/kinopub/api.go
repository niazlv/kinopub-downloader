// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/androidroot"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
	"github.com/niazlv/kinopub-downloader/internal/services/apiclient"
	"github.com/niazlv/kinopub-downloader/internal/services/apiscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// newAppIntrospector builds the reader for the installed kino.pub app. The
// reader uses root only if the process already holds it — it never invokes
// su/sudo — so on an unprivileged process the app-read paths simply fail and
// the caller falls back to a stored/explicit token.
func newAppIntrospector() *kinopubapp.App {
	return kinopubapp.New(androidroot.New(androidroot.OSExec), nil)
}

// prepareAppSession resolves everything --app needs into cfg for a download
// run: the access token, the app-matching User-Agent, and the API base URL. It
// prefers, in order, an explicit flag, the session saved by `login --app`, then
// reading the installed app (root only). It validates the token so an expired
// one is reported up front. Returns a process exit code: 0 to continue.
func prepareAppSession(ctx context.Context, cfg *domain.RunConfig) int {
	app := newAppIntrospector()

	// Fill any gaps from a saved `login --app` session so a normal run needs no
	// root and no flags. Explicit flags on cfg always take precedence.
	if cfg.AppToken == "" || cfg.UserAgent == "" || cfg.APIBase == "" {
		if stored, err := credstore.Load(); err == nil && stored.HasAppToken() {
			if cfg.AppToken == "" {
				cfg.AppToken = stored.AppToken
			}
			if cfg.UserAgent == "" {
				cfg.UserAgent = stored.AppUserAgent
			}
			if cfg.APIBase == "" {
				cfg.APIBase = stored.APIBase
			}
		}
	}

	// User-Agent: explicit or stored wins; otherwise the app's fingerprint,
	// prompting if it drifts from the baseline.
	if cfg.UserAgent == "" {
		cfg.UserAgent = appUserAgent(app.Fingerprint(ctx))
	}

	// Token: explicit or stored wins; otherwise read from the installed app.
	if cfg.AppToken == "" {
		tok, err := app.ReadToken(ctx)
		if err != nil {
			reportTokenUnavailable(app.RootAvailable(ctx))
			return 1
		}
		cfg.AppToken = tok.Access
	}

	if cfg.APIBase == "" {
		cfg.APIBase = kinopubapp.DefaultAPIBase
	}

	if _, code := validateAppToken(ctx, cfg.ProxyURL, cfg.APIBase, cfg.AppToken, cfg.UserAgent); code != 0 {
		return code
	}
	return 0
}

// loginApp implements `login --app`: it obtains the app session (from an
// explicit token or by reading the installed app under root), validates it, and
// saves it encrypted so later runs need neither root nor flags. An existing
// website (cookie) login is preserved. Returns a process exit code.
func loginApp(ctx context.Context, explicitToken, explicitUA, apiBase, proxyURL string) int {
	app := newAppIntrospector()

	ua := explicitUA
	if ua == "" {
		ua = appUserAgent(app.Fingerprint(ctx))
	}

	token := explicitToken
	if token == "" {
		tok, err := app.ReadToken(ctx)
		if err != nil {
			reportTokenUnavailable(app.RootAvailable(ctx))
			return 1
		}
		token = tok.Access
	}

	if apiBase == "" {
		apiBase = kinopubapp.DefaultAPIBase
	}

	user, code := validateAppToken(ctx, proxyURL, apiBase, token, ua)
	if code != 0 {
		return code
	}

	// Preserve any existing website (cookie) login; only add/replace the app
	// half.
	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.AppToken = token
	creds.AppUserAgent = ua
	creds.APIBase = apiBase
	creds.AppSavedAt = time.Now()
	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s kino.pub app session for %s saved (encrypted, machine-bound) to ~/.config/kinopub/credentials.enc\n",
		errStyle.Green("✓"), errStyle.Cyan(user.Username))
	fmt.Fprintf(os.Stderr, "  Now run without root or flags, e.g.:\n    %s --app https://kino.pub/item/view/126715\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  The token is not refreshed automatically; if it expires, open the app and run %s login --app again.\n", os.Args[0])
	return 0
}

// appUserAgent returns the User-Agent to send, handling drift from the
// baseline: it warns, and on a TTY asks whether to trust the app-extracted
// values or fall back to the baseline. Off a TTY it keeps the extracted values
// (the installed app is authoritative) and only warns.
func appUserAgent(fp kinopubapp.Fingerprint) string {
	drift := fp.DriftFromBaseline()
	if len(drift) == 0 {
		return fp.UserAgent
	}

	warnf("the installed kino.pub app differs from this build's baseline:")
	for _, d := range drift {
		fmt.Fprintf(os.Stderr, "    %s: app=%q  baseline=%q\n", d.Field, d.Extracted, d.Baseline)
	}
	fmt.Fprintf(os.Stderr, "    → app User-Agent:      %q\n", fp.UserAgent)
	fmt.Fprintf(os.Stderr, "    → baseline User-Agent: %q\n", fp.BaselineUserAgent())

	useExtracted := true
	if termx.IsTTY(os.Stdin) && termx.IsTTY(os.Stderr) {
		useExtracted = promptYesNo("Use the values extracted from the installed app?", true)
	} else {
		notef("keeping the app-extracted values (non-interactive); pass --user-agent to override")
	}
	if useExtracted {
		return fp.UserAgent
	}
	return fp.BaselineUserAgent()
}

// validateAppToken confirms the token is accepted, mapping an expired token to
// a refresh instruction. It returns the authenticated user and an exit code
// (0 on success).
func validateAppToken(ctx context.Context, proxyURL, apiBase, token, ua string) (apiclient.User, int) {
	pp, err := proxyprovider.New(proxyURL)
	if err != nil {
		errorf("%v", err)
		return apiclient.User{}, 1
	}
	client := apiclient.New(pp.HTTPClient(), apiBase, token, apiclient.WithUserAgent(ua))
	user, err := client.User(ctx)
	switch {
	case errors.Is(err, domain.ErrAPIUnauthorized):
		errorf("the kino.pub app token was rejected (expired). Open the kino.pub app " +
			"to refresh its session, then run `login --app` again (or re-run with a " +
			"fresh --app-token).")
		return apiclient.User{}, 1
	case err != nil:
		errorf("could not validate the kino.pub app token: %v", err)
		return apiclient.User{}, 1
	}
	if user.Subscription.Active {
		notef("kino.pub app session: authorized as %q (subscription active, %.0f days left)",
			user.Username, user.Subscription.Days)
	} else {
		notef("kino.pub app session: authorized as %q (no active subscription)", user.Username)
	}
	return user, 0
}

// reportTokenUnavailable prints guidance tailored to why the token could not be
// read. It never suggests the tool elevate itself; instead it tells the user
// how to re-run under root, or to pass a token directly.
func reportTokenUnavailable(rootAvailable bool) {
	if rootAvailable {
		// Root is held but the store was unreadable — the app is likely not
		// installed or has never been signed in.
		errorf("could not read the token from the kino.pub app: it may not be " +
			"installed, or has never been signed in. Sign in with the app once, or " +
			"pass --app-token to supply a token directly.")
		return
	}
	errorf("no kino.pub app session, and the tool is not running as root so it " +
		"cannot read the app's token (it never elevates itself). Do one of:")
	fmt.Fprintf(os.Stderr, "    • save a session once, then run normally:\n")
	fmt.Fprintf(os.Stderr, "        Android/Termux:  su -c '%s login --app'\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "        Linux/desktop:   sudo %s login --app\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "    • or pass a token directly:   %s --app --app-token <TOKEN> …\n", os.Args[0])
}

// promptYesNo asks a yes/no question on stderr and reads a line from stdin.
// An empty answer returns def. Any read error also returns def.
func promptYesNo(question string, def bool) bool {
	suffix := " [Y/n] "
	if !def {
		suffix = " [y/N] "
	}
	fmt.Fprint(os.Stderr, question+suffix)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	case "n", "no":
		return false
	default:
		return def
	}
}

// chooseSavedAppSession decides whether a run that named no method on the
// command line should use the stored app session, and returns the reason to
// show the user.
//
// It answers yes when the app session is the only one stored, or when it is the
// more recent of the two — so whichever credentials the user last logged in
// with (or last downloaded with) are the ones a bare `kinopub <url>` uses,
// rather than always trying the website and failing on a stale cookie. An
// explicit --cookie/--browser-cookies always wins, and the input must be an
// item link the API backend can resolve, so podcast-feed runs keep their own
// accurate error.
func chooseSavedAppSession(inputURL string, explicitCookie bool) (use bool, reason string) {
	if explicitCookie {
		return false, ""
	}
	if _, err := apiscraper.ParseItemID(inputURL); err != nil {
		return false, ""
	}
	stored, err := credstore.Load()
	if err != nil || !stored.HasAppToken() {
		return false, ""
	}
	switch {
	case !stored.HasCookie():
		return true, "no website cookies stored"
	case stored.PreferredMethod() == credstore.MethodApp:
		return true, "the more recent login"
	default:
		return false, ""
	}
}

// savedAppSessionApplies reports whether a saved app session could authorize
// this input at all. It backs the late fallback taken once the website path has
// produced no usable cookie.
func savedAppSessionApplies(inputURL string) bool {
	if _, err := apiscraper.ParseItemID(inputURL); err != nil {
		return false
	}
	stored, err := credstore.Load()
	return err == nil && stored.HasAppToken()
}

// preferredStoredMethod reports which stored credentials a run should use when
// the user named none — the most recently saved or last successful of the two.
// It returns "" when nothing is stored or the store cannot be read.
func preferredStoredMethod() string {
	stored, err := credstore.Load()
	if err != nil {
		return ""
	}
	return stored.PreferredMethod()
}

// recordAuthMethodUsed remembers which credentials just authorized a run, so a
// later run with no flags reaches for the same ones. It writes only when the
// recorded method actually changes, keeping an ordinary run free of a needless
// re-encrypt, and stays silent on failure: this is a convenience, not a result.
func recordAuthMethodUsed(method string) {
	stored, err := credstore.Load()
	if err != nil || stored.LastUsed == method {
		return
	}
	stored.LastUsed = method
	stored.LastUsedAt = time.Now()
	_ = credstore.Save(stored)
}
