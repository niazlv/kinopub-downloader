// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// newTestClient wires a Client to a stub endpoint, with polling sped up so the
// tests do not spend real seconds waiting.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(srv.Client(), srv.URL, "android", "s3cret")
	c.oauthBase = srv.URL // the stub serves the endpoint at its own root
	// Keep the pacing floors out of the way; production keeps the real ones.
	c.minInterval = time.Millisecond
	c.slowDown = time.Millisecond
	return c, srv
}

func TestOAuthBaseFromAPIBase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://api.service-kp.com/v1", "https://api.service-kp.com"},
		{"https://api.service-kp.com/v1/", "https://api.service-kp.com"},
		{"https://api.service-kp.com", "https://api.service-kp.com"},
		{"http://localhost:8080/v2", "http://localhost:8080"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := OAuthBaseFromAPIBase(tt.in); got != tt.want {
			t.Errorf("OAuthBaseFromAPIBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStartDevice(t *testing.T) {
	var gotGrant, gotID string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant, gotID = r.Form.Get("grant_type"), r.Form.Get("client_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"dev-code","user_code":"ABCD-1234",
			"verification_uri":"https://kino.pub/device","expires_in":600,"interval":5}`))
	})

	dc, err := c.StartDevice(context.Background())
	if err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if gotGrant != "device_code" {
		t.Errorf("grant_type = %q, want device_code", gotGrant)
	}
	if gotID != "android" {
		t.Errorf("client_id = %q", gotID)
	}
	if dc.Code != "dev-code" || dc.UserCode != "ABCD-1234" {
		t.Errorf("unexpected device code: %+v", dc)
	}
	if dc.VerificationURI != "https://kino.pub/device" {
		t.Errorf("verification URI = %q", dc.VerificationURI)
	}
	if dc.Interval != 5*time.Second {
		t.Errorf("interval = %v, want 5s", dc.Interval)
	}
	if dc.ExpiresAt.IsZero() {
		t.Error("ExpiresAt not set")
	}
}

// Without a client secret the flow cannot start, and must say so precisely
// rather than sending a request that is certain to fail.
func TestStartDeviceRequiresClientSecret(t *testing.T) {
	c := New(http.DefaultClient, "https://example.invalid/v1", "android", "")
	if _, err := c.StartDevice(context.Background()); !errors.Is(err, domain.ErrClientSecretUnavailable) {
		t.Errorf("err = %v, want ErrClientSecretUnavailable", err)
	}
}

// A pending authorization is the normal answer while the user is still
// approving, and must be distinguishable from a real failure.
func TestPollOncePending(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	})
	if _, err := c.PollOnce(context.Background(), "dev-code"); !errors.Is(err, domain.ErrDeviceAuthPending) {
		t.Errorf("err = %v, want ErrDeviceAuthPending", err)
	}
}

func TestPollOnceMapsErrors(t *testing.T) {
	tests := []struct {
		code string
		want error
	}{
		{"expired_token", domain.ErrDeviceAuthExpired},
		{"access_denied", domain.ErrDeviceAuthDenied},
		{"invalid_grant", domain.ErrTokenRefreshRejected},
	}
	for _, tt := range tests {
		body := `{"error":"` + tt.code + `"}`
		c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
		})
		if _, err := c.PollOnce(context.Background(), "x"); !errors.Is(err, tt.want) {
			t.Errorf("%s mapped to %v, want %v", tt.code, err, tt.want)
		}
	}
}

// The happy path: poll returns pending twice, then issues a token.
func TestPollDeviceSucceedsAfterPending(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	})

	dc := DeviceCode{
		Code:      "dev-code",
		Interval:  time.Millisecond,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	tok, err := c.PollDevice(context.Background(), dc)
	if err != nil {
		t.Fatalf("PollDevice: %v", err)
	}
	if tok.Access != "AT" || tok.Refresh != "RT" {
		t.Errorf("token = %+v", tok)
	}
	if tok.ExpiresAt.IsZero() {
		t.Error("ExpiresAt not derived from expires_in")
	}
	if calls.Load() != 3 {
		t.Errorf("polled %d times, want 3", calls.Load())
	}
}

// An already-expired code must fail without hitting the network at all.
func TestPollDeviceStopsWhenCodeExpired(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	})

	dc := DeviceCode{Code: "x", Interval: time.Millisecond, ExpiresAt: time.Now().Add(-time.Second)}
	if _, err := c.PollDevice(context.Background(), dc); !errors.Is(err, domain.ErrDeviceAuthExpired) {
		t.Errorf("err = %v, want ErrDeviceAuthExpired", err)
	}
	if calls.Load() != 0 {
		t.Errorf("made %d requests for an expired code, want 0", calls.Load())
	}
}

// "slow_down" must widen the interval rather than abort the flow.
func TestPollDeviceHonoursSlowDown(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"slow_down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"AT"}`))
	})

	// A tiny increment keeps the test fast while still exercising the branch.
	dc := DeviceCode{Code: "x", Interval: time.Millisecond, ExpiresAt: time.Now().Add(time.Minute)}
	tok, err := c.PollDevice(context.Background(), dc)
	if err != nil {
		t.Fatalf("PollDevice: %v", err)
	}
	if tok.Access != "AT" {
		t.Errorf("token = %+v", tok)
	}
}

func TestPollDeviceCancellable(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dc := DeviceCode{Code: "x", Interval: time.Hour, ExpiresAt: time.Now().Add(time.Hour)}
	if _, err := c.PollDevice(ctx, dc); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRefresh(t *testing.T) {
	var gotGrant, gotRefresh string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant, gotRefresh = r.Form.Get("grant_type"), r.Form.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"AT2","refresh_token":"RT2","expires_in":7200}`))
	})

	tok, err := c.Refresh(context.Background(), "RT1")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if gotGrant != "refresh_token" || gotRefresh != "RT1" {
		t.Errorf("grant=%q refresh=%q", gotGrant, gotRefresh)
	}
	if tok.Access != "AT2" || tok.Refresh != "RT2" {
		t.Errorf("token = %+v", tok)
	}
}

func TestRefreshRejected(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	if _, err := c.Refresh(context.Background(), "stale"); !errors.Is(err, domain.ErrTokenRefreshRejected) {
		t.Errorf("err = %v, want ErrTokenRefreshRejected", err)
	}
}

func TestRefreshWithoutTokenFailsFast(t *testing.T) {
	c := New(http.DefaultClient, "https://example.invalid/v1", "android", "s")
	if _, err := c.Refresh(context.Background(), ""); !errors.Is(err, domain.ErrTokenRefreshRejected) {
		t.Errorf("err = %v, want ErrTokenRefreshRejected", err)
	}
}

// The default User-Agent must identify this tool rather than imitate the
// Android app — that honesty is the whole point of this flow.
func TestDefaultUserAgentIsHonest(t *testing.T) {
	var gotUA string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"c","user_code":"u","verification_uri":"v","expires_in":60,"interval":5}`))
	})
	if _, err := c.StartDevice(context.Background()); err != nil {
		t.Fatalf("StartDevice: %v", err)
	}
	if !strings.Contains(gotUA, "kinopub-downloader") {
		t.Errorf("User-Agent = %q, want it to name this tool", gotUA)
	}
	if strings.Contains(strings.ToLower(gotUA), "android kinopub") {
		t.Errorf("User-Agent imitates the mobile app: %q", gotUA)
	}
}
