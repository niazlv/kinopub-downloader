// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubapp

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// ---- pure parsers -----------------------------------------------------------

func TestParseLoginXML(t *testing.T) {
	// Shape mirrors a real login.xml: string token/refresh_token and a long
	// expiry, in arbitrary order.
	xml := []byte(`<?xml version='1.0' encoding='utf-8' standalone='yes' ?>
<map>
    <string name="refresh_token">ref123</string>
    <long name="expired" value="1766924430000" />
    <string name="token">acc456</string>
</map>`)
	tok, err := parseLoginXML(xml)
	if err != nil {
		t.Fatalf("parseLoginXML: %v", err)
	}
	if tok.Access != "acc456" || tok.Refresh != "ref123" || tok.ExpiresAtMs != 1766924430000 {
		t.Errorf("got %+v", tok)
	}
}

func TestParseLoginXMLNoTokenErrors(t *testing.T) {
	if _, err := parseLoginXML([]byte(`<map></map>`)); err == nil {
		t.Fatal("want error for a store with no access token")
	}
}

func TestParseVersionName(t *testing.T) {
	dump := []byte("    versionCode=1 minSdk=21 targetSdk=30\n    versionName=1.34\n")
	if got := parseVersionName(dump); got != "1.34" {
		t.Errorf("parseVersionName = %q, want 1.34", got)
	}
	if got := parseVersionName([]byte("no version here")); got != "" {
		t.Errorf("parseVersionName(absent) = %q, want empty", got)
	}
}

func TestBuildUserAgent(t *testing.T) {
	got := buildUserAgent("Android KinoPub", "1.34", "16", "2.11.8")
	want := "Android KinoPub/1.34 (Linux;Android 16) ExoPlayerLib/2.11.8"
	if got != want {
		t.Errorf("buildUserAgent = %q, want %q", got, want)
	}
}

// fakeDEX synthesizes a DEX-like blob carrying the string constants scanDEX
// looks for, so the scanner can be exercised without a real bytecode image.
func fakeDEX(appName, exo, okhttp, clientID, secret string) []byte {
	var b bytes.Buffer
	b.WriteString("dex\n035\x00")
	b.WriteString("some/class/Path;")
	if appName != "" {
		b.WriteString(appName)
	}
	b.WriteString("\x00padding\x00")
	if exo != "" {
		b.WriteString(") ExoPlayerLib/" + exo)
	}
	b.WriteString("\x00")
	if okhttp != "" {
		b.WriteString("okhttp/" + okhttp)
	}
	b.WriteString("\x00")
	if clientID != "" {
		b.WriteString("api/oauth2/device?grant_type=refresh_token&client_id=" + clientID + "&client_secret=" + secret)
	}
	return b.Bytes()
}

