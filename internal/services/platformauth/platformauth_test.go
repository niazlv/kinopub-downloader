// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package platformauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// fakePlatform plays the platform's side of the contract: one pending code
// that a test approves, denies or lets expire, and a refresh that rotates.
type fakePlatform struct {
	mu       sync.Mutex
	polls    int
	answer   string // "", "approved", "denied", "expired"
	slowOnce bool
	refresh  map[string]bool // refresh token -> still valid
}

func (f *fakePlatform) serve(t *testing.T) *httptest.Server {
	t.Helper()
	f.refresh = map[string]bool{}
	mux := http.NewServeMux()
	fail := func(w http.ResponseWriter, status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	pair := func(w http.ResponseWriter, n int) {
		f.refresh["r"+string(rune('0'+n))] = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "t" + string(rune('0'+n)), "refreshToken": "r" + string(rune('0'+n)),
			"expiresAt":        time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
			"refreshExpiresAt": time.Now().Add(180 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
	}
	mux.HandleFunc("/api/v1/auth/device", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Device string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Device == "" {
			fail(w, 400, "invalid_input")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deviceCode": "dc", "userCode": "KJ7Q-3MTR",
			"verificationUrl": "https://kino.example/#/link/KJ7Q-3MTR",
			"expiresAt":       time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339), "interval": 1,
		})
	})
	mux.HandleFunc("/api/v1/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ DeviceCode string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.DeviceCode != "dc" {
			fail(w, 400, "invalid_grant")
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.polls++
		switch {
		case f.slowOnce:
			f.slowOnce = false
			fail(w, 400, "slow_down")
		case f.answer == "approved":
			pair(w, 1)
		case f.answer == "denied":
			fail(w, 400, "access_denied")
		case f.answer == "expired":
			fail(w, 400, "expired_token")
		default:
			fail(w, 400, "authorization_pending")
		}
	})
	mux.HandleFunc("/api/v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ RefreshToken string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.refresh[in.RefreshToken] {
			fail(w, 401, "unauthorized")
			return
		}
		f.refresh[in.RefreshToken] = false
		pair(w, 2)
	})
	mux.HandleFunc("/api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(SessionCookie); err != nil || c.Value != "t1" {
			fail(w, 401, "unauthorized")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "mama", "displayName": "Мама"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newClient(srv *httptest.Server) *Client {
	c := New(srv.Client(), srv.URL+"/")
	c.minInterval = time.Millisecond
	c.slowDown = time.Millisecond
	return c
}

func TestDeviceFlowApproved(t *testing.T) {
	f := &fakePlatform{slowOnce: true}
	srv := f.serve(t)
	c := newClient(srv)
	ctx := context.Background()

	dc, err := c.StartDevice(ctx, "kinopub on test")
	if err != nil {
		t.Fatal(err)
	}
	if dc.UserCode != "KJ7Q-3MTR" || dc.VerificationURL == "" || dc.Code != "dc" {
		t.Fatalf("device code: %+v", dc)
	}
	dc.Interval = time.Millisecond

	// Approve while the poll loop is running.
	go func() {
		time.Sleep(20 * time.Millisecond)
		f.mu.Lock()
		f.answer = "approved"
		f.mu.Unlock()
	}()
	sess, err := c.PollDevice(ctx, dc)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Token != "t1" || sess.RefreshToken != "r1" || sess.ExpiresAt.IsZero() || sess.RefreshExpiresAt.IsZero() {
		t.Fatalf("session: %+v", sess)
	}
	if sess.CookieHeader() != "kino_session=t1" {
		t.Fatalf("cookie header = %q", sess.CookieHeader())
	}
	f.mu.Lock()
	polls := f.polls
	f.mu.Unlock()
	if polls < 2 {
		t.Fatalf("the loop should have waited through pending and slow_down, polled %d times", polls)
	}

	acct, err := c.Me(ctx, sess.Token)
	if err != nil || acct.Login != "mama" {
		t.Fatalf("me: %+v, %v", acct, err)
	}
	if _, err := c.Me(ctx, "stale"); !errors.Is(err, domain.ErrPlatformSessionRequired) {
		t.Fatalf("stale token: %v", err)
	}
}

func TestDeviceFlowDeniedAndExpired(t *testing.T) {
	for _, answer := range []string{"denied", "expired"} {
		f := &fakePlatform{answer: answer}
		srv := f.serve(t)
		c := newClient(srv)
		dc := DeviceCode{Code: "dc", Interval: time.Millisecond, ExpiresAt: time.Now().Add(time.Minute)}
		_, err := c.PollDevice(context.Background(), dc)
		want := domain.ErrDeviceAuthDenied
		if answer == "expired" {
			want = domain.ErrDeviceAuthExpired
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: got %v, want %v", answer, err, want)
		}
	}
	// An unknown code reads as expired: from the tool's side both mean "start over".
	f := &fakePlatform{}
	c := newClient(f.serve(t))
	if _, err := c.PollOnce(context.Background(), "nope"); !errors.Is(err, domain.ErrDeviceAuthExpired) {
		t.Errorf("unknown code: %v", err)
	}
}

func TestRefreshRotates(t *testing.T) {
	f := &fakePlatform{answer: "approved"}
	c := newClient(f.serve(t))
	ctx := context.Background()
	sess, err := c.PollOnce(ctx, "dc")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := c.Refresh(ctx, sess.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Token != "t2" || fresh.RefreshToken != "r2" {
		t.Fatalf("refreshed: %+v", fresh)
	}
	if _, err := c.Refresh(ctx, sess.RefreshToken); !errors.Is(err, domain.ErrPlatformRefreshRejected) {
		t.Fatalf("spent refresh token: %v", err)
	}
	if _, err := c.Refresh(ctx, ""); !errors.Is(err, domain.ErrPlatformRefreshRejected) {
		t.Fatalf("empty refresh token: %v", err)
	}
}
