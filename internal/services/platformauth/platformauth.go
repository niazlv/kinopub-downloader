// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package platformauth — вход устройства на платформе, сайте поверх этой
// утилиты, по её контракту (api/openapi/device-auth.md в репозитории
// платформы): та же схема «введите код на другом экране», что и у kino.pub
// в kinopubauth, только без OAuth-клиента и секрета — платформа знает эту
// утилиту как единственного клиента.
//
// Выданная сессия — своя у утилиты: живёт без браузерных куки и продлевается
// refresh-токеном, пока ею пользуются. Пакет ничего не знает о терминале:
// возвращает ссылку и код, а показывает их вызывающий.
package platformauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

const (
	// SessionCookie — под этим именем платформа ждёт токен сессии, как от
	// браузера. Утилита хранит его как строку Cookie для сайта.
	SessionCookie = "kino_session"

	devicePath  = "/api/v1/auth/device"
	tokenPath   = "/api/v1/auth/device/token"
	refreshPath = "/api/v1/auth/refresh"
	mePath      = "/api/v1/me"

	maxBody             = 1 << 20
	defaultPollInterval = 5 * time.Second
	minPollInterval     = time.Second
	slowDownIncrement   = 5 * time.Second
	requestTimeout      = 30 * time.Second
)

// errSlowDown — платформа просит опрашивать реже. Внутренний: PollDevice
// обрабатывает его сам.
var errSlowDown = errors.New("slow_down")

// DeviceCode — ожидающий вход: что показать человеку и чем опрашивать.
type DeviceCode struct {
	// Code — секрет устройства, им опрашивают. Не показывается.
	Code string
	// UserCode — короткий код для человека, например "KJ7Q-3MTR".
	UserCode string
	// VerificationURL — страница подтверждения с уже подставленным кодом;
	// её и класть в QR.
	VerificationURL string
	Interval        time.Duration
	ExpiresAt       time.Time
}

// Session — выданная платформой пара.
type Session struct {
	Token            string
	RefreshToken     string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// CookieHeader — как сессия предъявляется платформе.
func (s Session) CookieHeader() string { return SessionCookie + "=" + s.Token }

// Account — чья сессия, для подтверждения человеку.
type Account struct {
	Login       string
	DisplayName string
}

// Client ходит на платформу.
type Client struct {
	http      *http.Client
	origin    string
	userAgent string
	logger    domain.Logger
	now       func() time.Time

	minInterval time.Duration
	slowDown    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithUserAgent задаёт User-Agent; по умолчанию утилита представляется собой.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithLogger включает отладочный лог.
func WithLogger(l domain.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l.Component("platformauth")
		}
	}
}

// New строит клиента к платформе по её origin ("https://kino.example").
func New(httpClient *http.Client, origin string, opts ...Option) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	c := &Client{
		http:        httpClient,
		origin:      strings.TrimRight(strings.TrimSpace(origin), "/"),
		userAgent:   "kinopub-downloader",
		now:         time.Now,
		minInterval: minPollInterval,
		slowDown:    slowDownIncrement,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// StartDevice просит у платформы код для этого устройства. device — как
// оно представится человеку и в админке платформы.
func (c *Client) StartDevice(ctx context.Context, device string) (DeviceCode, error) {
	var r struct {
		DeviceCode      string    `json:"deviceCode"`
		UserCode        string    `json:"userCode"`
		VerificationURL string    `json:"verificationUrl"`
		ExpiresAt       time.Time `json:"expiresAt"`
		Interval        int       `json:"interval"`
	}
	if err := c.call(ctx, http.MethodPost, devicePath, "", map[string]string{"device": device}, &r); err != nil {
		return DeviceCode{}, err
	}
	if r.DeviceCode == "" || r.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("the platform returned no device code")
	}
	interval := time.Duration(r.Interval) * time.Second
	if interval < c.minInterval {
		interval = defaultPollInterval
	}
	expires := r.ExpiresAt
	if expires.IsZero() {
		expires = c.now().Add(10 * time.Minute)
	}
	c.debug("device code issued", domain.F("user_code", r.UserCode), domain.F("interval", interval.String()))
	return DeviceCode{
		Code: r.DeviceCode, UserCode: r.UserCode, VerificationURL: r.VerificationURL,
		Interval: interval, ExpiresAt: expires,
	}, nil
}

