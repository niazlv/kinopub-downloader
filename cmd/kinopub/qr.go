// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/lib/qrterm"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubapp"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubauth"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// loginQR implements `login --qr`: kino.pub's device-authorization flow.
//
// It is the honest sibling of `login --app`. Rather than borrowing the phone
// app's session, it asks kino.pub to authorize *this* tool, which is why it
// works on a desktop with no Android and no root — and why the resulting
// session is the only kind this tool ever refreshes on its own.
func loginQR(ctx context.Context, apiBase, proxyURL, explicitSecret, explicitUA string) int {
	if apiBase == "" {
		apiBase = kinopubapp.DefaultAPIBase
	}

	clientID, clientSecret := resolveClientCredentials(ctx, explicitSecret)
	if clientSecret == "" {
		reportClientSecretUnavailable()
		return 1
	}

	pp, err := proxyprovider.New(proxyURL)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	ua := explicitUA
	if ua == "" {
		ua = kinopubauth.DefaultUserAgent
	}
	auth := kinopubauth.New(pp.HTTPClient(), apiBase, clientID, clientSecret,
		kinopubauth.WithUserAgent(ua))

	dc, err := auth.StartDevice(ctx)
	if err != nil {
		errorf("could not start device authorization: %v", err)
		return 1
	}

	presentDeviceCode(dc)

	tok, err := auth.PollDevice(ctx, dc)
	if err != nil {
		reportDeviceAuthFailure(err)
		return 1
	}

	// Confirm the session works before storing it, and show whose it is.
	user, code := validateAppToken(ctx, proxyURL, apiBase, tok.Access, ua)
	if code != 0 {
		return code
	}

	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.AppToken = tok.Access
	creds.AppRefreshToken = tok.Refresh
	creds.AppTokenExpiresAt = tok.ExpiresAt
	creds.AppUserAgent = ua
	creds.APIBase = apiBase
	creds.AppSavedAt = time.Now()
	// Ours, so it may be refreshed — the one case where that is safe.
	creds.AppTokenSource = credstore.SourceDevice
	creds.AppClientID = clientID
	creds.AppClientSecret = clientSecret
	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "\n%s kino.pub session for %s saved (encrypted, machine-bound).\n",
		errStyle.Green("✓"), errStyle.Cyan(user.Username))
	if tok.Refresh != "" {
		fmt.Fprintf(os.Stderr, "  This session belongs to this tool, so it renews itself automatically —\n"+
			"  no re-login when the access token expires.\n")
	}
	fmt.Fprintf(os.Stderr, "  Run without flags, e.g.:\n    %s --app https://kino.pub/item/view/126715\n", os.Args[0])
	return 0
}

// presentDeviceCode shows the user everything needed to approve the request:
// a scannable QR code, the link behind it, and the short code to type.
func presentDeviceCode(dc kinopubauth.DeviceCode) {
	scanURI := dc.ScanURI()

	fmt.Fprintf(os.Stderr, "\nOpen this page and enter the code to authorize kinopub:\n\n")
	if scanURI != "" {
		if err := qrterm.Render(os.Stderr, scanURI, errStyle.Enabled()); err != nil {
			// A QR is a convenience; the link and code below are the real
			// instructions, so a rendering failure must not stop the flow.
			warnf("could not render the QR code: %v", err)
		}
		fmt.Fprintln(os.Stderr)
	}
	if dc.VerificationURI != "" {
		fmt.Fprintf(os.Stderr, "  Link: %s\n", errStyle.Cyan(dc.VerificationURI))
	}
	fmt.Fprintf(os.Stderr, "  Code: %s\n", errStyle.Green(dc.UserCode))
	if !dc.ExpiresAt.IsZero() {
		fmt.Fprintf(os.Stderr, "  Valid for %s.\n", time.Until(dc.ExpiresAt).Round(time.Second))
	}
	fmt.Fprintf(os.Stderr, "\nWaiting for approval… (Ctrl+C to cancel)\n")
}

// resolveClientCredentials finds the OAuth client id and secret for the device
// flow, in order of decreasing directness: an explicit flag, the credential
// store, then the installed APK.
//
// The store matters most on a desktop, which has no APK to read: the secret
// travels there via `sessions export`, which is what makes the device flow
// usable off Android at all.
func resolveClientCredentials(ctx context.Context, explicitSecret string) (clientID, clientSecret string) {
	clientID = kinopubapp.BaselineClientID

	if explicitSecret != "" {
		return clientID, explicitSecret
	}

	if stored, err := credstore.Load(); err == nil && stored.HasClientCredentials() {
		return stored.AppClientID, stored.AppClientSecret
	}

	// Last resort: recover them from the installed app, which only works on a
	// rooted Android device.
	fp := newAppIntrospector().Fingerprint(ctx)
	if fp.ClientID != "" {
		clientID = fp.ClientID
	}
	if fp.HasClientSecret() {
		return clientID, fp.ClientSecret
	}
	return clientID, ""
}

// reportClientSecretUnavailable explains the one prerequisite the device flow
// cannot supply for itself.
func reportClientSecretUnavailable() {
	errorf("the OAuth client secret needed for device authorization is not available.")
	fmt.Fprintf(os.Stderr, "  kino.pub publishes no public OAuth client, so the flow needs the app's\n"+
		"  client secret. Get it once, then this machine keeps it. Either:\n")
	fmt.Fprintf(os.Stderr, "    • on a rooted Android device with the app installed, run:\n"+
		"        su -c '%s login --app'      (stores the secret alongside the session)\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "      then move it here with `%s sessions export` / `sessions import`\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "    • or pass it directly:  %s login --qr --client-secret <SECRET>\n", os.Args[0])
}

// reportDeviceAuthFailure turns the flow's outcomes into advice.
func reportDeviceAuthFailure(err error) {
	switch {
	case errors.Is(err, domain.ErrDeviceAuthExpired):
		errorf("the authorization code expired before it was approved. Run `%s login --qr` again.", os.Args[0])
	case errors.Is(err, domain.ErrDeviceAuthDenied):
		errorf("the authorization request was denied.")
	case errors.Is(err, context.Canceled):
		errorf("authorization cancelled.")
	default:
		errorf("device authorization failed: %v", err)
	}
}
