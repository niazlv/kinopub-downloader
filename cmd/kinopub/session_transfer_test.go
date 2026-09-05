// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
)

// Version consistency: an equal or older envelope imports, a newer one is
// refused rather than half-understood, and a file with no schema is rejected
// as not being an export at all.
func TestCheckExportSchema(t *testing.T) {
	if err := checkExportSchema(sessionExportSchema); err != nil {
		t.Errorf("current schema rejected: %v", err)
	}
	if sessionExportSchema > 1 {
		if err := checkExportSchema(sessionExportSchema - 1); err != nil {
			t.Errorf("older schema rejected: %v", err)
		}
	}
	if err := checkExportSchema(sessionExportSchema + 1); err == nil {
		t.Error("a newer schema must be refused")
	} else if !strings.Contains(err.Error(), "update kinopub") {
		t.Errorf("the error should tell the user what to do, got: %v", err)
	}
	for _, bad := range []int{0, -1} {
		if err := checkExportSchema(bad); err == nil {
			t.Errorf("schema %d must be rejected", bad)
		}
	}
}

// The envelope must survive a round trip with every field that matters,
// including the provenance that decides whether the session may be refreshed.
func TestSessionExportRoundTrip(t *testing.T) {
	want := sessionExport{
		Schema:      sessionExportSchema,
		ToolVersion: "v1.2.3",
		ExportedAt:  time.Unix(1800000000, 0).UTC(),
		Session: sessionPayload{
			AppToken:          "AT",
			AppRefreshToken:   "RT",
			AppTokenSource:    credstore.SourceDevice,
			AppTokenExpiresAt: time.Unix(1800003600, 0).UTC(),
			AppUserAgent:      "kinopub-downloader",
			APIBase:           "https://api.example/v1",
			AppClientID:       "android",
			AppClientSecret:   "secret",
		},
	}

	blob, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got sessionExport
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got.Session, want.Session) {
		t.Errorf("payload round trip lost data:\n got %+v\nwant %+v", got.Session, want.Session)
	}
	if got.Schema != sessionExportSchema {
		t.Errorf("schema = %d", got.Schema)
	}
}

// Provenance must travel: a phone-imported session stays non-refreshable after
// crossing machines, or renewing it there would sign the phone app out.
func TestImportedProvenanceKeepsRefreshRules(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		wantRefresh bool
	}{
		{"device session stays refreshable", credstore.SourceDevice, true},
		{"phone session stays non-refreshable", credstore.SourceApp, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mirror what runSessionsImport builds from a payload.
			creds := credstore.Credentials{
				AppToken:        "AT",
				AppRefreshToken: "RT",
				AppTokenSource:  tt.source,
			}
			if got := creds.CanRefresh(); got != tt.wantRefresh {
				t.Errorf("CanRefresh() = %v, want %v", got, tt.wantRefresh)
			}
		})
	}
}

// A cookie session is only exported when explicitly asked for, since it is
// bound to one browser and site.
func TestPayloadOmitsCookieByDefault(t *testing.T) {
	p := sessionPayload{AppToken: "AT"}
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "cookie") {
		t.Errorf("empty cookie fields should be omitted, got %s", blob)
	}
}

// Every site's login travels when cookies are asked for, and the kino.pub one
// is also written to the legacy slot so an older build imports it.
func TestPayloadCarriesEveryWebsiteLogin(t *testing.T) {
	creds := credstore.Credentials{AppToken: "AT"}
	creds.SetSession("kino.watch", credstore.SiteSession{Cookie: "kp=1", UserAgent: "UA"})
	creds.SetSession("kino.sorewa.ru", credstore.SiteSession{Cookie: "pf=1"})

	p := payloadFrom(creds, true)
	if p.Cookie != "kp=1" || p.Site != "kino.watch" {
		t.Errorf("legacy slot = %q for %q, want the kino.watch login", p.Cookie, p.Site)
	}
	if len(p.Sites) != 2 || p.Sites["kino.sorewa.ru"].Cookie != "pf=1" || p.Sites["kino.watch"].UserAgent != "UA" {
		t.Errorf("sites = %+v", p.Sites)
	}

	var back credstore.Credentials
	applyPayload(&back, p, time.Unix(1800000000, 0))
	if !back.HasCookieFor(domain.Site{Host: "kino.watch"}) || !back.HasCookieFor(domain.Site{Host: "kino.sorewa.ru"}) {
		t.Errorf("import lost a login: %+v", back.Sessions())
	}
	if back.AppToken != "AT" {
		t.Error("import lost the app session")
	}

	// An envelope from an older build has only the legacy slot.
	var older credstore.Credentials
	applyPayload(&older, sessionPayload{Site: "kino.watch", Cookie: "kp=1"}, time.Unix(1800000000, 0))
	if s, _, ok := older.SessionFor(domain.Site{Host: "kino.watch"}); !ok || s.Cookie != "kp=1" {
		t.Errorf("legacy envelope not imported: %+v", older.Sessions())
	}
}