// PollOnce — одна попытка обменять код на сессию. Пока человек не ответил,
// возвращает domain.ErrDeviceAuthPending: это ожидаемый ответ, а не ошибка.
func (c *Client) PollOnce(ctx context.Context, deviceCode string) (Session, error) {
	var r sessionResponse
	err := c.call(ctx, http.MethodPost, tokenPath, "", map[string]string{"deviceCode": deviceCode}, &r)
	if err != nil {
		var pe *platformError
		if errors.As(err, &pe) && pe.status == http.StatusBadRequest {
			switch pe.code {
			case "authorization_pending":
				return Session{}, domain.ErrDeviceAuthPending
			case "slow_down":
				return Session{}, errSlowDown
			case "access_denied":
				return Session{}, domain.ErrDeviceAuthDenied
			case "expired_token":
				return Session{}, domain.ErrDeviceAuthExpired
			case "invalid_grant":
				return Session{}, fmt.Errorf("%w: the platform does not know this device code", domain.ErrDeviceAuthExpired)
			}
		}
		return Session{}, err
	}
	return r.session()
}

// PollDevice ждёт, пока человек ответит, код истечёт или ctx закончится.
// Интервал платформы соблюдается, на slow_down — увеличивается.
func (c *Client) PollDevice(ctx context.Context, dc DeviceCode) (Session, error) {
	interval := dc.Interval
	if interval < c.minInterval {
		interval = defaultPollInterval
	}
	for {
		if !dc.ExpiresAt.IsZero() && c.now().After(dc.ExpiresAt) {
			return Session{}, domain.ErrDeviceAuthExpired
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Session{}, ctx.Err()
		case <-timer.C:
		}
		sess, err := c.PollOnce(ctx, dc.Code)
		switch {
		case err == nil:
			return sess, nil
		case errors.Is(err, domain.ErrDeviceAuthPending):
		case errors.Is(err, errSlowDown):
			interval += c.slowDown
			c.debug("the platform asked to slow down", domain.F("interval", interval.String()))
		default:
			return Session{}, err
		}
	}
}

// Refresh обменивает refresh-токен на новую пару. Платформа ротирует:
// прежняя сессия отзывается, повтор старого токена — отказ.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	if refreshToken == "" {
		return Session{}, domain.ErrPlatformRefreshRejected
	}
	var r sessionResponse
	err := c.call(ctx, http.MethodPost, refreshPath, "", map[string]string{"refreshToken": refreshToken}, &r)
	if err != nil {
		var pe *platformError
		if errors.As(err, &pe) {
			switch pe.status {
			case http.StatusUnauthorized, http.StatusForbidden, http.StatusGone:
				return Session{}, fmt.Errorf("%w (%s)", domain.ErrPlatformRefreshRejected, pe.Error())
			}
		}
		return Session{}, err
	}
	return r.session()
}

// Me — чья это сессия. Заодно подтверждает, что платформа её принимает.
func (c *Client) Me(ctx context.Context, token string) (Account, error) {
	var r struct {
		Login       string `json:"login"`
		DisplayName string `json:"displayName"`
	}
	err := c.call(ctx, http.MethodGet, mePath, SessionCookie+"="+token, nil, &r)
	if err != nil {
		var pe *platformError
		if errors.As(err, &pe) && (pe.status == http.StatusUnauthorized || pe.status == http.StatusForbidden) {
			return Account{}, fmt.Errorf("%w (%s)", domain.ErrPlatformSessionRequired, pe.Error())
		}
		return Account{}, err
	}
	return Account{Login: r.Login, DisplayName: r.DisplayName}, nil
}

type sessionResponse struct {
	Token            string    `json:"token"`
	RefreshToken     string    `json:"refreshToken"`
	ExpiresAt        time.Time `json:"expiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
}

func (r sessionResponse) session() (Session, error) {
	if r.Token == "" {
		return Session{}, fmt.Errorf("the platform returned no session token")
	}
	return Session{
		Token: r.Token, RefreshToken: r.RefreshToken,
		ExpiresAt: r.ExpiresAt, RefreshExpiresAt: r.RefreshExpiresAt,
	}, nil
}

// platformError — ответ платформы с кодом ошибки в теле.
type platformError struct {
	status  int
	code    string
	message string
}

func (e *platformError) Error() string {
	if e.code == "" {
		return fmt.Sprintf("the platform answered HTTP %d", e.status)
	}
	if e.message != "" && e.message != e.code {
		return fmt.Sprintf("the platform answered %d %s: %s", e.status, e.code, e.message)
	}
	return fmt.Sprintf("the platform answered %d %s", e.status, e.code)
}

// call делает один запрос. Успех — 2xx с JSON в out; иначе platformError.
func (c *Client) call(ctx context.Context, method, path, cookie string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.origin, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		pe := &platformError{status: resp.StatusCode}
		var eb struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			pe.code, pe.message = eb.Error, eb.Message
		}
		return pe
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s%s: response is not the platform API: %w", c.origin, path, err)
		}
	}
	return nil
}

func (c *Client) debug(msg string, fields ...domain.Field) {
	if c.logger != nil {
		c.logger.Debug(msg, fields...)
	}
}
