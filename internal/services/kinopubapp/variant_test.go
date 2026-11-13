// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopubapp

import (
	"context"
	"fmt"
	"testing"
)

func TestVariantForPackage(t *testing.T) {
	cases := []struct {
		pkg     string
		wantPkg string
		wantOK  bool
	}{
		{"", VariantKinoPub.Package, true},   // unrecorded → original client
		{Package, Package, true},             // com.kinopub
		{kinoWatchPackage, kinoWatchPackage, true},
		{"org.someone.else", "", false},
	}
	for _, c := range cases {
		v, ok := VariantForPackage(c.pkg)
		if ok != c.wantOK || v.Package != c.wantPkg {
			t.Errorf("VariantForPackage(%q) = (%q, %v), want (%q, %v)",
				c.pkg, v.Package, ok, c.wantPkg, c.wantOK)
		}
	}
}

// loginXMLFor builds a minimal login.xml carrying a token and an expiry, the
// two fields ReadTokens orders by.
func loginXMLFor(token string, expiredMs int64) []byte {
	return []byte(fmt.Sprintf(
		`<map><string name="token">%s</string><long name="expired" value="%d" /></map>`,
		token, expiredMs))
}

func TestReadTokensOrdersNewestFirst(t *testing.T) {
	// Both clients installed and signed in; Kino.Watch holds the later expiry,
	// so it must be offered first regardless of Variants order (which lists
	// kino.pub first).
	r := &fakeReader{
		available: true,
		files: map[string][]byte{
			"app:com.kinopub:shared_prefs/login.xml": loginXMLFor("kpTok", 1_000),
			"app:watch.kino:shared_prefs/login.xml":  loginXMLFor("kwTok", 2_000),
		},
	}
	cands := ReadTokens(context.Background(), r, nil)
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2", len(cands))
	}
	if cands[0].Variant.Package != kinoWatchPackage || cands[0].Token.Access != "kwTok" {
		t.Errorf("first = %q/%q, want watch.kino/kwTok", cands[0].Variant.Package, cands[0].Token.Access)
	}
	if cands[1].Variant.Package != Package || cands[1].Token.Access != "kpTok" {
		t.Errorf("second = %q/%q, want com.kinopub/kpTok", cands[1].Variant.Package, cands[1].Token.Access)
	}
}

func TestReadTokensTieKeepsVariantsOrder(t *testing.T) {
	// Equal (here: unrecorded) expiry falls back to Variants order: kino.pub
	// first.
	r := &fakeReader{
		available: true,
		files: map[string][]byte{
			"app:com.kinopub:shared_prefs/login.xml": []byte(`<map><string name="token">kp</string></map>`),
			"app:watch.kino:shared_prefs/login.xml":  []byte(`<map><string name="token">kw</string></map>`),
		},
	}
	cands := ReadTokens(context.Background(), r, nil)
	if len(cands) != 2 || cands[0].Variant.Package != Package {
		t.Fatalf("tie order = %+v, want com.kinopub first", cands)
	}
}

func TestReadTokensSkipsUninstalled(t *testing.T) {
	// Only Kino.Watch is signed in; the kino.pub read fails and is skipped.
	r := &fakeReader{
		available: true,
		files: map[string][]byte{
			"app:watch.kino:shared_prefs/login.xml": loginXMLFor("kwTok", 500),
		},
	}
	cands := ReadTokens(context.Background(), r, nil)
	if len(cands) != 1 || cands[0].Variant.Package != kinoWatchPackage {
		t.Fatalf("got %+v, want only watch.kino", cands)
	}
}

func TestReadTokensNoReader(t *testing.T) {
	if got := ReadTokens(context.Background(), nil, nil); len(got) != 0 {
		t.Fatalf("got %d candidates with no reader, want 0", len(got))
	}
}

func TestFingerprintKinoWatchVariant(t *testing.T) {
	apkPath := "/data/app/~~x==/watch.kino-y==/base.apk"
	apk := buildAPK(t, map[string][]byte{
		"classes.dex": fakeDEX(kinoWatchAppName, kinoWatchExoVersion, kinoWatchOkHTTPVersion, "android", "sekret"),
	})
	r := &fakeReader{
		available: true,
		props:     map[string]string{"ro.build.version.release": "17"},
		apkPath:   apkPath,
		dump:      []byte("versionName=2.25\n"),
		files:     map[string][]byte{"path:" + apkPath: apk},
	}
	fp := NewForVariant(r, VariantKinoWatch, nopLogger()).Fingerprint(context.Background())

	if fp.Source != SourceExtracted {
		t.Errorf("Source = %v, want extracted", fp.Source)
	}
	want := "Android kino.watch/2.25 (Linux;Android 17) ExoPlayerLib/2.10.8"
	if fp.UserAgent != want {
		t.Errorf("UA = %q, want %q", fp.UserAgent, want)
	}
	if fp.APIBase != kinoWatchAPIBase {
		t.Errorf("APIBase = %q, want %q", fp.APIBase, kinoWatchAPIBase)
	}
	// The watch.kino APK matches the watch.kino baseline, so there is no drift.
	if d := fp.DriftFromBaseline(); len(d) != 0 {
		t.Errorf("unexpected drift vs Kino.Watch baseline: %+v", d)
	}
}

func TestFingerprintKinoWatchDriftVsOwnBaseline(t *testing.T) {
	// A newer Kino.Watch build drifts from the compiled-in watch baseline, not
	// from kino.pub's — proving the comparison is per-variant.
	apkPath := "/data/app/base.apk"
	apk := buildAPK(t, map[string][]byte{
		"classes.dex": fakeDEX(kinoWatchAppName, "2.11.0", kinoWatchOkHTTPVersion, "android", "s"),
	})
	r := &fakeReader{
		available: true,
		props:     map[string]string{"ro.build.version.release": "17"},
		apkPath:   apkPath,
		dump:      []byte("versionName=2.30\n"),
		files:     map[string][]byte{"path:" + apkPath: apk},
	}
	fp := NewForVariant(r, VariantKinoWatch, nopLogger()).Fingerprint(context.Background())
	got := map[string]string{}
	for _, d := range fp.DriftFromBaseline() {
		got[d.Field] = d.Extracted
	}
	if got["versionName"] != "2.30" || got["exoPlayerVersion"] != "2.11.0" {
		t.Errorf("drift = %+v, want versionName 2.30 & exo 2.11.0 vs watch baseline", got)
	}
}
