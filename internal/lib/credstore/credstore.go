// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package credstore provides encrypted storage for authentication credentials.
//
// Credentials (website logins per site, plus the kino.pub app session) are
// encrypted with AES-256-GCM using
// a key derived from a machine-specific secret. The encrypted file is stored at
// ~/.config/kinopub/credentials.enc
//
// Key derivation strategy (platform-dependent):
//   - macOS: uses the hardware UUID from IOPlatformExpertDevice (unique per Mac,
//     survives OS reinstalls, not exposed to other machines).
//   - Linux: uses /etc/machine-id (systemd machine identifier, unique per install).
//   - Termux/Android: uses $PREFIX/etc/machine-id or falls back to
//     /proc/sys/kernel/random/boot_id combined with the Android ID.
//
// The key is derived via PBKDF2-SHA256 with a fixed salt (the salt is not secret
// — the security comes from the machine-specific seed being unavailable on other
// devices). This means copying the .enc file to another machine won't help an
// attacker unless they also know the source machine's hardware UUID / machine-id.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// Credentials holds the authentication data persisted between runs.
//
// Website logins live in Sites, one per site: kino.pub is one site, a platform
// built on this tool is another with a login of its own, and a run sends only
// the login of the site its URL names — see SessionFor. Cookie, UserAgent and
// Site are the slot that held the single login before Sites existed. They
// still decode from older files and are kept as a mirror of the kino.pub login
// when writing, so an older build finds its cookie where it expects it; read
// them through the methods, never directly. Site is empty in files written
// before it was recorded; such a login is treated as belonging to the hosts
// the service is known by rather than refused.
type Credentials struct {
	Cookie    string `json:"cookie"`
	UserAgent string `json:"user_agent"`
	Site      string `json:"site,omitempty"`

	// Sites holds the website logins keyed by site host — see SiteKey.
	Sites map[string]SiteSession `json:"sites,omitempty"`

	// AppToken is the access token taken from the installed kino.pub mobile
	// app, used by --app runs. It is stored alongside the cookie rather than
	// instead of it: the two authorize different backends (the JSON API vs the
	// website) and a user may hold both. Empty when the user never ran
	// `login --app`.
	AppToken string `json:"app_token,omitempty"`

	// AppUserAgent is the User-Agent to send with the app token — the mobile
	// app's, not a browser's. It is kept separate from UserAgent (the browser
	// UA that pairs with Cookie) so holding both sessions does not make one
	// overwrite the other's User-Agent.
	AppUserAgent string `json:"app_user_agent,omitempty"`

	// APIBase is the API origin the token was validated against, so a run
	// reuses the same one without re-specifying it. Empty means the default.
	APIBase string `json:"api_base,omitempty"`

	// AppTokenSource records where AppToken came from, and with it whether this
	// tool may refresh the session. It is the safety switch behind the whole
	// refresh mechanism:
	//
	//   SourceApp    — imported from the installed mobile app. Refreshing would
	//                  rotate the token and sign the phone app out, so it is
	//                  never refreshed here; an expired one is re-imported.
	//   SourceDevice — obtained by this tool's own device/QR authorization. The
	//                  slot belongs to us, so refreshing is safe and automatic.
	//
	// Empty means a store written before this was recorded. Those could only
	// have come from an app import, so they are treated as SourceApp — the
	// conservative reading, since guessing "device" would risk rotating a token
	// the phone still depends on.
	AppTokenSource string `json:"app_token_source,omitempty"`

	// AppRefreshToken renews the session without another authorization. It is
	// only ever set for SourceDevice: an app import deliberately does not carry
	// the phone's refresh token, so there is nothing here to misuse.
	AppRefreshToken string `json:"app_refresh_token,omitempty"`

	// AppTokenExpiresAt is when the access token stops being valid, as stated
	// by the authorization server. Zero means unknown, in which case validity
	// is discovered from the API's answer rather than assumed.
	AppTokenExpiresAt time.Time `json:"app_token_expires_at,omitempty"`

	// AppClientID and AppClientSecret are the OAuth client credentials the
	// authorization endpoint requires, for both the device flow and refreshing.
	//
	// They are stored because they cannot always be re-derived where they are
	// needed: they come out of the installed Android APK, which a desktop has
	// no copy of. Keeping them lets `login --qr` and automatic refresh work on
	// a machine that has never seen the app — which is the point of the device
	// flow. The secret is kino.pub's, never this project's, so it is only ever
	// held here and in memory, and is redacted everywhere it could be printed.
	AppClientID     string `json:"app_client_id,omitempty"`
	AppClientSecret string `json:"app_client_secret,omitempty"`

	// CookieSavedAt and AppSavedAt record when each half was last stored, and
	// LastUsed/LastUsedAt which one last authorized a run. Together they let a
	// run with no explicit flag pick the credentials the user actually works
	// with, instead of always preferring one kind and failing on a stale
	// session while a fresh one sits unused. Absent in stores written before
	// this was tracked, which PreferredMethod treats as "no opinion".
	CookieSavedAt time.Time `json:"cookie_saved_at,omitempty"`
	AppSavedAt    time.Time `json:"app_saved_at,omitempty"`
	LastUsed      string    `json:"last_used,omitempty"` // MethodCookie or MethodApp
	LastUsedAt    time.Time `json:"last_used_at,omitempty"`
}

