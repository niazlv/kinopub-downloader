// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

// Package session подхватывает сохранённую авторизацию и отдаёт готовый к
// работе клиент API.
//
// Существует потому, что «взять токен» — это не одно действие: надо выбрать
// метод из сохранённых, понять, истекает ли токен, разрешено ли его обновлять,
// обновить и сохранить обратно. Разбросанное по вызывающим, это превращается
// в пять слегка разных копий одной логики.
//
// Пакет публичный, а хранилище — нет. Так и задумано: внешний потребитель
// получает готовый клиент, но не доступ к формату credentials.enc. Импорт
// internal/ отсюда законен — запрет действует только на чужие модули.
//
// Сервера здесь нет и не будет: это библиотека загрузки учётных данных,
// а не сетевая служба.
package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubauth"
	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

// Method — способ авторизации, лежащий в хранилище.
type Method string

const (
	// MethodAPI — OAuth-токен для JSON API. Единственный, пригодный для
	// программного доступа к каталогу.
	MethodAPI Method = "api"
	// MethodCookie — сессия сайта. Годится для скрейпинга страниц,
	// но JSON API её не принимает: он ждёт Bearer-токен.
	MethodCookie Method = "cookie"
	// MethodNone — в хранилище ничего пригодного нет.
	MethodNone Method = "none"
)

// Ошибки, по которым вызывающий принимает решение.
var (
	// ErrNoSession — хранилища нет или оно пусто.
	ErrNoSession = errors.New("kinopub/session: no stored credentials")

	// ErrNoAPIToken — есть только cookie-сессия. JSON API ею не пользуется,
	// поэтому программный доступ к каталогу невозможен, пока не появится
	// OAuth-токен.
	ErrNoAPIToken = errors.New("kinopub/session: stored session has no API token")
)

// refreshLead — насколько заранее обновлять токен. Краулер не должен
// узнавать о протухании из середины обхода.
const refreshLead = 10 * time.Minute

// Status описывает состояние авторизации БЕЗ секретов: его можно печатать
// в лог и показывать в админке.
type Status struct {
	Method      Method
	APIBase     string
	HasAPIToken bool
	HasCookie   bool
	// TokenSource: "device" — сессия этого инструмента, обновляется сама;
	// "app" — заимствована у установленного приложения, обновлять нельзя,
	// иначе телефон разлогинится.
	TokenSource   string
	CanRefresh    bool
	ExpiresAt     time.Time
	CookieSiteURL string
}

// Ready сообщает, возможен ли программный доступ к API.
func (s Status) Ready() bool { return s.HasAPIToken }

// Hint возвращает человекочитаемое объяснение, что делать дальше.
// Не «ошибка», а подсказка: чаще всего проблема решается одной командой.
func (s Status) Hint() string {
	switch {
	case s.HasAPIToken:
		return ""
	case s.HasCookie:
		return "в хранилище только cookie-сессия" +
			func() string {
				if s.CookieSiteURL != "" {
					return " (" + s.CookieSiteURL + ")"
				}
				return ""
			}() +
			"; JSON API её не принимает.\n" +
			"Получи собственную сессию инструмента:  kinopub login --qr\n" +
			"или перенеси токен установленного приложения:  kinopub login --app"
	default:
		return "авторизации нет. Начни с:  kinopub login --qr"
	}
}

// Session — подхваченная авторизация.
type Session struct {
	creds credstore.Credentials
}

// Load читает сохранённую авторизацию. Отсутствие хранилища — это
// ErrNoSession, а не сбой: свежая машина просто ещё не авторизована.
func Load() (*Session, error) {
	creds, err := credstore.Load()
	if err != nil {
		return nil, fmt.Errorf("kinopub/session: load: %w", err)
	}
	if creds.IsEmpty() {
		return nil, ErrNoSession
	}
	return &Session{creds: creds}, nil
}

// Status отдаёт состояние без секретов.
func (s *Session) Status() Status {
	m := MethodNone
	switch {
	case s.creds.HasAppToken():
		m = MethodAPI
	case s.creds.HasCookie():
		m = MethodCookie
	}
	return Status{
		Method:        m,
		APIBase:       s.apiBase(),
		HasAPIToken:   s.creds.HasAppToken(),
		HasCookie:     s.creds.HasCookie(),
		TokenSource:   s.creds.TokenSource(),
		CanRefresh:    s.creds.CanRefresh() && s.creds.HasClientCredentials(),
		ExpiresAt:     s.creds.AppTokenExpiresAt,
		CookieSiteURL: s.creds.Site,
	}
}

func (s *Session) apiBase() string {
	if s.creds.APIBase != "" {
		return s.creds.APIBase
	}
	return kinopub.DefaultAPIBase
}

// Client отдаёт готовый клиент API, при необходимости обновив токен.
//
// httpClient обязателен: через него вызывающий задаёт прокси и таймауты.
// opts прокидываются в клиент как есть.
func (s *Session) Client(ctx context.Context, httpClient *http.Client, opts ...kinopub.Option) (*kinopub.Client, error) {
	if !s.creds.HasAppToken() {
		return nil, ErrNoAPIToken
	}
	if err := s.ensureFresh(ctx, httpClient); err != nil {
		// Неудачное обновление — не приговор: возможно, текущий токен ещё
		// жив. Отдаём клиент, а окончательный вердикт вынесет сам API.
		_ = err
	}
	ua := s.creds.AppUserAgent
	if ua == "" {
		ua = kinopubauth.DefaultUserAgent
	}
	all := append([]kinopub.Option{kinopub.WithUserAgent(ua)}, opts...)
	return kinopub.New(httpClient, s.apiBase(), s.creds.AppToken, all...), nil
}

// ensureFresh обновляет токен заранее, если это разрешено.
//
// Обновляется ТОЛЬКО собственная device-сессия: токен, заимствованный у
// установленного приложения, принадлежит телефону, и его ротация разлогинила
// бы приложение. Политика уже выражена в credstore.CanRefresh — здесь она не
// дублируется, а используется.
func (s *Session) ensureFresh(ctx context.Context, httpClient *http.Client) error {
	if !s.creds.AppTokenExpiringWithin(refreshLead) {
		return nil
	}
	if !s.creds.CanRefresh() || !s.creds.HasClientCredentials() {
		return nil
	}
	return s.Refresh(ctx, httpClient)
}

// Refresh принудительно обновляет токен и сохраняет его обратно в хранилище.
func (s *Session) Refresh(ctx context.Context, httpClient *http.Client) error {
	if !s.creds.CanRefresh() {
		return fmt.Errorf("kinopub/session: token source %q is not refreshable", s.creds.TokenSource())
	}
	if !s.creds.HasClientCredentials() {
		return errors.New("kinopub/session: OAuth client credentials are not stored")
	}

	auth := kinopubauth.New(httpClient, s.apiBase(), s.creds.AppClientID, s.creds.AppClientSecret)
	tok, err := auth.Refresh(ctx, s.creds.AppRefreshToken)
	if err != nil {
		return fmt.Errorf("kinopub/session: refresh: %w", err)
	}

	s.creds.AppToken = tok.Access
	if tok.Refresh != "" {
		s.creds.AppRefreshToken = tok.Refresh
	}
	s.creds.AppTokenExpiresAt = tok.ExpiresAt
	s.creds.AppSavedAt = time.Now()
	if err := credstore.Save(s.creds); err != nil {
		// Токен обновлён и работает, но не сохранён: следующий запуск
		// начнёт со старого. Сообщаем, а не глотаем.
		return fmt.Errorf("kinopub/session: token refreshed but not persisted: %w", err)
	}
	return nil
}
