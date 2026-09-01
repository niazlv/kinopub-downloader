// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package apiclient is a thin client for the kino.pub JSON API.
//
// It authenticates with an OAuth access token as a Bearer credential and sends
// the mobile app's User-Agent so requests are indistinguishable from the app.
// Only the endpoints the downloader needs are implemented: fetching an item
// (whose response carries signed hls4 manifests) and reading the current user
// (used to validate the token before a run).
//
// The client never refreshes the token. A rejected token surfaces as
// domain.ErrAPIUnauthorized so the CLI can tell the user to refresh it via the
// app, keeping this package free of any OAuth client secret.
package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// maxBody caps how much of a response body is read, guarding against a
// misbehaving endpoint. Item payloads are a few hundred KB at most.
const maxBody = 16 << 20 // 16 MiB

// Client talks to the kino.pub JSON API.
type Client struct {
	http      *http.Client
	base      string // e.g. "https://api.service-kp.com/v1", no trailing slash
	token     string
	userAgent string
	logger    domain.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent sets the User-Agent header (the app's, to blend in). Empty
// leaves Go's default, which is fine functionally but stands out.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithLogger attaches a logger; component logs go under "apiclient".
func WithLogger(l domain.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l.Component("apiclient")
		}
	}
}

// New builds a Client. httpClient must be non-nil (pass the proxy provider's
// client). base defaults to the baseline API URL when empty.
func New(httpClient *http.Client, base, token string, opts ...Option) *Client {
	if base == "" {
		base = "https://api.service-kp.com/v1"
	}
	c := &Client{
		http:  httpClient,
		base:  strings.TrimRight(base, "/"),
		token: token,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// get issues an authenticated GET for a path (leading slash optional) and
// decodes the JSON body into out. It maps HTTP 401 to
// domain.ErrAPIUnauthorized and other non-2xx statuses to a descriptive error.
func (c *Client) get(ctx context.Context, path string, out any) error {
	url := c.base + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return domain.ErrAPIUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("API %s returned HTTP %d", path, resp.StatusCode)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

type userResponse struct {
	Status int  `json:"status"`
	User   User `json:"user"`
}

// User fetches the authenticated account. It doubles as token validation: a
// success proves the token is accepted; ErrAPIUnauthorized means it is not.
func (c *Client) User(ctx context.Context) (User, error) {
	var r userResponse
	if err := c.get(ctx, "/user", &r); err != nil {
		return User{}, err
	}
	return r.User, nil
}

type itemResponse struct {
	Status int  `json:"status"`
	Item   Item `json:"item"`
}

// Item fetches a single item (movie or serial) by numeric id.
func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	var r itemResponse
	if err := c.get(ctx, "/items/"+strings.TrimSpace(id), &r); err != nil {
		return Item{}, err
	}
	if r.Item.ID == 0 {
		return Item{}, fmt.Errorf("API returned no item for id %s", id)
	}
	return r.Item, nil
}
