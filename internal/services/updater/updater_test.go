// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/logx"
)

func testLogger() domain.Logger {
	return logx.New([]logx.Handler{logx.NewPlainHandler(io.Discard, domain.VerbosityQuiet, logx.NewCoordinator(io.Discard))})
}

// releaseServer stands in for GitHub: it serves one release, its assets, the
// checksums and (optionally) a signature over them.
type releaseServer struct {
	*httptest.Server
	assets map[string][]byte
	tag    string
	// commit is what the tag resolves to. Empty makes the lookup fail, the
	// way a rate-limited or misbehaving API would.
	commit string
}

// testCommit is the hash the stub's release tag points at.
const testCommit = "59f83e2466b7a76e75d3267712a6f5893304395f"

func newReleaseServer(t *testing.T, tag string, binary []byte, signWith ed25519.PrivateKey) *releaseServer {
	t.Helper()

	assetName, ok := domain.ReleaseAssetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Skipf("no release binary is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	sum := sha256.Sum256(binary)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	rs := &releaseServer{
		tag:    tag,
		commit: testCommit,
		assets: map[string][]byte{
			assetName:                 binary,
			domain.ChecksumsAssetName: []byte(checksums),
		},
	}
	if signWith != nil {
		sig := ed25519.Sign(signWith, []byte(checksums))
		rs.assets[SignatureAssetName] = []byte(base64.StdEncoding.EncodeToString(sig))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		// The commit lookup, as GitHub answers it under the SHA media type:
		// the bare hash, nothing else.
		if strings.HasSuffix(r.URL.Path, "/commits/refs/tags/"+rs.tag) {
			if rs.commit == "" || r.Header.Get("Accept") != "application/vnd.github.sha" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.github.sha; charset=utf-8")
			fmt.Fprint(w, rs.commit)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			http.NotFound(w, r)
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, `{"tag_name":%q,"html_url":"https://example.invalid/rel","assets":[`, rs.tag)
		first := true
		for name := range rs.assets {
			if !first {
				sb.WriteString(",")
			}
			first = false
			fmt.Fprintf(&sb, `{"name":%q,"browser_download_url":"%s/dl/%s"}`, name, rs.URL, name)
		}
		sb.WriteString(`]}`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, sb.String())
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := rs.assets[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	})

	// TLS, not plain HTTP: the updater refuses non-https asset URLs, and that
	// refusal is worth exercising rather than working around.
	rs.Server = httptest.NewTLSServer(mux)
	t.Cleanup(rs.Close)
	return rs
}

// fakeBinary is a script that identifies itself the way the real binary does,
// so the updater's sanity check passes without building anything.
func fakeBinary(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the sanity check runs the downloaded file; not portable to windows in tests")
	}
	return []byte("#!/bin/sh\necho 'kinopub v9.9.9'\n")
}

func releaseBuild(tag string) domain.BuildInfo {
	return domain.BuildInfo{
		Version: tag,
		Repo:    domain.UpstreamRepo,
		Ref:     "refs/tags/" + tag,
		Commit:  "0123456789abcdef",
	}
}

// newUpdater wires an Updater at the stub server, replacing a throwaway file
// rather than the test binary.
func newUpdater(t *testing.T, rs *releaseServer, build domain.BuildInfo, opts ...Option) (*Updater, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "kinopub")
	if err := os.WriteFile(target, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	base := []Option{
		WithAPIBase(rs.URL),
		WithExecPath(func() (string, error) { return target, nil }),
	}
	return New(rs.Client(), testLogger(), build, append(base, opts...)...), target
}

func TestCheck_ReportsNewerRelease(t *testing.T) {
	rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
	u, _ := newUpdater(t, rs, releaseBuild("v1.0.0"))

	rel, newer, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !newer {
		t.Error("v2.0.0 must be newer than v1.0.0")
	}
	if rel.Tag != "v2.0.0" {
		t.Errorf("tag: %q", rel.Tag)
	}
}

// The release payload only names the tag; the commit it points at is looked
// up separately so `update` can print it beside the running build's.
func TestCheck_ResolvesReleaseCommit(t *testing.T) {
	rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
	u, _ := newUpdater(t, rs, releaseBuild("v1.0.0"))

	rel, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Commit != testCommit {
		t.Errorf("commit: want %q, got %q", testCommit, rel.Commit)
	}
}

