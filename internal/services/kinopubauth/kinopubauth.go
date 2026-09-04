// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package kinopubauth implements kino.pub's OAuth2 device-authorization flow —
// the "enter this code on another screen" grant behind `login --qr`.
//
// This is the honest counterpart to importing the installed app's session: the
// tool asks kino.pub for its own authorization, so the account sees a device
// that is actually this tool, on any platform, with no root and no Android.
// Because the session belongs to us, it is also the only one this tool ever
// refreshes — see the note on refreshing below.
//
// One caveat worth stating plainly: kino.pub issues no public OAuth client, so
// the flow is driven with the Android client's id and secret (the secret is
// recovered from the installed APK, never committed). Only the credentials are
// borrowed — the device registered, the User-Agent sent, and the slot consumed
// are all this tool's own.
//
// The package deliberately knows nothing about terminals or printing: it
// returns the verification URI and user code and lets the caller present them,
// so a CLI can draw a QR code and a server can render a web page.
package kinopubauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

const (
	// devicePath is the authorization endpoint, which lives at the service root
	// rather than under the versioned API prefix.
	devicePath = "/oauth2/device"

	// maxBody caps how much of a response is read, so a hostile or broken
	// endpoint cannot exhaust memory.
	maxBody = 1 << 20

	// defaultPollInterval is used when the server does not state one.
	defaultPollInterval = 5 * time.Second

	// minPollInterval floors the server's interval so a misconfigured or
	// hostile value cannot turn polling into a request flood.
	minPollInterval = time.Second

	// slowDownIncrement is added to the interval when the server answers
	// "slow_down", as the device-flow spec prescribes.
	slowDownIncrement = 5 * time.Second

	// requestTimeout bounds a single HTTP exchange.
	requestTimeout = 30 * time.Second
)

// DeviceCode is the pending authorization: what to show the user, and the
// opaque code used to poll for completion.
type DeviceCode struct {
	// Code is the device code polled with; it is a secret and is never shown.
	Code string
	// UserCode is the short human-typed code, e.g. "ABCD-1234".
	UserCode string
	// VerificationURI is where the user approves the request.
	VerificationURI string
	// VerificationURIComplete, when the server provides it, is the same page
	// with the user code already embedded. It is the better thing to put in a
	// QR code, since scanning it skips typing the code by hand.
	VerificationURIComplete string
	// Interval is how often to poll.
	Interval time.Duration
	// ExpiresAt is when the user code stops being accepted.
	ExpiresAt time.Time
}

// Token is an issued session.
type Token struct {
	Access  string
	Refresh string
	// ExpiresAt is when the access token stops being valid. Zero when the
	// server did not say.
	ExpiresAt time.Time
}

// Client talks to the authorization endpoint.
type Client struct {
	http         *http.Client
	oauthBase    string
	clientID     string
	clientSecret string
	userAgent    string
	logger       domain.Logger
	now          func() time.Time

	// Pacing floors, overridable in tests. They exist so a server-supplied
	// (or caller-supplied) interval cannot turn polling into a request flood.
	minInterval time.Duration
	slowDown    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent sets the User-Agent. The default identifies this tool honestly;
// pass another only when a caller has a specific reason.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithLogger attaches a logger for debug tracing. Secrets are never logged.
func WithLogger(l domain.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l.Component("kinopubauth")
		}
	}
}

// DefaultUserAgent identifies the tool rather than imitating the mobile app —
// the point of this flow is that the account sees what is really connecting.
const DefaultUserAgent = "kinopub-downloader (+https://github.com/niazlv/kinopub-downloader)"

// New builds a Client. apiBase is the JSON API base (with or without the
// version suffix); the authorization endpoint is derived from its origin.
func New(httpClient *http.Client, apiBase, clientID, clientSecret string, opts ...Option) *Client {
	c := &Client{
		http:         httpClient,
		oauthBase:    OAuthBaseFromAPIBase(apiBase),
		clientID:     clientID,
		clientSecret: clientSecret,
		userAgent:    DefaultUserAgent,
		now:          time.Now,
		minInterval:  minPollInterval,
		slowDown:     slowDownIncrement,
	}
	for _, o := range opts {
		o(c)
	}
	if c.http == nil {
		c.http = http.DefaultClient
	}
	return c
}

// OAuthBaseFromAPIBase derives the authorization origin from the JSON API base.
// The API is served under a version prefix ("…/v1") while OAuth sits at the
// root, so the prefix is trimmed rather than assumed absent.
func OAuthBaseFromAPIBase(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	// Not a parseable URL: fall back to trimming a trailing version segment so
	// a caller passing a bare host still gets something usable.
	if i := strings.LastIndex(base, "/v"); i > 0 {
		return base[:i]
	}
	return base
}

// StartDevice asks for a new device code to show the user.
func (c *Client) StartDevice(ctx context.Context) (DeviceCode, error) {
	if c.clientSecret == "" {
		return DeviceCode{}, domain.ErrClientSecretUnavailable
	}

	var r deviceCodeResponse
	if err := c.post(ctx, url.Values{
		"grant_type":    {"device_code"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}, &r); err != nil {
		return DeviceCode{}, err
	}
	if r.Code == "" || r.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("authorization endpoint returned no device code")
	}

	interval := time.Duration(r.Interval) * time.Second
	if interval < c.minInterval {
		interval = defaultPollInterval
	}
	expires := c.now().Add(time.Duration(r.ExpiresIn) * time.Second)
	if r.ExpiresIn <= 0 {
		expires = c.now().Add(10 * time.Minute)
	}

	c.debug("device code issued",
		domain.F("user_code", r.UserCode),
		domain.F("verification_uri", r.VerificationURI),
		domain.F("interval", interval.String()),
	)
	return DeviceCode{
		Code:                    r.Code,
		UserCode:                r.UserCode,
		VerificationURI:         r.VerificationURI,
		VerificationURIComplete: r.VerificationURIComplete,
		Interval:                interval,
		ExpiresAt:               expires,
	}, nil
}