func buildAPK(t *testing.T, dexByName map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// Include a non-DEX entry to ensure it is ignored.
	if w, err := zw.Create("AndroidManifest.xml"); err == nil {
		w.Write([]byte("binary-manifest"))
	}
	for name, data := range dexByName {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func TestScanAPKFullFacts(t *testing.T) {
	apk := buildAPK(t, map[string][]byte{
		"classes.dex": fakeDEX("Android KinoPub", "2.11.8", "3.14.9", "android", "topsecret"),
	})
	f, err := scanAPK(apk)
	if err != nil {
		t.Fatalf("scanAPK: %v", err)
	}
	if f.AppName != "Android KinoPub" || f.ExoVersion != "2.11.8" || f.OkHTTPVersion != "3.14.9" {
		t.Errorf("facts = %+v", f)
	}
	if f.ClientID != "android" || f.ClientSecret != "topsecret" {
		t.Errorf("oauth = %q / %q", f.ClientID, f.ClientSecret)
	}
	if !f.complete() {
		t.Error("complete() = false")
	}
}

func TestScanAPKMultiDexEarlierWins(t *testing.T) {
	// classes.dex has the app name; classes2.dex has the versions. Merging
	// across images must recover all of them.
	apk := buildAPK(t, map[string][]byte{
		"classes2.dex": fakeDEX("", "2.11.8", "3.14.9", "android", "s"),
		"classes.dex":  fakeDEX("Android KinoPub", "", "", "", ""),
	})
	f, err := scanAPK(apk)
	if err != nil {
		t.Fatalf("scanAPK: %v", err)
	}
	if f.AppName != "Android KinoPub" || f.ExoVersion != "2.11.8" || f.ClientID != "android" {
		t.Errorf("merged facts = %+v", f)
	}
}

func TestScanAPKNoDex(t *testing.T) {
	apk := buildAPK(t, nil)
	if _, err := scanAPK(apk); err == nil {
		t.Fatal("want error when APK has no classes*.dex")
	}
}

// ---- Reader-backed App ------------------------------------------------------

type fakeReader struct {
	available bool
	files     map[string][]byte // key: "app:<rel>" or "path:<abs>"
	props     map[string]string
	apkPath   string
	dump      []byte
	failAPK   bool
}

func (r *fakeReader) Available(context.Context) bool { return r.available }

func (r *fakeReader) ReadAppFile(_ context.Context, pkg, rel string) ([]byte, error) {
	if b, ok := r.files["app:"+rel]; ok {
		return b, nil
	}
	return nil, errors.New("no such app file")
}

func (r *fakeReader) ReadFile(_ context.Context, path string) ([]byte, error) {
	if r.failAPK {
		return nil, errors.New("read denied")
	}
	if b, ok := r.files["path:"+path]; ok {
		return b, nil
	}
	return nil, errors.New("no such file")
}

func (r *fakeReader) Getprop(_ context.Context, key string) (string, error) {
	if v, ok := r.props[key]; ok {
		return v, nil
	}
	return "", nil
}

func (r *fakeReader) PackageAPKPath(context.Context, string) (string, error) {
	if r.apkPath == "" {
		return "", errors.New("not installed")
	}
	return r.apkPath, nil
}

func (r *fakeReader) PackageDump(context.Context, string) ([]byte, error) {
	if r.dump == nil {
		return nil, errors.New("no dump")
	}
	return r.dump, nil
}

func nopLogger() domain.Logger { return nil }

func TestReadTokenFromApp(t *testing.T) {
	r := &fakeReader{
		available: true,
		files: map[string][]byte{
			"app:shared_prefs/login.xml": []byte(`<map><string name="token">abc</string></map>`),
		},
	}
	app := New(r, nopLogger())
	tok, err := app.ReadToken(context.Background())
	if err != nil {
		t.Fatalf("ReadToken: %v", err)
	}
	if tok.Access != "abc" {
		t.Errorf("token = %q", tok.Access)
	}
}

func TestReadTokenNoRoot(t *testing.T) {
	app := New(nil, nopLogger())
	if _, err := app.ReadToken(context.Background()); !errors.Is(err, domain.ErrAPITokenUnavailable) {
		t.Fatalf("err = %v, want ErrAPITokenUnavailable", err)
	}
}

func TestFingerprintFullExtraction(t *testing.T) {
	apkPath := "/data/app/~~x==/com.kinopub-y==/base.apk"
	apk := buildAPK(t, map[string][]byte{
		"classes.dex": fakeDEX("Android KinoPub", "2.11.8", "3.14.9", "android", "topsecret"),
	})
	r := &fakeReader{
		available: true,
		props:     map[string]string{"ro.build.version.release": "16"},
		apkPath:   apkPath,
		dump:      []byte("versionName=1.34\n"),
		files:     map[string][]byte{"path:" + apkPath: apk},
	}
	fp := New(r, nopLogger()).Fingerprint(context.Background())

	if fp.Source != SourceExtracted {
		t.Errorf("Source = %v, want extracted", fp.Source)
	}
	want := "Android KinoPub/1.34 (Linux;Android 16) ExoPlayerLib/2.11.8"
	if fp.UserAgent != want {
		t.Errorf("UA = %q, want %q", fp.UserAgent, want)
	}
	if fp.ClientSecret != "topsecret" {
		t.Errorf("secret = %q", fp.ClientSecret)
	}
	if len(fp.DriftFromBaseline()) != 0 {
		t.Errorf("unexpected drift vs baseline: %+v", fp.DriftFromBaseline())
	}
}

func TestFingerprintNoRootFallsBackToBaseline(t *testing.T) {
	app := New(nil, nopLogger())
	fp := app.Fingerprint(context.Background())
	if fp.Source != SourceBaseline {
		t.Errorf("Source = %v, want baseline", fp.Source)
	}
	// Still a usable, app-shaped UA (OS release unknown → empty slot).
	if !strings.HasPrefix(fp.UserAgent, "Android KinoPub/1.34 (Linux;Android ") {
		t.Errorf("baseline UA = %q", fp.UserAgent)
	}
	if fp.HasClientSecret() {
		t.Error("baseline must not carry a client secret")
	}
}

func TestFingerprintDriftDetected(t *testing.T) {
	apkPath := "/data/app/base.apk"
	apk := buildAPK(t, map[string][]byte{
		// App updated to 1.40 with a newer ExoPlayer.
		"classes.dex": fakeDEX("Android KinoPub", "2.12.0", "3.14.9", "android", "s"),
	})
	r := &fakeReader{
		available: true,
		props:     map[string]string{"ro.build.version.release": "16"},
		apkPath:   apkPath,
		dump:      []byte("versionName=1.40\n"),
		files:     map[string][]byte{"path:" + apkPath: apk},
	}
	fp := New(r, nopLogger()).Fingerprint(context.Background())
	drift := fp.DriftFromBaseline()
	got := map[string]string{}
	for _, d := range drift {
		got[d.Field] = d.Extracted
	}
	if got["versionName"] != "1.40" || got["exoPlayerVersion"] != "2.12.0" {
		t.Errorf("drift = %+v", drift)
	}
}

func TestFingerprintMixedWhenAPKUnreadable(t *testing.T) {
	r := &fakeReader{
		available: true,
		props:     map[string]string{"ro.build.version.release": "16"},
		apkPath:   "/data/app/base.apk",
		dump:      []byte("versionName=1.34\n"),
		failAPK:   true, // root present, but APK read denied
	}
	fp := New(r, nopLogger()).Fingerprint(context.Background())
	if fp.Source != SourceBaseline {
		// versionName came from pm dump but no APK facts → still baseline for
		// the classified (APK-derived) signal.
		t.Errorf("Source = %v, want baseline (no APK facts extracted)", fp.Source)
	}
	if fp.OSRelease != "16" {
		t.Errorf("OSRelease = %q, want 16", fp.OSRelease)
	}
}

func TestRedactedMasksSecret(t *testing.T) {
	fp := Fingerprint{ClientSecret: "topsecret"}
	red := fp.Redacted()
	if strings.Contains(red.ClientSecret, "topsecret") {
		t.Errorf("secret leaked in redacted form: %q", red.ClientSecret)
	}
	if red.ClientSecret == "" {
		t.Error("redacted secret should still indicate presence")
	}
}