// The authentication methods a run can use.
const (
	MethodCookie = "cookie"
	MethodApp    = "app"
)

// Where an app-mode token came from. See Credentials.AppTokenSource.
const (
	// SourceApp: imported from the installed mobile app. Never refreshed here.
	SourceApp = "app"
	// SourceDevice: obtained by this tool's own device/QR authorization.
	SourceDevice = "device"
)

// TokenSource reports where the stored app token came from, normalising the
// pre-provenance case. A store written before the field existed can only hold
// an imported token, so it reads as SourceApp — never as something refreshable.
func (c Credentials) TokenSource() string {
	if c.AppTokenSource == SourceDevice {
		return SourceDevice
	}
	return SourceApp
}

// CanRefresh reports whether this tool may renew the session on its own.
//
// Only a session this tool authorized itself qualifies. Refreshing an imported
// app session would rotate the token and sign the phone out, so that case is
// excluded here rather than at each call site.
// It answers whether the session is *of a refreshable kind*; performing the
// refresh additionally needs OAuth client credentials, which may come from the
// store (HasClientCredentials) or from a flag. The two are kept apart so a
// caller can supply the secret at runtime without the store having one.
func (c Credentials) CanRefresh() bool {
	return c.HasAppToken() && c.TokenSource() == SourceDevice && c.AppRefreshToken != ""
}

// HasClientCredentials reports whether the stored OAuth client credentials are
// complete enough to call the authorization endpoint.
func (c Credentials) HasClientCredentials() bool {
	return c.AppClientID != "" && c.AppClientSecret != ""
}

// AppTokenExpiringWithin reports whether the access token is known to expire
// within d. It is false when the expiry is unknown, so an absent value never
// triggers a needless refresh — staleness is then discovered from the API.
func (c Credentials) AppTokenExpiringWithin(d time.Duration) bool {
	if c.AppTokenExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(d).After(c.AppTokenExpiresAt)
}

// PreferredMethod reports which stored credentials a bare kino.pub run should
// reach for — see PreferredMethodFor, evaluated for the default site.
func (c Credentials) PreferredMethod() string { return c.PreferredMethodFor(domain.Site{}) }

// PreferredMethodFor reports which stored credentials a run against the target
// site should reach for when the user named none: whichever was most recently
// saved or last worked. The website half is that site's own login — a platform
// login says nothing about how to reach kino.pub. It returns "" when nothing
// applies.
//
// With both stored and no timestamps at all — a store written before they were
// recorded — it answers MethodCookie, matching the behaviour those stores were
// created under.
func (c Credentials) PreferredMethodFor(target domain.Site) string {
	s, _, hasCookie := c.SessionFor(target)
	switch {
	case !hasCookie && !c.HasAppToken():
		return ""
	case !c.HasAppToken():
		return MethodCookie
	case !hasCookie:
		return MethodApp
	}
	if c.freshness(MethodApp, c.AppSavedAt).After(c.freshness(MethodCookie, s.SavedAt)) {
		return MethodApp
	}
	return MethodCookie
}

// freshness is the most recent moment a method was saved or successfully used.
func (c Credentials) freshness(method string, savedAt time.Time) time.Time {
	if c.LastUsed == method && c.LastUsedAt.After(savedAt) {
		return c.LastUsedAt
	}
	return savedAt
}

// IsEmpty reports whether the credentials carry no useful data.
func (c Credentials) IsEmpty() bool {
	return len(c.sessions()) == 0 && c.UserAgent == "" && c.AppToken == ""
}

// HasCookie reports whether a website login is stored for any site. It is
// distinct from IsEmpty because an `login --app` saves a token and the app's
// User-Agent but no cookie: a website run must treat that as "nothing saved"
// rather than adopt an Android User-Agent that matches no browser session.
// Whether one is stored for a particular site is HasCookieFor.
func (c Credentials) HasCookie() bool { return len(c.sessions()) > 0 }

