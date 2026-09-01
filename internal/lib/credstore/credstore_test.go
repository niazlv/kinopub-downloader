// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package credstore

import (
	"encoding/json"
	"strings"
	"testing"
)

// The stored file is machine-bound, so Save/Load cannot be exercised without
// touching the real config directory and the machine seed. What can be pinned
// down here is the serialization contract those two share: which fields survive
// a round trip, and what a file written by an older version decodes to.

func TestCredentialsRoundTrip(t *testing.T) {
	want := Credentials{
		Cookie:    "cf_clearance=abc; _identity=def",
		UserAgent: "Mozilla/5.0 (Macintosh)",
		Site:      "kino.watch",
	}

	blob, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Credentials
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !strings.Contains(string(blob), `"site":"kino.watch"`) {
		t.Errorf("site not persisted under the expected key: %s", blob)
	}
}

// A credentials.enc written before the site was recorded must still decode, and
// must decode to an empty Site — callers key their leniency off exactly that.
func TestCredentialsLegacyPayloadHasEmptySite(t *testing.T) {
	legacy := []byte(`{"cookie":"cf_clearance=abc","user_agent":"Mozilla/5.0"}`)

	var got Credentials
	if err := json.Unmarshal(legacy, &got); err != nil {
		t.Fatalf("unmarshal legacy payload: %v", err)
	}
	if got.Cookie != "cf_clearance=abc" || got.UserAgent != "Mozilla/5.0" {
		t.Errorf("legacy fields lost: %+v", got)
	}
	if got.Site != "" {
		t.Errorf("Site = %q, want empty for a legacy payload", got.Site)
	}
	if got.IsEmpty() {
		t.Error("IsEmpty() = true, want false: legacy credentials still carry a session")
	}
}

func TestCredentialsIsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		creds Credentials
		want  bool
	}{
		{"zero", Credentials{}, true},
		{"cookie_only", Credentials{Cookie: "a=b"}, false},
		{"user_agent_only", Credentials{UserAgent: "Mozilla/5.0"}, false},
		{"full", Credentials{Cookie: "a=b", UserAgent: "Mozilla/5.0", Site: "kino.watch"}, false},
		// A site without a session is nothing to send.
		{"site_only", Credentials{Site: "kino.watch"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.creds.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}

// deriveKey is pure, so its shape and determinism are checkable without a real
// machine seed. The stored file is only readable if this stays stable.
func TestDeriveKey(t *testing.T) {
	seed := []byte("machine-seed")
	key := deriveKey(seed)
	if len(key) != 32 {
		t.Fatalf("key length = %d, want 32 (AES-256)", len(key))
	}
	if string(deriveKey(seed)) != string(key) {
		t.Error("deriveKey is not deterministic for the same seed")
	}
	if string(deriveKey([]byte("other-seed"))) == string(key) {
		t.Error("different seeds produced the same key")
	}
}

// The app-session fields round-trip and are distinct from the cookie fields: a
// user may hold both a website (cookie) login and an app login at once, and the
// two User-Agents must not collide.
func TestCredentialsAppFieldsRoundTrip(t *testing.T) {
	want := Credentials{
		Cookie:       "cf_clearance=abc",
		UserAgent:    "Mozilla/5.0 (Macintosh)",
		Site:         "kino.watch",
		AppToken:     "acc456",
		AppUserAgent: "Android KinoPub/1.34 (Linux;Android 16) ExoPlayerLib/2.11.8",
		APIBase:      "https://api.service-kp.com/v1",
	}
	blob, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Credentials
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	if !strings.Contains(string(blob), `"app_token":"acc456"`) {
		t.Errorf("app token not persisted under the expected key: %s", blob)
	}
}

// An app-only login (token, no cookie) is not empty, but must not be treated as
// a website session: HasCookie gates the cookie path so the Android User-Agent
// never leaks onto website requests.
func TestCredentialsAppOnlyIsNotACookieSession(t *testing.T) {
	c := Credentials{
		AppToken:     "tok",
		AppUserAgent: "Android KinoPub/1.34 (Linux;Android 16) ExoPlayerLib/2.11.8",
	}
	if c.IsEmpty() {
		t.Error("IsEmpty() = true, want false: an app token is a session")
	}
	if c.HasCookie() {
		t.Error("HasCookie() = true, want false: no cookie was stored")
	}
	if !c.HasAppToken() {
		t.Error("HasAppToken() = false, want true")
	}
}

// A pre-app credentials file still decodes, with the app fields empty.
func TestCredentialsPreAppPayloadDecodes(t *testing.T) {
	old := []byte(`{"cookie":"cf=1","user_agent":"UA","site":"kino.watch"}`)
	var got Credentials
	if err := json.Unmarshal(old, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.HasAppToken() {
		t.Errorf("AppToken = %q, want empty for a pre-app payload", got.AppToken)
	}
	if !got.HasCookie() {
		t.Error("HasCookie() = false, want true")
	}
}
