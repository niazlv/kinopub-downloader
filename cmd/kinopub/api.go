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
	"github.com/niazlv/kinopub-downloader/internal/services/apiscraper"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

// newAppIntrospector builds the reader for the installed kino.pub app. The
// reader uses root only if the process already holds it — it never invokes
// su/sudo — so on an unprivileged process the app-read paths simply fail and
// the caller falls back to a stored/explicit token.
func newAppIntrospector() *kinopubapp.App {
	return kinopubapp.New(androidroot.New(androidroot.OSExec), nil)
}

// deviceAppSession is a session read from one installed client and confirmed to
// work against the API: the token plus everything a run needs to send it (the
// client's User-Agent and API host) and the build fingerprint `login --app`
// saves alongside it.
type deviceAppSession struct {
	Variant     kinopubapp.Variant
	Token       kinopubapp.Token
	Fingerprint kinopubapp.Fingerprint
	UserAgent   string
	APIBase     string
	User        kinopub.User
}

// pickDeviceAppSession reads the session from every installed client — kino.pub
// and Kino.Watch — and returns the freshest one the API actually accepts. The
// candidates come newest-first (by stored expiry), but because that expiry is
// only advisory each is validated in turn, so a newer token the server rejects
// gives way to an older one that still works. forceUA and forceBase, when set
// (an explicit flag), override the chosen client's own User-Agent and API host.
//
// It returns a process exit code: 0 with a usable session, non-zero after it has
// already reported why none could be used (no app readable, or every session
// rejected). It performs no download-side side effects beyond the network
// validation and any drift prompt for the session it selects.
func pickDeviceAppSession(ctx context.Context, proxyURL, forceUA, forceBase string) (deviceAppSession, int) {
	su := androidroot.New(androidroot.OSExec)

	cands := kinopubapp.ReadTokens(ctx, su, nil)
	if len(cands) == 0 {
		reportTokenUnavailable(kinopubapp.New(su, nil).RootAvailable(ctx))
		return deviceAppSession{}, 1
	}

	var sawUnauthorized bool
	var lastErr error
	for _, c := range cands {
		fp := kinopubapp.NewForVariant(su, c.Variant, nil).Fingerprint(ctx)

		// Validate with the extracted User-Agent; the interactive drift prompt
		// is resolved only for the session actually chosen, below, so a loop
		// over several installed apps does not ask more than once.
		ua := forceUA
		if ua == "" {
			ua = fp.UserAgent
		}
		base := forceBase
		if base == "" {
			base = c.Variant.DefaultAPIBase
		}

		user, err := checkAppToken(ctx, proxyURL, base, c.Token.Access, ua)
		switch {
		case err == nil:
			finalUA := forceUA
			if finalUA == "" {
				finalUA = appUserAgent(fp)
			}
			if len(cands) > 1 {
				notef("using the %s app session (freshest of %d installed that the API accepts)",
					c.Variant.Label, len(cands))
			}
			return deviceAppSession{
				Variant:     c.Variant,
				Token:       c.Token,
				Fingerprint: fp,
				UserAgent:   finalUA,
				APIBase:     base,
				User:        user,
			}, 0
		case errors.Is(err, domain.ErrAPIUnauthorized):
			sawUnauthorized = true
			if len(cands) > 1 {
				notef("the %s app session was rejected (expired); trying another installed app…", c.Variant.Label)
			}
		default:
			lastErr = err
		}
	}

	// Every candidate failed. A rejection is the common, actionable case; a
	// transport error is reported as-is.
	if !sawUnauthorized && lastErr != nil {
		errorf("could not validate the app token: %v", lastErr)
		return deviceAppSession{}, 1
	}
	reportAppTokenExpired(cands[0].Variant)
	return deviceAppSession{}, 1
}

