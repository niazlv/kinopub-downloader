// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/credstore"
)

// sessionExportSchema is the version of the export format.
//
// The stored file is encrypted with a machine-bound key and so cannot be
// copied; this envelope is the portable form. It carries its own version so a
// future field never has to be guessed at: a reader that does not understand
// the format says so instead of silently importing a partial session.
//
// Bump this only for a breaking change. Adding an optional field is backwards
// compatible and does not need a bump.
const sessionExportSchema = 1

// sessionExport is the portable envelope.
type sessionExport struct {
	// Schema is the envelope version — see sessionExportSchema.
	Schema int `json:"schema"`
	// ToolVersion records which build produced the file. It is advisory: import
	// warns on a mismatch rather than refusing, since the schema is what
	// actually governs compatibility.
	ToolVersion string `json:"tool_version"`
	// ExportedAt is when the file was written, for the operator's benefit.
	ExportedAt time.Time `json:"exported_at"`
	// Session is the credential payload.
	Session sessionPayload `json:"session"`
}

// sessionPayload mirrors the parts of credstore.Credentials that are worth
// moving between machines. It is a separate type on purpose: the on-disk store
// may grow machine-local fields that must not travel, and an explicit list
// makes each addition a deliberate decision.
type sessionPayload struct {
	// Site, Cookie and UserAgent carry the kino.pub website login, as the
	// only one the format had before Sites. They are still written so an
	// older build imports that login; a newer one reads Sites first.
	Site      string `json:"site,omitempty"`
	Cookie    string `json:"cookie,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`

	// Sites carries every website login, keyed by site host. Optional, so the
	// schema stays the same: a build that does not know the field ignores it.
	Sites map[string]sitePayload `json:"sites,omitempty"`

	AppToken          string    `json:"app_token,omitempty"`
	AppRefreshToken   string    `json:"app_refresh_token,omitempty"`
	AppTokenSource    string    `json:"app_token_source,omitempty"`
	AppTokenExpiresAt time.Time `json:"app_token_expires_at,omitempty"`
	AppUserAgent      string    `json:"app_user_agent,omitempty"`
	APIBase           string    `json:"api_base,omitempty"`

	AppClientID     string `json:"app_client_id,omitempty"`
	AppClientSecret string `json:"app_client_secret,omitempty"`
}

// sitePayload is one website login in the envelope.
type sitePayload struct {
	Cookie    string `json:"cookie"`
	UserAgent string `json:"user_agent,omitempty"`
}

// payloadFrom picks what travels. The app session always does; website logins
// only when asked for — they are tied to the browser that solved the
// Cloudflare challenge and to one site, so they are rarely useful elsewhere.
func payloadFrom(creds credstore.Credentials, includeCookie bool) sessionPayload {
	payload := sessionPayload{
		AppToken:          creds.AppToken,
		AppRefreshToken:   creds.AppRefreshToken,
		AppTokenSource:    creds.TokenSource(),
		AppTokenExpiresAt: creds.AppTokenExpiresAt,
		AppUserAgent:      creds.AppUserAgent,
		APIBase:           creds.APIBase,
		AppClientID:       creds.AppClientID,
		AppClientSecret:   creds.AppClientSecret,
	}
	if !includeCookie {
		return payload
	}
	for _, s := range creds.Sessions() {
		if payload.Sites == nil {
			payload.Sites = make(map[string]sitePayload)
		}
		payload.Sites[s.Site] = sitePayload{Cookie: s.Cookie, UserAgent: s.UserAgent}
	}
	if s, site, ok := creds.SessionFor(domain.Site{}); ok {
		payload.Site, payload.Cookie, payload.UserAgent = site, s.Cookie, s.UserAgent
	}
	return payload
}

