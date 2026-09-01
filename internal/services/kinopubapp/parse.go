// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubapp

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Package is the Android application id of the official kino.pub client.
const Package = "com.kinopub"

// loginPrefsPath is the app-relative path of the SharedPreferences file that
// holds the OAuth session (access token, refresh token, expiry).
const loginPrefsPath = "shared_prefs/login.xml"

// Baseline values captured from kino.pub Android 1.34 (2026-09-01). They are
// non-secret build facts used as a fallback when the installed APK cannot be
// read, and as the reference the runtime-extracted values are compared against
// to detect that the app was updated.
//
// The OAuth client secret is deliberately absent: it is kino.pub's app secret,
// not ours, so it is only ever obtained by scanning the installed APK at
// runtime and is never committed here.
const (
	BaselineAppName       = "Android KinoPub"
	BaselineVersionName   = "1.34"
	BaselineExoVersion    = "2.11.8"
	BaselineOkHTTPVersion = "3.14.9"
	BaselineClientID      = "android"
)

// DefaultAPIBase is the JSON API base URL the client talks to. The service
// rotates domains; this is the one in use as of the baseline snapshot.
const DefaultAPIBase = "https://api.service-kp.com/v1"

// Token is the OAuth session persisted by the mobile app.
type Token struct {
	Access  string
	Refresh string
	// ExpiresAtMs is the app's stored expiry in Unix milliseconds. It is
	// advisory only: the server has been observed to keep accepting a token
	// past this instant, so the tool validates against the API rather than
	// trusting this field.
	ExpiresAtMs int64
}

var (
	reLoginString = regexp.MustCompile(`<string name="([^"]+)">([^<]*)</string>`)
	reLoginLong   = regexp.MustCompile(`<long name="([^"]+)" value="(-?\d+)"`)
)

// parseLoginXML extracts the session from the app's login.xml SharedPreferences
// document. Missing fields decode to their zero value; a document with no
// access token is reported as an error so callers do not proceed with an empty
// Bearer.
func parseLoginXML(data []byte) (Token, error) {
	var tok Token
	for _, m := range reLoginString.FindAllStringSubmatch(string(data), -1) {
		switch m[1] {
		case "token":
			tok.Access = m[2]
		case "refresh_token":
			tok.Refresh = m[2]
		}
	}
	for _, m := range reLoginLong.FindAllStringSubmatch(string(data), -1) {
		if m[1] == "expired" {
			tok.ExpiresAtMs, _ = strconv.ParseInt(m[2], 10, 64)
		}
	}
	if tok.Access == "" {
		return Token{}, fmt.Errorf("login.xml carries no access token")
	}
	return tok, nil
}

var reVersionName = regexp.MustCompile(`versionName=(\S+)`)

// parseVersionName pulls the versionName out of "pm dump <pkg>" output. It
// returns "" when absent so the caller can fall back to the baseline.
func parseVersionName(pmDump []byte) string {
	if m := reVersionName.FindSubmatch(pmDump); m != nil {
		return string(m[1])
	}
	return ""
}

// dexFacts are the build facts recovered from an APK's DEX bytecode.
type dexFacts struct {
	AppName       string // ExoPlayer UA application name literal
	ExoVersion    string // ExoPlayerLib version
	OkHTTPVersion string // bundled OkHttp version
	ClientID      string // OAuth client id
	ClientSecret  string // OAuth client secret (sensitive; never logged in full)
}

var (
	reExoVersion  = regexp.MustCompile(`\) ExoPlayerLib/([0-9]+\.[0-9]+\.[0-9]+)`)
	reOkHTTP      = regexp.MustCompile(`okhttp/([0-9]+\.[0-9]+\.[0-9]+)`)
	reOAuthClient = regexp.MustCompile(`client_id=([A-Za-z0-9_.\-]+)&client_secret=([A-Za-z0-9_.\-]+)`)
	reAppNameLit  = regexp.MustCompile(`Android KinoPub`)
)

// scanDEX recovers build facts from a single DEX image. String constants live
// in the DEX as contiguous ASCII, so a byte scan over the raw image finds them
// without decoding the class structure. Absent facts stay empty.
func scanDEX(dex []byte, f *dexFacts) {
	if f.AppName == "" && reAppNameLit.Match(dex) {
		f.AppName = BaselineAppName
	}
	if f.ExoVersion == "" {
		if m := reExoVersion.FindSubmatch(dex); m != nil {
			f.ExoVersion = string(m[1])
		}
	}
	if f.OkHTTPVersion == "" {
		if m := reOkHTTP.FindSubmatch(dex); m != nil {
			f.OkHTTPVersion = string(m[1])
		}
	}
	if f.ClientID == "" || f.ClientSecret == "" {
		if m := reOAuthClient.FindSubmatch(dex); m != nil {
			f.ClientID = string(m[1])
			f.ClientSecret = string(m[2])
		}
	}
}

// scanAPK reads the DEX images out of an APK (a ZIP) and merges the facts each
// yields. classes.dex is scanned before classes2.dex, classes3.dex, … so
// earlier images win ties.
func scanAPK(apk []byte) (dexFacts, error) {
	zr, err := zip.NewReader(bytes.NewReader(apk), int64(len(apk)))
	if err != nil {
		return dexFacts{}, fmt.Errorf("open apk: %w", err)
	}
	// Collect and order the DEX entries deterministically.
	var dexNames []string
	byName := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		base := path.Base(zf.Name)
		if strings.HasPrefix(base, "classes") && strings.HasSuffix(base, ".dex") {
			dexNames = append(dexNames, base)
			byName[base] = zf
		}
	}
	if len(dexNames) == 0 {
		return dexFacts{}, fmt.Errorf("apk contains no classes*.dex")
	}
	sortDexNames(dexNames)

	var f dexFacts
	for _, name := range dexNames {
		if f.complete() {
			break
		}
		rc, err := byName[name].Open()
		if err != nil {
			return dexFacts{}, fmt.Errorf("open %s: %w", name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return dexFacts{}, fmt.Errorf("read %s: %w", name, err)
		}
		scanDEX(data, &f)
	}
	return f, nil
}

// complete reports whether every fact has been recovered, so scanning can stop
// early rather than read every DEX in a multi-dex APK.
func (f dexFacts) complete() bool {
	return f.AppName != "" && f.ExoVersion != "" && f.OkHTTPVersion != "" &&
		f.ClientID != "" && f.ClientSecret != ""
}

// sortDexNames orders "classes.dex", "classes2.dex", "classes3.dex", … by the
// numeric suffix, with the unnumbered classes.dex first.
func sortDexNames(names []string) {
	rank := func(n string) int {
		mid := strings.TrimSuffix(strings.TrimPrefix(n, "classes"), ".dex")
		if mid == "" {
			return 1
		}
		if v, err := strconv.Atoi(mid); err == nil {
			return v
		}
		return 1 << 30
	}
	// Simple insertion sort keeps this dependency-free and the slice is tiny.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && rank(names[j]) < rank(names[j-1]); j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
}

// buildUserAgent assembles the ExoPlayer-style User-Agent the app sends,
// matching androidx.media Util.getUserAgent:
//
//	"<appName>/<versionName> (Linux;Android <osRelease>) ExoPlayerLib/<exoVersion>"
func buildUserAgent(appName, versionName, osRelease, exoVersion string) string {
	return fmt.Sprintf("%s/%s (Linux;Android %s) ExoPlayerLib/%s",
		appName, versionName, osRelease, exoVersion)
}