// prepareAppSession resolves everything --app needs into cfg for a download
// run: the access token, the app-matching User-Agent, and the API base URL. It
// prefers, in order, an explicit flag, the session saved by `login --app`, then
// reading the installed app (root only). It validates the token so an expired
// one is reported up front. Returns a process exit code: 0 to continue.
func prepareAppSession(ctx context.Context, cfg *domain.RunConfig) int {
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

	// No token from a flag or a saved session: read the installed apps and use
	// the freshest session the API still accepts — from either client, whichever
	// is newer and working.
	if cfg.AppToken == "" {
		sel, code := pickDeviceAppSession(ctx, cfg.ProxyURL, cfg.UserAgent, cfg.APIBase)
		if code != 0 {
			return code
		}
		cfg.AppToken = sel.Token.Access
		if cfg.UserAgent == "" {
			cfg.UserAgent = sel.UserAgent
		}
		if cfg.APIBase == "" {
			cfg.APIBase = sel.APIBase
		}
		announceAppSession(sel.Variant.Label, sel.User)
		return 0
	}

	// A token from a flag or a saved session: default its User-Agent and API
	// base, then renew (if ours) and validate.
	if cfg.UserAgent == "" {
		cfg.UserAgent = appUserAgent(newAppIntrospector().Fingerprint(ctx))
	}

	if cfg.APIBase == "" {
		cfg.APIBase = kinopubapp.DefaultAPIBase
	}

	// Renew a session of our own before it lapses, so a long run does not lose
	// its token partway through. An imported app session is left untouched.
	ensureFreshAppToken(ctx, cfg)

	user, err := checkAppToken(ctx, cfg.ProxyURL, cfg.APIBase, cfg.AppToken, cfg.UserAgent)
	if errors.Is(err, domain.ErrAPIUnauthorized) {
		// Rejected: if this session is ours, renewing it is likely to fix
		// exactly this, so try once before telling the user anything.
		if refreshAfterRejection(ctx, cfg) {
			user, err = checkAppToken(ctx, cfg.ProxyURL, cfg.APIBase, cfg.AppToken, cfg.UserAgent)
		}
	}
	switch {
	case errors.Is(err, domain.ErrAPIUnauthorized):
		reportTokenExpiredFor(storedTokenSource())
		return 1
	case err != nil:
		errorf("could not validate the kino.pub app token: %v", err)
		return 1
	}
	announceAppSession(storedAppVariant().Label, user)
	return 0
}

// loginApp implements `login --app`: it obtains the app session (from an
// explicit token or by reading the installed app under root), validates it, and
// saves it encrypted so later runs need neither root nor flags. An existing
// website (cookie) login is preserved. Returns a process exit code.
func loginApp(ctx context.Context, explicitToken, explicitUA, apiBase, proxyURL string) int {
	// Device path: no token supplied, so read it from whichever installed client
	// — kino.pub or Kino.Watch — holds the freshest session the API accepts.
	if explicitToken == "" {
		sel, code := pickDeviceAppSession(ctx, proxyURL, explicitUA, apiBase)
		if code != 0 {
			return code
		}
		announceAppSession(sel.Variant.Label, sel.User)
		base := apiBase
		if base == "" {
			base = sel.APIBase
		}
		return saveImportedAppSession(sel.Variant, sel.Token.Access, sel.UserAgent, base, sel.Fingerprint)
	}

	// Explicit token: supplied directly, with no installed-app context, so it is
	// treated as the original kino.pub client's session.
	app := newAppIntrospector()
	fp := app.Fingerprint(ctx)
	ua := explicitUA
	if ua == "" {
		ua = appUserAgent(fp)
	}
	if apiBase == "" {
		apiBase = kinopubapp.DefaultAPIBase
	}
	if _, code := validateAppToken(ctx, proxyURL, apiBase, app.Variant().Label, explicitToken, ua); code != 0 {
		return code
	}
	return saveImportedAppSession(app.Variant(), explicitToken, ua, apiBase, fp)
}