// applyPayload merges an envelope into the credentials, as import does.
func applyPayload(creds *credstore.Credentials, s sessionPayload, now time.Time) {
	if s.AppToken != "" {
		creds.AppToken = s.AppToken
		creds.AppRefreshToken = s.AppRefreshToken
		creds.AppUserAgent = s.AppUserAgent
		creds.APIBase = s.APIBase
		creds.AppTokenExpiresAt = s.AppTokenExpiresAt
		// Provenance travels with the session: an imported phone session stays
		// non-refreshable on the new machine too, or renewing it there would
		// sign the phone out just the same.
		creds.AppTokenSource = s.AppTokenSource
		creds.AppSavedAt = now
	}
	if s.AppClientID != "" {
		creds.AppClientID = s.AppClientID
	}
	if s.AppClientSecret != "" {
		creds.AppClientSecret = s.AppClientSecret
	}
	// The legacy slot first, then Sites over it: a file from a newer build
	// carries the same login in both, and Sites is the authoritative copy.
	if s.Cookie != "" {
		creds.SetSession(s.Site, credstore.SiteSession{Cookie: s.Cookie, UserAgent: s.UserAgent, SavedAt: now})
	}
	for site, sp := range s.Sites {
		if sp.Cookie != "" {
			creds.SetSession(site, credstore.SiteSession{Cookie: sp.Cookie, UserAgent: sp.UserAgent, SavedAt: now})
		}
	}
}