// PollOnce makes a single attempt to exchange the device code for a token.
//
// It returns domain.ErrDeviceAuthPending while the user has not approved yet,
// which is the expected answer for most of the flow — callers drive their own
// loop with it, which is what lets a server poll without blocking a goroutine.
func (c *Client) PollOnce(ctx context.Context, deviceCode string) (Token, error) {
	var r tokenResponse
	err := c.post(ctx, url.Values{
		"grant_type":    {"device_token"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {deviceCode},
	}, &r)
	if err != nil {
		return Token{}, err
	}
	return c.tokenFrom(r)
}

// PollDevice blocks until the user approves, the code expires, or ctx ends.
// It honours the server's interval and backs off on "slow_down".
func (c *Client) PollDevice(ctx context.Context, dc DeviceCode) (Token, error) {
	interval := dc.Interval
	if interval < c.minInterval {
		interval = defaultPollInterval
	}

	for {
		// Stop before a request that cannot possibly succeed any more.
		if !dc.ExpiresAt.IsZero() && c.now().After(dc.ExpiresAt) {
			return Token{}, domain.ErrDeviceAuthExpired
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Token{}, ctx.Err()
		case <-timer.C:
		}

		tok, err := c.PollOnce(ctx, dc.Code)
		switch {
		case err == nil:
			return tok, nil
		case errors.Is(err, domain.ErrDeviceAuthPending):
			// Keep waiting.
		case errors.Is(err, errSlowDown):
			interval += c.slowDown
			c.debug("server asked to slow down", domain.F("interval", interval.String()))
		default:
			return Token{}, err
		}
	}
}

// Refresh exchanges a refresh token for a new session.
//
// Callers must only reach this for a session this tool obtained itself. A
// session imported from the installed mobile app must never be refreshed here:
// the rotation would invalidate the phone app's own login. The credential store
// records which is which, and the CLI enforces it.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if refreshToken == "" {
		return Token{}, domain.ErrTokenRefreshRejected
	}
	if c.clientSecret == "" {
		return Token{}, domain.ErrClientSecretUnavailable
	}

	var r tokenResponse
	err := c.post(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"refresh_token": {refreshToken},
	}, &r)
	if err != nil {
		// Any definite answer other than "pending" means this refresh token is
		// no longer usable; report it as such so callers re-authorize.
		if errors.Is(err, domain.ErrDeviceAuthPending) || errors.Is(err, errSlowDown) {
			return Token{}, domain.ErrTokenRefreshRejected
		}
		return Token{}, err
	}
	return c.tokenFrom(r)
}

// tokenFrom converts a decoded response into a Token, rejecting empty ones.
func (c *Client) tokenFrom(r tokenResponse) (Token, error) {
	if r.AccessToken == "" {
		return Token{}, fmt.Errorf("authorization endpoint returned no access token")
	}
	var expires time.Time
	if r.ExpiresIn > 0 {
		expires = c.now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return Token{Access: r.AccessToken, Refresh: r.RefreshToken, ExpiresAt: expires}, nil
}

// errSlowDown is internal: the spec's "poll less often" signal.
var errSlowDown = errors.New("slow_down")

// post sends a form-encoded request and decodes the JSON reply, mapping the
// documented OAuth error codes onto domain errors.
func (c *Client) post(ctx context.Context, form url.Values, out any) error {
	if c.oauthBase == "" {
		return fmt.Errorf("no authorization endpoint configured")
	}

	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.oauthBase + devicePath
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("read authorization response: %w", err)
	}

	// The endpoint reports flow state through an "error" field, and does so with
	// a non-2xx status, so the body is inspected before the status is judged.
	var e errorResponse
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return mapOAuthError(e.Error)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("authorization endpoint returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode authorization response: %w", err)
	}
	return nil
}

// mapOAuthError translates the flow's error codes into domain errors.
func mapOAuthError(code string) error {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "authorization_pending":
		return domain.ErrDeviceAuthPending
	case "slow_down":
		return errSlowDown
	case "expired_token", "code_expired":
		return domain.ErrDeviceAuthExpired
	case "access_denied":
		return domain.ErrDeviceAuthDenied
	case "invalid_grant", "invalid_token":
		return domain.ErrTokenRefreshRejected
	default:
		return fmt.Errorf("authorization failed: %s", code)
	}
}

func (c *Client) debug(msg string, fields ...domain.Field) {
	if c.logger != nil {
		c.logger.Debug(msg, fields...)
	}
}

// Wire types for the endpoint's JSON.
type deviceCodeResponse struct {
	Code                    string `json:"code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// ScanURI is the address to put in a QR code: the pre-filled page when the
// server offers one, otherwise the plain verification page.
func (d DeviceCode) ScanURI() string {
	if d.VerificationURIComplete != "" {
		return d.VerificationURIComplete
	}
	return d.VerificationURI
}
