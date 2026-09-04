// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultAPIBase — базовый URL JSON API.
const DefaultAPIBase = "https://api.service-kp.com/v1"

// maxBody ограничивает читаемое тело ответа, страхуя от сбойного эндпоинта.
const maxBody = 16 << 20

// Client разговаривает с JSON API kino.pub.
//
// Клиент никогда не обновляет токен самостоятельно: отвергнутый токен
// поднимается как ErrUnauthorized. Обновление требует client secret, и его
// место — в пакете session, а не в HTTP-клиенте.
type Client struct {
	http      *http.Client
	base      string
	token     string
	userAgent string
	logger    Logger
}

type Option func(*Client)

// WithUserAgent задаёт User-Agent. Пустой оставляет дефолтный от Go — это
// работает, но выделяется среди запросов приложения.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

func WithLogger(l Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// New собирает клиент. httpClient обязателен и не должен быть nil —
// через него проходит прокси и таймауты вызывающей стороны.
func New(httpClient *http.Client, base, token string, opts ...Option) *Client {
	if base == "" {
		base = DefaultAPIBase
	}
	c := &Client{
		http:   httpClient,
		base:   strings.TrimRight(base, "/"),
		token:  token,
		logger: nopLogger{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Token отдаёт текущий токен — нужно слою сессии, чтобы понять, изменился ли
// он после обновления. Значение чувствительное: не логировать.
func (c *Client) Token() string { return c.token }

// Base отдаёт базовый URL, с которым клиент был собран.
func (c *Client) Base() string { return c.base }

// get выполняет авторизованный GET и декодирует JSON в out.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.base + "/" + strings.TrimLeft(path, "/")
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return ErrUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return &APIError{Path: path, Status: resp.StatusCode, Body: truncate(string(body), 256)}
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

type userResponse struct {
	Status int  `json:"status"`
	User   User `json:"user"`
}

// User возвращает аккаунт. Заодно проверяет токен: успех означает, что токен
// принят, ErrUnauthorized — что нет.
func (c *Client) User(ctx context.Context) (User, error) {
	var r userResponse
	if err := c.get(ctx, "/user", nil, &r); err != nil {
		return User{}, err
	}
	return r.User, nil
}

type itemResponse struct {
	Status int  `json:"status"`
	Item   Item `json:"item"`
}

// Item возвращает фильм или сериал по числовому id.
func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	var r itemResponse
	if err := c.get(ctx, "/items/"+url.PathEscape(strings.TrimSpace(id)), nil, &r); err != nil {
		return Item{}, err
	}
	if r.Item.ID == 0 {
		return Item{}, fmt.Errorf("id %s: %w", id, ErrNotFound)
	}
	return r.Item, nil
}
