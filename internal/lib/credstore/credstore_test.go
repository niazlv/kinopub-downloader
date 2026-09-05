// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"github.com/niazlv/kinopub-downloader/internal/domain"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if !reflect.DeepEqual(got, want) {
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
	if !reflect.DeepEqual(got, want) {
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

// encryptWith mirrors Save's encryption for a chosen seed, so the legacy-seed
// migration path can be exercised without a real machine identifier.
func encryptWith(t *testing.T, seed []byte, creds Credentials) []byte {
	t.Helper()
	plaintext, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	block, err := aes.NewCipher(deriveKey(seed))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil)
}

func TestDecryptWithRoundTrip(t *testing.T) {
	want := Credentials{Cookie: "cf=1", UserAgent: "UA", AppToken: "tok"}
	blob := encryptWith(t, []byte("seed-a"), want)

	got, err := decryptWith([]byte("seed-a"), blob)
	if err != nil {
		t.Fatalf("decryptWith: %v", err)
	}
	// decryptWith hands back the normalized form: the login filed under its
	// site, and the legacy slot mirroring it.
	want.normalize()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// A blob written under one seed must not open under another — this is what
// binds a store to the machine that wrote it.
func TestDecryptWithWrongSeedFails(t *testing.T) {
	blob := encryptWith(t, []byte("seed-a"), Credentials{Cookie: "cf=1"})
	if _, err := decryptWith([]byte("seed-b"), blob); err == nil {
		t.Fatal("decryptWith with a different seed succeeded, want failure")
	}
}

func TestDecryptWithTruncatedBlob(t *testing.T) {
	if _, err := decryptWith([]byte("seed"), []byte("short")); err == nil {
		t.Fatal("want error for a blob shorter than the nonce")
	}
}

func TestPreferredMethod(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		creds Credentials
		want  string
	}{
		{
			name:  "nothing stored",
			creds: Credentials{},
			want:  "",
		},
		{
			name:  "only a cookie",
			creds: Credentials{Cookie: "cf=1"},
			want:  MethodCookie,
		},
		{
			name:  "only an app token",
			creds: Credentials{AppToken: "tok"},
			want:  MethodApp,
		},
		{
			name:  "both, app saved more recently",
			creds: Credentials{Cookie: "cf=1", CookieSavedAt: early, AppToken: "tok", AppSavedAt: late},
			want:  MethodApp,
		},
		{
			name:  "both, cookie saved more recently",
			creds: Credentials{Cookie: "cf=1", CookieSavedAt: late, AppToken: "tok", AppSavedAt: early},
			want:  MethodCookie,
		},
		{
			// Last successful use outranks an older save of the other method.
			name: "app used after the cookie was saved",
			creds: Credentials{
				Cookie: "cf=1", CookieSavedAt: late,
				AppToken: "tok", AppSavedAt: early,
				LastUsed: MethodApp, LastUsedAt: late.Add(time.Hour),
			},
			want: MethodApp,
		},
		{
			// A store written before timestamps existed keeps the behaviour it
			// was created under: the website session.
			name:  "both, no timestamps at all",
			creds: Credentials{Cookie: "cf=1", AppToken: "tok"},
			want:  MethodCookie,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.creds.PreferredMethod(); got != tt.want {
				t.Errorf("PreferredMethod() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Saving one method must never discard the other: a user may hold both a
// website login and an app session and switch between them.
func TestBothMethodsCoexistThroughSerialization(t *testing.T) {
	both := Credentials{
		Cookie: "cf=1", UserAgent: "Mozilla/5.0", Site: "kino.watch",
		AppToken: "tok", AppUserAgent: "Android KinoPub/1.34",
	}
	blob, err := json.Marshal(both)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Credentials
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.HasCookie() || !got.HasAppToken() {
		t.Errorf("a method was lost: %+v", got)
	}
	if got.UserAgent == got.AppUserAgent {
		t.Error("browser and app User-Agents must stay distinct")
	}
}

// The refresh safety switch. Refreshing a session imported from the phone app
// would rotate the token and sign the app out, so only a session this tool
// authorized itself may ever be refreshed.
func TestCanRefreshOnlyForOwnDeviceSession(t *testing.T) {
	tests := []struct {
		name  string
		creds Credentials
		want  bool
	}{
		{
			name:  "device session with a refresh token",
			creds: Credentials{AppToken: "AT", AppTokenSource: SourceDevice, AppRefreshToken: "RT"},
			want:  true,
		},
		{
			name:  "imported app session is never refreshable",
			creds: Credentials{AppToken: "AT", AppTokenSource: SourceApp, AppRefreshToken: "RT"},
			want:  false,
		},
		{
			// Written before provenance existed: could only be an import, so it
			// must not be treated as refreshable.
			name:  "legacy store without a recorded source",
			creds: Credentials{AppToken: "AT", AppRefreshToken: "RT"},
			want:  false,
		},
		{
			name:  "device session without a refresh token",
			creds: Credentials{AppToken: "AT", AppTokenSource: SourceDevice},
			want:  false,
		},
		{
			name:  "no app token at all",
			creds: Credentials{AppTokenSource: SourceDevice, AppRefreshToken: "RT"},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.creds.CanRefresh(); got != tt.want {
				t.Errorf("CanRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTokenSourceNormalizesLegacy(t *testing.T) {
	if got := (Credentials{AppToken: "AT"}).TokenSource(); got != SourceApp {
		t.Errorf("legacy TokenSource() = %q, want %q", got, SourceApp)
	}
	if got := (Credentials{AppToken: "AT", AppTokenSource: SourceDevice}).TokenSource(); got != SourceDevice {
		t.Errorf("TokenSource() = %q, want %q", got, SourceDevice)
	}
	// An unrecognised value must not be mistaken for a refreshable session.
	if got := (Credentials{AppToken: "AT", AppTokenSource: "something-else"}).TokenSource(); got != SourceApp {
		t.Errorf("unknown source read as %q, want %q", got, SourceApp)
	}
}

func TestAppTokenExpiringWithin(t *testing.T) {
	soon := Credentials{AppTokenExpiresAt: time.Now().Add(30 * time.Second)}
	if !soon.AppTokenExpiringWithin(time.Minute) {
		t.Error("a token expiring in 30s should count as expiring within a minute")
	}
	if soon.AppTokenExpiringWithin(time.Second) {
		t.Error("a token expiring in 30s should not count as expiring within a second")
	}
	// Unknown expiry must never trigger a refresh on its own.
	if (Credentials{}).AppTokenExpiringWithin(time.Hour) {
		t.Error("an unknown expiry must not report as expiring")
	}
}

// Provenance and refresh fields must survive a round trip, and an older payload
// must still decode with them absent.
func TestDeviceSessionFieldsRoundTrip(t *testing.T) {
	want := Credentials{
		AppToken:          "AT",
		AppRefreshToken:   "RT",
		AppTokenSource:    SourceDevice,
		AppTokenExpiresAt: time.Unix(1800000000, 0).UTC(),
	}
	blob, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Credentials
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.CanRefresh() {
		t.Errorf("round trip lost refreshability: %+v", got)
	}
	if !got.AppTokenExpiresAt.Equal(want.AppTokenExpiresAt) {
		t.Errorf("expiry = %v, want %v", got.AppTokenExpiresAt, want.AppTokenExpiresAt)
	}

	legacy := []byte(`{"cookie":"c","app_token":"AT"}`)
	var old Credentials
	if err := json.Unmarshal(legacy, &old); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if old.CanRefresh() {
		t.Error("a legacy store must not be considered refreshable")
	}
}

func TestSessionForPicksTheLoginOfTheTargetSite(t *testing.T) {
	c := Credentials{Sites: map[string]SiteSession{
		"kino.watch":     {Cookie: "kp=1"},
		"kino.sorewa.ru": {Cookie: "pf=1"},
	}}
	tests := []struct {
		name, target, wantCookie, wantSite string
		ok                                 bool
	}{
		{"exact", "kino.watch", "kp=1", "kino.watch", true},
		{"another site entirely", "kino.sorewa.ru", "pf=1", "kino.sorewa.ru", true},
		{"case and port", "KINO.watch:8443", "kp=1", "kino.watch", true},
		{"subdomain belongs to the site", "www.kino.watch", "kp=1", "kino.watch", true},
		{"the parent does not", "sorewa.ru", "", "", false},
		{"lookalike", "evilkino.watch", "", "", false},
		{"site as a suffix of the target", "kino.watch.evil.example", "", "", false},
		{"unknown host", "evil.example", "", "", false},
		{"known host without a login", "kino.pub", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, site, ok := c.SessionFor(domain.Site{Host: tt.target})
			if ok != tt.ok || s.Cookie != tt.wantCookie || site != tt.wantSite {
				t.Fatalf("SessionFor(%q) = %q, %q, %v; want %q, %q, %v",
					tt.target, s.Cookie, site, ok, tt.wantCookie, tt.wantSite, tt.ok)
			}
		})
	}
}

// A login written before the site was recorded serves the hosts the service is
// known by, and nothing else — the leniency older files were created under.
func TestLegacyLoginServesKnownHostsOnly(t *testing.T) {
	c := Credentials{Cookie: "cf=1", UserAgent: "UA"}
	for _, host := range []string{"kino.watch", "kino.pub", "www.kino.pub"} {
		if s, _, ok := c.SessionFor(domain.Site{Host: host}); !ok || s.Cookie != "cf=1" {
			t.Errorf("legacy login withheld from %s", host)
		}
	}
	if _, _, ok := c.SessionFor(domain.Site{}); !ok {
		t.Error("legacy login withheld from the default site")
	}
	if _, _, ok := c.SessionFor(domain.Site{Host: "evil.example"}); ok {
		t.Error("legacy login sent to an unknown host")
	}
}

// Logins are per site: saving one leaves the others — and the app session —
// exactly as they were.
func TestSetSessionKeepsOtherSites(t *testing.T) {
	c := Credentials{Cookie: "kp=1", UserAgent: "UA", Site: "kino.watch", AppToken: "tok"}
	c.SetSession("kino.sorewa.ru", SiteSession{Cookie: "pf=1"})

	if s, _, ok := c.SessionFor(domain.Site{Host: "kino.watch"}); !ok || s.Cookie != "kp=1" {
		t.Fatalf("the kino.watch login was disturbed: %+v", c.Sites)
	}
	if s, _, ok := c.SessionFor(domain.Site{Host: "kino.sorewa.ru"}); !ok || s.Cookie != "pf=1" {
		t.Fatalf("the platform login was not stored: %+v", c.Sites)
	}
	if !c.HasAppToken() {
		t.Error("the app session was lost")
	}
	if c.Cookie != "kp=1" || c.Site != "kino.watch" {
		t.Errorf("legacy slot should mirror the kino.pub login, got %q for %q", c.Cookie, c.Site)
	}

	c.SetSession("kino.watch", SiteSession{Cookie: "kp=2"})
	if s, _, _ := c.SessionFor(domain.Site{Host: "kino.watch"}); s.Cookie != "kp=2" || c.Cookie != "kp=2" {
		t.Errorf("re-login should replace that site's login and its mirror, got %q / %q", s.Cookie, c.Cookie)
	}
	if s, _, _ := c.SessionFor(domain.Site{Host: "kino.sorewa.ru"}); s.Cookie != "pf=1" {
		t.Error("re-login on one site touched another")
	}
}

func TestRemoveSessionDropsOneSite(t *testing.T) {
	c := Credentials{}
	c.SetSession("kino.watch", SiteSession{Cookie: "kp=1"})
	c.SetSession("kino.sorewa.ru", SiteSession{Cookie: "pf=1"})

	if !c.RemoveSession("kino.watch") {
		t.Fatal("RemoveSession reported nothing to remove")
	}
	if c.HasCookieFor(domain.Site{Host: "kino.watch"}) || c.Cookie != "" {
		t.Errorf("kino.watch login (or its mirror) survived: %+v / %q", c.Sites, c.Cookie)
	}
	if !c.HasCookieFor(domain.Site{Host: "kino.sorewa.ru"}) {
		t.Error("the other site's login went with it")
	}
	if c.RemoveSession("nothing.example") {
		t.Error("RemoveSession reported a removal for a site without a login")
	}
}

func TestNormalizeFilesLegacyLoginUnderTheCurrentDomain(t *testing.T) {
	c := Credentials{Cookie: "cf=1", UserAgent: "UA"}
	c.normalize()
	if s, ok := c.Sites[domain.DefaultSiteHost]; !ok || s.Cookie != "cf=1" || s.UserAgent != "UA" {
		t.Fatalf("legacy login not filed under %s: %+v", domain.DefaultSiteHost, c.Sites)
	}
	if c.Site != domain.DefaultSiteHost || c.Cookie != "cf=1" {
		t.Errorf("mirror = %q for %q", c.Cookie, c.Site)
	}
}

// The on-disk shape keeps the kino.pub login in the legacy slot: an older build
// reads only that slot and must still find its cookie there.
func TestSerializedFormKeepsTheLegacySlotForOlderBuilds(t *testing.T) {
	c := Credentials{}
	c.SetSession("kino.sorewa.ru", SiteSession{Cookie: "pf=1"})
	c.SetSession("kino.watch", SiteSession{Cookie: "kp=1", UserAgent: "UA"})

	blob, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var older struct {
		Cookie string `json:"cookie"`
		Site   string `json:"site"`
	}
	if err := json.Unmarshal(blob, &older); err != nil {
		t.Fatal(err)
	}
	if older.Cookie != "kp=1" || older.Site != "kino.watch" {
		t.Errorf("an older build would read %q for %q, want the kino.watch login", older.Cookie, older.Site)
	}

	var back Credentials
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Sessions()) != 2 {
		t.Errorf("sessions after a round trip: %+v", back.Sessions())
	}
}

func TestPreferredMethodForIsPerSite(t *testing.T) {
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c := Credentials{AppToken: "tok", AppSavedAt: early}
	c.SetSession("kino.sorewa.ru", SiteSession{Cookie: "pf=1", SavedAt: late})

	if got := c.PreferredMethodFor(domain.Site{Host: "kino.watch"}); got != MethodApp {
		t.Errorf("kino.watch has no login of its own, want the app session, got %q", got)
	}
	if got := c.PreferredMethodFor(domain.Site{Host: "kino.sorewa.ru"}); got != MethodCookie {
		t.Errorf("the platform has a fresher login, got %q", got)
	}
}