// saveImportedAppSession persists an app session imported from an installed
// client, preserving any website (cookie) login and recording which client it
// came from. The session is marked SourceApp so it is never refreshed here —
// rotating the token would sign the phone app out. Returns a process exit code.
func saveImportedAppSession(v kinopubapp.Variant, token, ua, apiBase string, fp kinopubapp.Fingerprint) int {
	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.AppToken = token
	creds.AppUserAgent = ua
	creds.APIBase = apiBase
	creds.AppSavedAt = time.Now()
	creds.AppPackage = v.Package
	// This session belongs to the phone app, not to us: mark it so nothing ever
	// refreshes it, since rotating the token would sign the app out. The app's
	// own refresh token is deliberately not stored — there is nothing here to
	// misuse later.
	creds.AppTokenSource = credstore.SourceApp
	creds.AppRefreshToken = ""
	creds.AppTokenExpiresAt = time.Time{}
	// Keep the OAuth client credentials if the APK yielded them. They are not
	// used for this session, but they are what lets `login --qr` and automatic
	// refresh work later — including on a desktop that exports this store and
	// has no APK of its own to read.
	if fp.ClientID != "" {
		creds.AppClientID = fp.ClientID
	}
	if fp.HasClientSecret() {
		creds.AppClientSecret = fp.ClientSecret
	}
	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s %s app session saved (encrypted, machine-bound) to ~/.config/kinopub/credentials.enc\n",
		errStyle.Green("✓"), errStyle.Cyan(v.Label))
	fmt.Fprintf(os.Stderr, "  Now run without root or flags, e.g.:\n    %s --app https://kino.watch/item/view/126715\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  This is the phone app's own session, so it is never refreshed here — that\n"+
		"  would sign the app out. When it expires, open the app and run `%s login --app` again,\n"+
		"  or switch to `%s login --qr` for a session this tool owns and renews by itself.\n",
		os.Args[0], os.Args[0])
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

	warnf("the installed %s app differs from this build's baseline:", appLabel(fp.Package))
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
// a refresh instruction. label names the client the session belongs to, for the
// account line. It returns the authenticated user and an exit code (0 on success).
func validateAppToken(ctx context.Context, proxyURL, apiBase, label, token, ua string) (kinopub.User, int) {
	user, err := checkAppToken(ctx, proxyURL, apiBase, token, ua)
	switch {
	case errors.Is(err, domain.ErrAPIUnauthorized):
		reportTokenExpiredFor(storedTokenSource())
		return kinopub.User{}, 1
	case err != nil:
		errorf("could not validate the %s app token: %v", label, err)
		return kinopub.User{}, 1
	}
	announceAppSession(label, user)
	return user, 0
}

// reportTokenUnavailable prints guidance tailored to why the token could not be
// read. It never suggests the tool elevate itself; instead it tells the user
// how to re-run under root, or to pass a token directly.
func reportTokenUnavailable(rootAvailable bool) {
	if rootAvailable {
		// Root is held but the store was unreadable — the app is likely not
		// installed or has never been signed in.
		errorf("could not read a token from an installed kino.pub or Kino.Watch app: " +
			"neither may be installed, or has never been signed in. Sign in with either " +
			"app once, or pass --app-token to supply a token directly.")
		return
	}
	errorf("no kino.pub or Kino.Watch app session, and the tool is not running as root " +
		"so it cannot read the app's token (it never elevates itself). Do one of:")
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
	site := domain.SiteFromURL(inputURL)
	switch {
	case !stored.HasCookieFor(site):
		return true, "no website cookies stored for " + site.String()
	case stored.PreferredMethodFor(site) == credstore.MethodApp:
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

// checkAppToken asks the API who the token belongs to, returning the raw error
// rather than reporting it.
//
// It exists so a caller can react to a rejection — by renewing the session and
// trying again — before any message is printed. validateAppToken is the
// reporting wrapper around it.
func checkAppToken(ctx context.Context, proxyURL, apiBase, token, ua string) (kinopub.User, error) {
	pp, err := proxyprovider.New(proxyURL)
	if err != nil {
		return kinopub.User{}, err
	}
	client := kinopub.New(pp.HTTPClient(), apiBase, token, kinopub.WithUserAgent(ua))
	return client.User(ctx)
}

// appLabel names the client a stored package id belongs to, for user-facing
// text. An unrecognized or empty id reads as the original kino.pub client.
func appLabel(pkg string) string {
	if v, ok := kinopubapp.VariantForPackage(pkg); ok {
		return v.Label
	}
	return kinopubapp.VariantKinoPub.Label
}

// announceAppSession prints who the run is authorized as, naming the client the
// session belongs to (kino.pub or Kino.Watch).
func announceAppSession(label string, user kinopub.User) {
	if user.Subscription.Active {
		notef("%s app session: authorized as %q (subscription active, %.0f days left)",
			label, user.Username, user.Subscription.Days)
	} else {
		notef("%s app session: authorized as %q (no active subscription)", label, user.Username)
	}
}
