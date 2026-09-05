// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
	"github.com/niazlv/kinopub-downloader/internal/services/kinopubauth"
	"github.com/niazlv/kinopub-downloader/internal/services/platformauth"
	"github.com/niazlv/kinopub-downloader/internal/services/proxyprovider"
)

// Сессия устройства на платформе: `login --qr --site <host>` и её продление.
//
// Это сестра `login --qr` для kino.pub: тот же ход с кодом на другом экране,
// но сессию выдаёт платформа, и она — своя у утилиты: не зависит от куки
// браузера и продлевается refresh-токеном, пока ею пользуются. Хранится она
// в посайтовом хранилище как логин этого сайта, поэтому путь скачивания о
// ней ничего особенного не знает.

// platformRefreshLeeway — за сколько до истечения токена сессия продлевается.
// Токен живёт месяц, refresh — полгода: сутки запаса покрывают и самую
// долгую выкачку, и остановленные на ночь часы.
const platformRefreshLeeway = 24 * time.Hour

// deviceName — как утилита представится человеку на странице подтверждения
// и в админке платформы. Версия и имя машины: ровно то, по чему через
// месяц можно понять, какое из устройств отозвать.
func deviceName() string {
	host, _ := os.Hostname()
	host = strings.TrimSuffix(strings.TrimSpace(host), ".local")
	if host == "" {
		return "kinopub " + version
	}
	return "kinopub " + version + " on " + host
}

func platformClient(proxyURL string, site domain.Site, ua string) (*platformauth.Client, error) {
	pp, err := proxyprovider.New(proxyURL)
	if err != nil {
		return nil, err
	}
	if ua == "" {
		ua = kinopubauth.DefaultUserAgent
	}
	return platformauth.New(pp.HTTPClient(), site.Origin(), platformauth.WithUserAgent(ua)), nil
}

// loginPlatformQR implements `login --qr --site <host>`.
func loginPlatformQR(ctx context.Context, siteHost, proxyURL, explicitUA string) int {
	site := domain.SiteFromHost(siteHost)
	ua := explicitUA
	if ua == "" {
		ua = kinopubauth.DefaultUserAgent
	}
	client, err := platformClient(proxyURL, site, ua)
	if err != nil {
		errorf("%v", err)
		return 1
	}

	dc, err := client.StartDevice(ctx, deviceName())
	if err != nil {
		errorf("could not start device authorization on %s: %v", site, err)
		return 1
	}
	presentDeviceCode(devicePrompt{
		scanURI: dc.VerificationURL, link: dc.VerificationURL, code: dc.UserCode, expiresAt: dc.ExpiresAt,
	})

	sess, err := client.PollDevice(ctx, dc)
	if err != nil {
		reportDeviceAuthFailure(err, fmt.Sprintf("%s login --qr --site %s", os.Args[0], site))
		return 1
	}

	// Confirm the session works before storing it, and show whose it is.
	acct, err := client.Me(ctx, sess.Token)
	if err != nil {
		errorf("the platform issued a session but does not accept it: %v", err)
		return 1
	}

	creds, err := credstore.Load()
	if err != nil {
		creds = credstore.Credentials{}
	}
	creds.SetSession(site.String(), sessionFrom(sess, ua, time.Now()))
	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	who := acct.DisplayName
	if who == "" {
		who = acct.Login
	}
	fmt.Fprintf(os.Stderr, "\n%s %s session for %s saved (encrypted, machine-bound).\n",
		errStyle.Green("✓"), errStyle.Cyan(site.String()), errStyle.Cyan(who))
	fmt.Fprintf(os.Stderr, "  It belongs to this tool and renews itself: no re-login while you keep using it.\n"+
		"  Now just paste a link from the address bar, e.g.:\n    %s %s/#/title/201\n",
		os.Args[0], site.Origin())
	if others := otherSites(creds, site.String()); len(others) > 0 {
		notef("logins for %s are kept: website logins are held per site.", strings.Join(others, ", "))
	}
	return 0
}

// sessionFrom turns the platform's pair into the stored login of that site.
func sessionFrom(sess platformauth.Session, ua string, now time.Time) credstore.SiteSession {
	return credstore.SiteSession{
		Cookie:           sess.CookieHeader(),
		UserAgent:        ua,
		SavedAt:          now,
		RefreshToken:     sess.RefreshToken,
		ExpiresAt:        sess.ExpiresAt,
		RefreshExpiresAt: sess.RefreshExpiresAt,
	}
}

// platformSessionNeedsRenewal says whether a stored login should be renewed
// before a run: only a device session can be, and only when its token is
// about to expire — or already has, which the refresh token also covers.
func platformSessionNeedsRenewal(s credstore.SiteSession, now time.Time) bool {
	return s.Renewable() && s.ExpiringWithin(now, platformRefreshLeeway)
}

// ensureFreshPlatformSession renews the stored device session for the site
// when it is about to expire, so a run never starts with a token the platform
// is about to refuse. Failure is not fatal: the run proceeds with what is
// stored, and the platform's answer decides.
func ensureFreshPlatformSession(ctx context.Context, site domain.Site, proxyURL string) {
	stored, err := credstore.Load()
	if err != nil {
		return
	}
	s, key, ok := stored.SessionFor(site)
	if !ok || !platformSessionNeedsRenewal(s, time.Now()) {
		return
	}
	refreshPlatformSession(ctx, site, key, s, proxyURL, "expiring soon")
}

// refreshPlatformAfterRejection renews the session after the platform refused
// the current token mid-run. The run itself is over — its clients were built
// with the old token — so the outcome is advice: run again, or log in again.
func refreshPlatformAfterRejection(ctx context.Context, site domain.Site, proxyURL string) bool {
	stored, err := credstore.Load()
	if err != nil {
		return false
	}
	s, key, ok := stored.SessionFor(site)
	if !ok || !s.Renewable() {
		return false
	}
	return refreshPlatformSession(ctx, site, key, s, proxyURL, "rejected by the platform")
}

// refreshPlatformSession performs the exchange and stores the new pair.
func refreshPlatformSession(ctx context.Context, site domain.Site, key string, s credstore.SiteSession,
	proxyURL, reason string) bool {
	client, err := platformClient(proxyURL, site, s.UserAgent)
	if err != nil {
		warnf("could not renew the %s session: %v", site, err)
		return false
	}
	fresh, err := client.Refresh(ctx, s.RefreshToken)
	if err != nil {
		if errors.Is(err, domain.ErrPlatformRefreshRejected) {
			warnf("the %s session could not be renewed (%s): %v. It was revoked or its refresh "+
				"token expired — log in again:  %s login --qr --site %s", site, reason, err, os.Args[0], site)
		} else {
			warnf("could not renew the %s session (%s): %v", site, reason, err)
		}
		return false
	}

	stored, err := credstore.Load()
	if err != nil {
		stored = credstore.Credentials{}
	}
	stored.SetSession(key, sessionFrom(fresh, s.UserAgent, time.Now()))
	if err := credstore.Save(stored); err != nil {
		// The platform has already rotated: the old pair is dead. A store
		// that could not be written is a loud problem, not a footnote.
		errorf("renewed the %s session but could not save it: %v — the next run will need "+
			"`%s login --qr --site %s`", site, err, os.Args[0], site)
		return false
	}
	notef("%s session renewed automatically (%s).", site, reason)
	return true
}
