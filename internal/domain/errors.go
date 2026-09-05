// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"errors"

	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
)

// Sentinel errors for the kinopub downloader.
// Each maps to a specific requirement for traceability.
var (
	ErrExactlyOneURL          = errors.New("exactly one URL argument is required")          // Req 1.4
	ErrInvalidInputURL        = errors.New("input URL is invalid or unclassified")          // Req 1.5
	ErrFeedTokenUnavailable   = errors.New("feed token could not be obtained")              // Req 1.6
	ErrEmptyFeed              = errors.New("no downloadable episodes were found")           // Req 2.6
	ErrFeedParse              = errors.New("feed could not be parsed")                      // Req 2.5
	ErrFeedRetrieval          = errors.New("feed retrieval failed")                         // Req 2.7
	ErrNoVideoTrack           = errors.New("no video track could be resolved")              // Req 3.5
	ErrInvalidProxyURL        = errors.New("proxy URL is invalid")                          // Req 6.4
	ErrProxyUnsupportedFFmpeg = errors.New("proxy scheme not supported by ffmpeg")          // Req 6.6
	ErrFFmpegNotFound         = errors.New("ffmpeg is required but was not found")          // Req 7.3
	ErrFFmpegFailed           = errors.New("ffmpeg exited with a non-zero status")          // Req 7.4
	ErrEmptyOutput            = errors.New("ffmpeg produced an empty or missing file")      // Req 7.7
	ErrOutputDirUnwritable    = errors.New("output directory cannot be created or written") // Req 11.7
	ErrInvalidFlag            = errors.New("invalid flag value")                            // Req 15.4
	ErrMissingDependency      = errors.New("required component dependency not provided")    // Req 16.5
	ErrAuthRequired           = errors.New("content appears to require authentication")     // Req 17.3, 17.4

	// ErrPlatformSessionRequired reports a platform page link (…/#/title/…)
	// given without a session for that platform. Neither pipeline nor
	// credential swap can cure it: the platform publishes no feed for the RSS
	// path, and it does not accept a kino.pub session — only its own cookie.
	ErrPlatformSessionRequired = errors.New("platform page needs that site's session")

	// ErrNoSubtitlesMatched reports that an episode carries none of the
	// subtitles --subs-only asked for. Only strict (subtitles-only) runs raise
	// it: elsewhere subtitles are optional and a missing track falls back.
	ErrNoSubtitlesMatched = errors.New("no subtitles matched the selection")

	// ErrAPIUnauthorized reports that the kino.pub JSON API rejected the access
	// token (HTTP 401). In apitoken mode this means the token read from the
	// mobile app has expired: the user must open the app (or run the su refresh
	// helper) so it mints a fresh one, then re-run.
	// Значение заимствовано у публичной библиотеки, а не заведено своё:
	// errors.Is должен срабатывать на ошибке, пришедшей из pkg/kinopub.
	// Отдельный сентинел с тем же смыслом означал бы, что часть проверок
	// молча перестаёт ловить нужный случай.
	ErrAPIUnauthorized = kinopub.ErrUnauthorized

	// ErrAppTokenUnavailable reports that no access token could be obtained for
	// apitoken mode — neither supplied explicitly nor readable from the
	// installed mobile app (no root, app not installed, or store unreadable).
	ErrAppTokenUnavailable = errors.New("kino.pub API access token is unavailable")

	// ErrItemIDUnrecognized reports that a URL is not a kino.pub item link the
	// API backend can turn into an item id.
	ErrItemIDUnrecognized = errors.New("URL is not a recognizable kino.pub item link")

	// ErrDeviceAuthPending reports that the user has not yet approved the device
	// authorization. It is the normal answer while polling, not a failure.
	ErrDeviceAuthPending = errors.New("device authorization is still pending")

	// ErrDeviceAuthExpired reports that the user code was not approved before it
	// expired; a new code must be requested.
	ErrDeviceAuthExpired = errors.New("device authorization code expired")

	// ErrDeviceAuthDenied reports that the user rejected the authorization.
	ErrDeviceAuthDenied = errors.New("device authorization was denied")

	// ErrTokenRefreshRejected reports that the refresh token was not accepted,
	// so the session must be established again from scratch.
	ErrTokenRefreshRejected = errors.New("kino.pub rejected the refresh token")

	// ErrTokenNotRefreshable reports that a session may not be refreshed by this
	// tool. A session imported from the installed mobile app is exactly that
	// case: refreshing rotates the token and would sign the phone app out, so
	// only sessions this tool obtained itself are ever refreshed.
	ErrTokenNotRefreshable = errors.New("this session is not refreshable by kinopub")

	// ErrClientSecretUnavailable reports that the OAuth client secret needed to
	// talk to the authorization endpoint could not be obtained.
	ErrClientSecretUnavailable = errors.New("kino.pub OAuth client secret is unavailable")
)