// HasAppToken reports whether an app session is stored.
func (c Credentials) HasAppToken() bool { return c.AppToken != "" }

// credDir returns the directory where the credential file is stored.
func credDir() (string, error) {
	home, err := storeHome()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kinopub"), nil
}

// ConfigDir returns the per-user directory kinopub keeps its files in
// (~/.config/kinopub). It is exported so anything else stored per user — the
// plaintext preferences file, say — lands beside the credentials instead of
// deriving its own idea of the user's home, which under `sudo` or Termux's `su`
// is not $HOME. See storeHome for why.
func ConfigDir() (string, error) { return credDir() }

// ChownToStoreOwner hands a file written by root to the user the config
// directory belongs to, so a later unprivileged run can still read and rewrite
// it. Best-effort: a failure leaves the file owned by root, which surfaces as an
// ordinary permission error if it ever matters. A no-op off Linux and when not
// running as root.
func ChownToStoreOwner(path string) { chownToStoreOwner(path) }

// credPath returns the full path to the encrypted credential file.
func credPath() (string, error) {
	dir, err := credDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.enc"), nil
}

// pbkdf2Salt is a fixed application-specific salt. The security does not depend
// on this being secret — it prevents rainbow-table attacks against the
// machine-specific seed.
var pbkdf2Salt = []byte("kinopub-credstore-v1-salt-2024")

// deriveKey produces a 32-byte AES key from the machine-specific seed.
func deriveKey(seed []byte) []byte {
	return pbkdf2.Key(seed, pbkdf2Salt, 100_000, 32, sha256.New)
}

// Save encrypts and persists the given credentials.
func Save(creds Credentials) error {
	seed, err := machineSeed()
	if err != nil {
		return fmt.Errorf("machine seed: %w", err)
	}

	// One shape on disk: logins in Sites, the kino.pub one mirrored into the
	// legacy slot for older builds.
	creds.normalize()
	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	key := deriveKey(seed)
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	dir, err := credDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	path, err := credPath()
	if err != nil {
		return err
	}

	// Write with restrictive permissions (owner-only read/write).
	if err := os.WriteFile(path, ciphertext, 0600); err != nil {
		return fmt.Errorf("write credential file: %w", err)
	}

	// A store written while running as root (the documented way to read the
	// mobile app's session) belongs to the user who invoked us, not to root —
	// otherwise the next unprivileged run cannot read what it just saved.
	chownToStoreOwner(dir)
	chownToStoreOwner(path)

	return nil
}

// Load decrypts and returns the stored credentials.
// Returns empty Credentials (not an error) if the file does not exist.
func Load() (Credentials, error) {
	path, err := credPath()
	if err != nil {
		return Credentials{}, err
	}

	ciphertext, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, nil
		}
		return Credentials{}, fmt.Errorf("read credential file: %w", err)
	}

	seed, err := machineSeed()
	if err != nil {
		return Credentials{}, fmt.Errorf("machine seed: %w", err)
	}

	creds, err := decryptWith(seed, ciphertext)
	if err == nil {
		return creds, nil
	}

	// A store written by an older version was keyed on a seed this build no
	// longer prefers — on Android that was the boot id, which changes at every
	// reboot. Try those seeds before giving up, and migrate anything that opens
	// to the stable seed so the next reboot does not lose it again.
	for _, legacy := range legacySeeds() {
		if migrated, lerr := decryptWith(legacy, ciphertext); lerr == nil {
			if serr := Save(migrated); serr != nil {
				// Re-encryption is an optimization; the credentials are valid
				// either way, so report them rather than failing the run.
				return migrated, nil
			}
			return migrated, nil
		}
	}

	return Credentials{}, fmt.Errorf("decrypt credentials failed (wrong machine or corrupted file): %w", err)
}

// decryptWith opens a stored blob with the key derived from seed.
func decryptWith(seed, blob []byte) (Credentials, error) {
	key := deriveKey(seed)
	block, err := aes.NewCipher(key)
	if err != nil {
		return Credentials{}, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Credentials{}, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(blob) < nonceSize {
		return Credentials{}, fmt.Errorf("credential file is corrupted (too short)")
	}

	nonce, body := blob[:nonceSize], blob[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return Credentials{}, err
	}

	var creds Credentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return Credentials{}, fmt.Errorf("parse credentials: %w", err)
	}
	creds.normalize()
	return creds, nil
}

// Clear removes the stored credential file.
func Clear() error {
	path, err := credPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credential file: %w", err)
	}
	return nil
}
