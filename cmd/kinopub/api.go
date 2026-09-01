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

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/androidroot"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
	"github.com/niazlv/kinopub-downloader/internal/services/apiclient"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// prepareAPIConfig resolves everything --api needs into cfg: the access token,
// the app-matching User-Agent, and the API base URL. It reads the installed
// mobile app only when this process is already running as root — it never
// elevates itself — warns on (and lets the user resolve) any drift from the
// built-in baseline, and validates the token against the API so an expired one
// is reported clearly up front. It returns a process exit code: 0 to continue,
// non-zero to abort.
func prepareAPIConfig(ctx context.Context, cfg *domain.RunConfig) int {
	// The on-device reader uses root only if the process already holds it; it
	// does not invoke su/sudo. When not root, the app-read paths simply fail and
	// the user is asked to supply a token or re-run under root themselves.
	runner := androidroot.New(androidroot.OSExec)
	app := kinopubapp.New(runner, nil)

	// Fingerprint is best-effort: OS release is read without root, the rest
	// from the APK when readable, otherwise the baseline. It never fails.
	fp := app.Fingerprint(ctx)

	// User-Agent: an explicit --user-agent always wins. Otherwise use the
	// app's, prompting on drift.
	if cfg.UserAgent == "" {
		cfg.UserAgent = resolveAPIUserAgent(fp)
	}

	// Access token: an explicit --api-token wins; otherwise read it from the
	// installed app (requires root/elevation).
	if cfg.APIToken == "" {
		tok, err := app.ReadToken(ctx)
		if err != nil {
			reportTokenUnavailable(app.RootAvailable(ctx))
			return 1
		}
		cfg.APIToken = tok.Access
	}

	if cfg.APIBase == "" {
		cfg.APIBase = kinopubapp.DefaultAPIBase
	}

	// Validate the token now so an expired one produces an actionable message
	// rather than a confusing mid-run failure.
	if code := validateAPIToken(ctx, cfg); code != 0 {
		return code
	}
	return 0
}

// resolveAPIUserAgent returns the User-Agent to send, handling drift from the
// baseline: it warns, and on a TTY asks whether to trust the app-extracted
// values or fall back to the baseline. Off a TTY it keeps the extracted values
// (the app is authoritative) and only warns.
func resolveAPIUserAgent(fp kinopubapp.Fingerprint) string {
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

// validateAPIToken confirms the token is accepted, mapping an expired token to
// a refresh instruction. It uses the same proxy settings the run will.
func validateAPIToken(ctx context.Context, cfg *domain.RunConfig) int {
	pp, err := proxyprovider.New(cfg.ProxyURL)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	client := apiclient.New(pp.HTTPClient(), cfg.APIBase, cfg.APIToken,
		apiclient.WithUserAgent(cfg.UserAgent))
	user, err := client.User(ctx)
	switch {
	case errors.Is(err, domain.ErrAPIUnauthorized):
		errorf("the kino.pub access token was rejected (expired). Open the kino.pub " +
			"app to refresh its session, then re-run. If reading the token from the " +
			"app, make sure the app has been opened recently.")
		return 1
	case err != nil:
		errorf("could not validate the kino.pub access token: %v", err)
		return 1
	}
	if user.Subscription.Active {
		notef("kino.pub API: authorized as %q (subscription active, %.0f days left)",
			user.Username, user.Subscription.Days)
	} else {
		notef("kino.pub API: authorized as %q (no active subscription)", user.Username)
	}
	return 0
}

// reportTokenUnavailable prints guidance tailored to why the token could not be
// read. It never suggests the tool elevate itself; instead it tells the user
// how to re-run under root, or to pass a token directly.
func reportTokenUnavailable(rootAvailable bool) {
	if rootAvailable {
		// Root is held but the store was unreadable — the app is likely not
		// installed or has never been signed in.
		errorf("could not read the access token from the kino.pub app: it may not be " +
			"installed, or has never been signed in. Sign in with the app once, or " +
			"pass --api-token to supply a token directly.")
		return
	}
	errorf("no kino.pub access token, and the tool is not running as root so it " +
		"cannot read the app's session (it will never elevate itself). Do one of:")
	fmt.Fprintf(os.Stderr, "    • pass a token directly:   %s --api-token <TOKEN> …\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "    • re-run this command as root yourself:\n")
	fmt.Fprintf(os.Stderr, "        Android/Termux:  su -c '%s'\n", currentCommand())
	fmt.Fprintf(os.Stderr, "        Linux/desktop:   sudo %s\n", currentCommand())
}

// currentCommand reconstructs the invocation for the guidance above, quoting
// arguments that contain spaces so the printed line is runnable as-is.
func currentCommand() string {
	parts := make([]string, len(os.Args))
	for i, a := range os.Args {
		if strings.ContainsAny(a, " \t") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
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