// The commit is a courtesy. Losing it — to a rate limit, an outage, a fork
// whose API differs — must not turn a successful check into a failure.
func TestCheck_SurvivesCommitLookupFailure(t *testing.T) {
	rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
	rs.commit = ""
	u, _ := newUpdater(t, rs, releaseBuild("v1.0.0"))

	rel, newer, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check must not fail over the commit lookup: %v", err)
	}
	if !newer || rel.Tag != "v2.0.0" {
		t.Errorf("the release itself must still be reported: newer=%v tag=%q", newer, rel.Tag)
	}
	if rel.Commit != "" {
		t.Errorf("commit must be empty when unresolved, got %q", rel.Commit)
	}
}

// Whatever comes back under the SHA media type is printed next to the running
// build's commit, so only an actual hash may get through.
func TestIsCommitHash(t *testing.T) {
	for _, ok := range []string{
		testCommit,
		strings.Repeat("a", 64), // SHA-256 repositories
	} {
		if !isCommitHash(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	for _, bad := range []string{
		"",
		"59f83e2466b7",                 // abbreviated
		strings.ToUpper(testCommit),    // GitHub never upper-cases
		testCommit[:39] + "g",          // not hex
		`{"message":"Not Found"}`,      // JSON error under the wrong media type
		"<!DOCTYPE html>" + testCommit, // an error page
	} {
		if isCommitHash(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestCheck_UpToDateAndAhead(t *testing.T) {
	rs := newReleaseServer(t, "v1.0.0", fakeBinary(t), nil)

	for _, current := range []string{"v1.0.0", "v1.1.0"} {
		u, _ := newUpdater(t, rs, releaseBuild(current))
		_, newer, err := u.Check(context.Background())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if newer {
			t.Errorf("current %s must not be considered older than v1.0.0", current)
		}
	}
}

// A development build has no place in the release ordering.
func TestCheck_DevBuildIsNeverNewer(t *testing.T) {
	rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
	u, _ := newUpdater(t, rs, domain.BuildInfo{Version: domain.DevVersion})

	rel, newer, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if newer {
		t.Error("a development build must not be told it is out of date")
	}
	if rel.Tag != "v2.0.0" {
		t.Errorf("the release should still be reported: %q", rel.Tag)
	}
}

func TestApply_ReplacesTheBinary(t *testing.T) {
	binary := fakeBinary(t)
	rs := newReleaseServer(t, "v2.0.0", binary, nil)
	u, target := newUpdater(t, rs, releaseBuild("v1.0.0"))

	rel, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if err := u.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binary) {
		t.Errorf("binary not replaced:\n%q", got)
	}
	// The download must not be left lying around next to the binary.
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Errorf("temporary download survived: %v", err)
	}
}

// The whole point of the gate: a build that is not a release must not replace
// itself even when a newer one exists.
func TestApply_RefusesForNonReleaseBuilds(t *testing.T) {
	cases := map[string]domain.BuildInfo{
		"development": {Version: domain.DevVersion},
		"no origin":   {Version: "v1.0.0", Ref: "refs/tags/v1.0.0"},
		"branch":      {Version: "v1.0.0", Repo: domain.UpstreamRepo, Ref: "refs/heads/main"},
		"dirty":       {Version: "v1.0.0", Repo: domain.UpstreamRepo, Ref: "refs/tags/v1.0.0", Dirty: true},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
			u, target := newUpdater(t, rs, build)

			rel, _, err := u.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			err = u.Apply(context.Background(), rel)
			if !errors.Is(err, ErrNotPermitted) {
				t.Fatalf("want ErrNotPermitted, got %v", err)
			}
			if got, _ := os.ReadFile(target); string(got) != "old binary" {
				t.Error("the binary was replaced despite the refusal")
			}
		})
	}
}

// A binary served with the wrong contents must never reach the disk, whether it
// was corrupted in transit or swapped deliberately.
func TestApply_RejectsChecksumMismatch(t *testing.T) {
	rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
	assetName, _ := domain.ReleaseAssetName(runtime.GOOS, runtime.GOARCH)
	rs.assets[assetName] = []byte("#!/bin/sh\necho 'kinopub v9.9.9'\n# tampered\n")

	u, target := newUpdater(t, rs, releaseBuild("v1.0.0"))
	rel, _, err := u.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	err = u.Apply(context.Background(), rel)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("want a checksum failure, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old binary" {
		t.Error("a mismatching binary was installed")
	}
}

func TestApply_SignedRelease(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub)

	t.Run("valid signature is accepted", func(t *testing.T) {
		binary := fakeBinary(t)
		rs := newReleaseServer(t, "v2.0.0", binary, priv)
		u, target := newUpdater(t, rs, releaseBuild("v1.0.0"), WithSigningKey(key))

		rel, _, err := u.Check(context.Background())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if err := u.Apply(context.Background(), rel); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if got, _ := os.ReadFile(target); string(got) != string(binary) {
			t.Error("a correctly signed release was not installed")
		}
	})

	t.Run("signature from another key is rejected", func(t *testing.T) {
		_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), otherPriv)
		u, target := newUpdater(t, rs, releaseBuild("v1.0.0"), WithSigningKey(key))

		rel, _, err := u.Check(context.Background())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if err := u.Apply(context.Background(), rel); err == nil {
			t.Fatal("a release signed by an unrelated key was accepted")
		}
		if got, _ := os.ReadFile(target); string(got) != "old binary" {
			t.Error("the binary was replaced despite a bad signature")
		}
	})

	// Removing the signature must not downgrade the check to "unsigned is fine".
	t.Run("missing signature is rejected", func(t *testing.T) {
		rs := newReleaseServer(t, "v2.0.0", fakeBinary(t), nil)
		u, target := newUpdater(t, rs, releaseBuild("v1.0.0"), WithSigningKey(key))

		rel, _, err := u.Check(context.Background())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		err = u.Apply(context.Background(), rel)
		if err == nil || !strings.Contains(err.Error(), "signed") {
			t.Fatalf("want a signature failure, got %v", err)
		}
		if got, _ := os.ReadFile(target); string(got) != "old binary" {
			t.Error("an unsigned release was installed by a key-carrying build")
		}
	})

	// Tampering with the checksums invalidates the signature over them.
	t.Run("tampered checksums are rejected", func(t *testing.T) {
		binary := fakeBinary(t)
		rs := newReleaseServer(t, "v2.0.0", binary, priv)
		assetName, _ := domain.ReleaseAssetName(runtime.GOOS, runtime.GOARCH)
		evil := sha256.Sum256([]byte("something else"))
		rs.assets[domain.ChecksumsAssetName] = []byte(
			fmt.Sprintf("%s  %s\n", hex.EncodeToString(evil[:]), assetName))

		u, target := newUpdater(t, rs, releaseBuild("v1.0.0"), WithSigningKey(key))
		rel, _, err := u.Check(context.Background())
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if err := u.Apply(context.Background(), rel); err == nil {
			t.Fatal("rewritten checksums were accepted")
		}
		if got, _ := os.ReadFile(target); string(got) != "old binary" {
			t.Error("the binary was replaced despite rewritten checksums")
		}
	})
}