// runSessionsExport writes the stored session to a portable file.
func runSessionsExport(args []string) int {
	fs := flag.NewFlagSet("kinopub sessions export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var out string
	var force, includeCookie bool
	fs.StringVar(&out, "out", "", "file to write (use - for stdout); required")
	fs.BoolVar(&force, "force", false, "overwrite the destination if it exists")
	fs.BoolVar(&includeCookie, "include-cookie", false, "also export the website logins, every site's (off by default: they are bound to one browser)")
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Export the stored session so another kinopub can import it.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub sessions export --out session.json"},
			command{name: "kinopub sessions export --out -", desc: "write to stdout"},
		)
		h.blank()
		h.text("The credential store is encrypted with a machine-bound key, so it cannot " +
			"simply be copied. This writes a portable, UNENCRYPTED file instead — treat it " +
			"like a password: it grants full access to the account.")
		h.blank()
		h.text("Typical use: authorize once on a rooted phone (%s), then move the session — "+
			"and the OAuth client secret that %s needs — to a desktop.",
			errStyle.Cyan("login --app"), errStyle.Cyan("login --qr"))
		h.section("Flags:")
		h.flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	if out == "" {
		errorf("--out is required: name the destination file, or - for stdout.")
		fs.Usage()
		return 1
	}

	creds, err := credstore.Load()
	if err != nil {
		errorf("could not read stored credentials: %v", err)
		return 1
	}
	if creds.IsEmpty() {
		errorf("nothing to export: no session is stored.")
		return 1
	}

	payload := payloadFrom(creds, includeCookie)

	blob, err := json.MarshalIndent(sessionExport{
		Schema:      sessionExportSchema,
		ToolVersion: version,
		ExportedAt:  time.Now().UTC(),
		Session:     payload,
	}, "", "  ")
	if err != nil {
		errorf("could not encode the session: %v", err)
		return 1
	}
	blob = append(blob, '\n')

	if out == "-" {
		if _, err := os.Stdout.Write(blob); err != nil {
			errorf("%v", err)
			return 1
		}
		warnf("this output contains credentials in clear text — do not store or share it casually.")
		return 0
	}

	if !force {
		if _, err := os.Stat(out); err == nil {
			errorf("%s already exists; pass --force to overwrite.", out)
			return 1
		}
	}
	// Owner-only: the file holds credentials that are no longer protected by
	// the machine-bound encryption of the store.
	if err := os.WriteFile(out, blob, 0600); err != nil {
		errorf("could not write %s: %v", out, err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s Session exported to %s (schema %d, %s).\n",
		errStyle.Green("✓"), errStyle.Cyan(out), sessionExportSchema, version)
	warnf("the file is NOT encrypted and grants full account access: move it over a trusted " +
		"channel and delete it afterwards.")
	if !includeCookie && creds.HasCookie() {
		notef("the website logins (%s) were not included; pass --include-cookie if you need them.",
			strings.Join(creds.SiteHosts(), ", "))
	}
	return 0
}

// runSessionsImport loads a portable session file into this machine's store.
func runSessionsImport(args []string) int {
	fs := flag.NewFlagSet("kinopub sessions import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var replace bool
	fs.BoolVar(&replace, "replace", false, "discard any existing session instead of merging into it")
	registerColorFlags(fs)
	fs.Usage = func() {
		h := newHelpPrinter(os.Stderr, errStyle)
		h.text("Import a session written by `kinopub sessions export`.")
		h.section("Usage:")
		h.commands(
			command{name: "kinopub sessions import session.json"},
			command{name: "kinopub sessions import -", desc: "read from stdin"},
		)
		h.blank()
		h.text("The session is re-encrypted with this machine's key on arrival, so the " +
			"portable file can be deleted afterwards.")
		h.section("Flags:")
		h.flags(fs)
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}

	paths := fs.Args()
	if len(paths) != 1 {
		errorf("exactly one file is required (or - for stdin).")
		fs.Usage()
		return 1
	}

	var blob []byte
	var err error
	if paths[0] == "-" {
		blob, err = readAllStdin()
	} else {
		blob, err = os.ReadFile(paths[0])
	}
	if err != nil {
		errorf("could not read the session file: %v", err)
		return 1
	}

	var env sessionExport
	if err := json.Unmarshal(blob, &env); err != nil {
		errorf("not a kinopub session export: %v", err)
		return 1
	}
	if err := checkExportSchema(env.Schema); err != nil {
		errorf("%v", err)
		return 1
	}
	if env.ToolVersion != "" && env.ToolVersion != version {
		notef("exported by kinopub %s; importing into %s (schema %d is compatible).",
			env.ToolVersion, version, env.Schema)
	}

	s := env.Session
	if s.AppToken == "" && s.Cookie == "" && len(s.Sites) == 0 {
		errorf("the file contains no session to import.")
		return 1
	}

	creds := credstore.Credentials{}
	if !replace {
		if existing, err := credstore.Load(); err == nil {
			creds = existing
		}
	}
	applyPayload(&creds, s, time.Now())

	if err := credstore.Save(creds); err != nil {
		errorf("%v", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "%s Session imported and re-encrypted for this machine.\n", errStyle.Green("✓"))
	if creds.HasAppToken() {
		if creds.CanRefresh() {
			notef("the app session renews itself here (it was obtained by this tool).")
		} else {
			notef("the app session came from the phone app, so it is never refreshed here; " +
				"re-import or use `login --qr` when it expires.")
		}
	}
	notef("verify it with `%s sessions --check`.", os.Args[0])
	return 0
}

// readAllStdin reads the whole of stdin, used for `-`. It reads the stream
// directly rather than opening os.Stdin.Name(), which is not a real path on
// every platform.
func readAllStdin() ([]byte, error) {
	return io.ReadAll(os.Stdin)
}

// checkExportSchema decides whether this build can import an envelope.
//
// An older or equal schema is fine — the format only ever gains optional
// fields. A newer one is refused rather than partially understood: fields this
// build cannot interpret could be exactly the ones that make the session work,
// and a half-imported session fails later in a confusing place.
func checkExportSchema(schema int) error {
	switch {
	case schema <= 0:
		return fmt.Errorf("not a kinopub session export: no schema version")
	case schema > sessionExportSchema:
		return fmt.Errorf("this file uses export schema %d, but this build understands up to %d — update kinopub first",
			schema, sessionExportSchema)
	default:
		return nil
	}
}