// Asset URLs come out of the API response, so an attacker who can influence it
// must not be able to point the download at a host of their choosing.
func TestCheckAssetURL(t *testing.T) {
	u := New(http.DefaultClient, testLogger(), releaseBuild("v1.0.0"))

	for _, ok := range []string{
		"https://github.com/niazlv/kinopub-downloader/releases/download/v1/kinopub-linux-amd64",
		"https://objects.githubusercontent.com/whatever",
		"https://release-assets.githubusercontent.com/whatever",
	} {
		if err := u.checkAssetURL(ok); err != nil {
			t.Errorf("%s must be allowed: %v", ok, err)
		}
	}

	for _, bad := range []string{
		"https://evil.example/kinopub-linux-amd64",
		"http://github.com/x",                // plaintext
		"https://github.com.evil.example/x",  // suffix trickery
		"https://notgithubusercontent.com/x", // near-miss host
		"ftp://github.com/x",                 // wrong scheme
		"://malformed",
	} {
		if err := u.checkAssetURL(bad); err == nil {
			t.Errorf("%s must be rejected", bad)
		}
	}
}

// A fork's binary must query the fork, never upstream.
func TestUpdater_UsesBuildOrigin(t *testing.T) {
	build := releaseBuild("v1.0.0")
	build.Repo = "someone-else/kinopub-downloader"

	u := New(http.DefaultClient, testLogger(), build)
	if got := u.Repo(); got != "someone-else/kinopub-downloader" {
		t.Errorf("want the fork, got %q", got)
	}

	// A build with no recorded origin still needs somewhere to look.
	u = New(http.DefaultClient, testLogger(), domain.BuildInfo{Version: domain.DevVersion})
	if got := u.Repo(); got != domain.UpstreamRepo {
		t.Errorf("want %q, got %q", domain.UpstreamRepo, got)
	}
}
