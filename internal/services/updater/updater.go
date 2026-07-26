// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package updater checks GitHub Releases for a newer version and, when the
// running binary is entitled to, replaces itself with it.
//
// A binary updates from the repository it was built from, and nowhere else.
// That origin travels inside the binary as domain.BuildInfo, stamped by the
// release workflow, so each publisher has an independent update line: a fork's
// releases reach the fork's users, upstream's reach upstream's, and neither can
// reach the other's.
//
// The trust chain has two links, and both must hold:
//
//  1. The running build must be a release build with a recorded origin
//     (domain.BuildInfo). This decides whether a binary may replace itself at
//     all. Development builds never may.
//
//  2. The incoming binary must match the checksums the release publishes, and —
//     when the running build carries a signing key — those checksums must bear
//     that key's signature.
//
// Signing is optional hardening rather than a precondition, so a fork needs no
// setup at all to have working updates. A key stored in the publishing
// repository's own secrets would not withstand that repository being
// compromised anyway: the same access reads the secret or adds a step that
// signs anything. Where it helps is a swap of the release assets alone, and for
// that it is worth having.
//
// What is not optional is the direction of the check. A build that carries a
// key requires a valid signature and will not fall back to an unsigned release,
// so stripping the signature cannot downgrade anything. A build without one
// relies on HTTPS to its origin repository — the same trust anchor as
// `go install` or downloading a release by hand.
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/fsutil"
)

// SignatureAssetName is the release asset holding the detached signature over
// checksums.txt.
const SignatureAssetName = domain.ChecksumsAssetName + ".sig"

// maxAssetBytes bounds what the updater will read from a release asset. A
// compromised or malfunctioning endpoint should not be able to fill the disk.
const maxAssetBytes = 256 << 20 // 256 MiB

// ErrNotPermitted reports that this build may not replace itself. The message
// explains which of the conditions in the package doc was not met.
var ErrNotPermitted = errors.New("self-update is not permitted for this build")

// Updater checks for and installs new releases.
type Updater struct {
	client     *http.Client
	logger     domain.Logger
	build      domain.BuildInfo
	repo       string
	execPath   func() (string, error)
	apiBase    string
	assetHost  string
	signingKey string
}

// Option configures an Updater.
type Option func(*Updater)

// WithAPIBase overrides the GitHub API root. Tests point it at a local server.
func WithAPIBase(base string) Option {
	return func(u *Updater) {
		u.apiBase = strings.TrimSuffix(base, "/")
		// Assets of a self-hosted or stubbed API live on that same host, so
		// pinning follows the API rather than being hardcoded alongside it.
		if parsed, err := url.Parse(u.apiBase); err == nil {
			u.assetHost = strings.ToLower(parsed.Hostname())
		}
	}
}

// WithExecPath overrides how the running executable is located. Tests point it
// at a temporary file so a replacement can be exercised without touching the
// real binary.
func WithExecPath(fn func() (string, error)) Option {
	return func(u *Updater) { u.execPath = fn }
}

// WithSigningKey overrides the public key releases are verified against, given
// as base64. Only tests should use it: production verifies against the key
// stamped into the binary at build time.
func WithSigningKey(base64Key string) Option {
	return func(u *Updater) { u.signingKey = base64Key }
}

// New builds an Updater for the given build provenance.
func New(client *http.Client, logger domain.Logger, build domain.BuildInfo, opts ...Option) *Updater {
	u := &Updater{
		client:     client,
		logger:     logger,
		build:      build,
		repo:       build.UpdateRepo(),
		execPath:   os.Executable,
		apiBase:    "https://api.github.com",
		assetHost:  "github.com",
		signingKey: build.SigningKey,
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

// Release is a published release and the assets it carries.
type Release struct {
	Tag     string
	Version domain.Version
	PageURL string
	assets  map[string]string // asset name → download URL
}

// AssetURL returns the download URL of a named asset.
func (r *Release) AssetURL(name string) (string, bool) {
	u, ok := r.assets[name]
	return u, ok
}

// Repo is the repository this updater draws releases from.
func (u *Updater) Repo() string { return u.repo }

// CanSelfUpdate reports whether this build may replace itself, and why not.
func (u *Updater) CanSelfUpdate() (bool, string) {
	return u.build.SelfUpdateAllowed()
}

// Check fetches the latest release and reports whether it is newer than the
// running build.
//
// Checking is allowed from any build, including development ones: knowing a
// newer version exists is useful even where installing it is not permitted.
// newer is false for a development build, whose relation to any release is
// unknown.
func (u *Updater) Check(ctx context.Context) (rel *Release, newer bool, err error) {
	rel, err = u.latestRelease(ctx)
	if err != nil {
		return nil, false, err
	}
	if domain.IsDevBuild(u.build.Version) {
		return rel, false, nil
	}
	current, err := domain.ParseVersion(u.build.Version)
	if err != nil {
		return rel, false, nil
	}
	return rel, current.Compare(rel.Version) < 0, nil
}

// latestRelease queries the GitHub API for the most recent published release.
func (u *Updater) latestRelease(ctx context.Context) (*Release, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/releases/latest", u.apiBase, u.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("query latest release: unexpected status %s", resp.Status)
	}

	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}

	v, err := domain.ParseVersion(payload.TagName)
	if err != nil {
		return nil, fmt.Errorf("release tag %q: %w", payload.TagName, err)
	}

	rel := &Release{
		Tag:     payload.TagName,
		Version: v,
		PageURL: payload.HTMLURL,
		assets:  make(map[string]string, len(payload.Assets)),
	}
	for _, a := range payload.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, nil
}

// Apply downloads the release binary for this platform, verifies it, and
// replaces the running executable.
//
// Verification order matters: the signature over checksums.txt is checked
// before the checksums are trusted, and the binary's hash before it is ever
// made executable or run.
func (u *Updater) Apply(ctx context.Context, rel *Release) error {
	if ok, why := u.CanSelfUpdate(); !ok {
		return fmt.Errorf("%w: %s", ErrNotPermitted, why)
	}

	assetName, ok := domain.ReleaseAssetName(runtime.GOOS, runtime.GOARCH)
	if !ok {
		return fmt.Errorf("no release binary is published for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	assetURL, ok := rel.AssetURL(assetName)
	if !ok {
		return fmt.Errorf("release %s carries no asset %q", rel.Tag, assetName)
	}

	// 1. Checksums, and the signature proving they are the project's.
	sums, err := u.verifiedChecksums(ctx, rel)
	if err != nil {
		return err
	}
	wantSum, ok := sums[assetName]
	if !ok {
		return fmt.Errorf("release %s lists no checksum for %q", rel.Tag, assetName)
	}

	execPath, err := u.execPath()
	if err != nil {
		return fmt.Errorf("locate the running executable: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(execPath); rerr == nil {
		// Replace what the symlink points at, not the link itself, so a
		// /usr/local/bin/kinopub → /opt/... layout keeps working.
		execPath = resolved
	}

	// 2. Download beside the target, so the final step is a rename within one
	// filesystem and cannot half-copy over a working binary.
	tmpPath := execPath + ".new"
	if err := u.download(ctx, assetURL, tmpPath); err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	// 3. Verify before the file is ever made executable.
	gotSum, err := fileSHA256(tmpPath)
	if err != nil {
		return err
	}
	if gotSum != wantSum {
		return fmt.Errorf("downloaded binary does not match the signed checksum "+
			"(expected %s, got %s); refusing to install it", wantSum[:16], gotSum[:16])
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("make the new binary executable: %w", err)
	}

	// 4. A binary that cannot report its own version is not one to install over
	// a working copy — this catches a truncated download or a wrong-platform
	// asset before it becomes the only binary present.
	if err := u.sanityCheck(ctx, tmpPath); err != nil {
		return err
	}

	// 5. Swap it in.
	if err := replaceExecutable(tmpPath, execPath); err != nil {
		return err
	}

	u.logger.Info("updated",
		domain.F("from", u.build.Version),
		domain.F("to", rel.Tag),
		domain.F("path", execPath),
	)
	return nil
}

// verifiedChecksums downloads checksums.txt and, when this build carries a
// signing key, proves it genuine before returning anything parsed from it.
func (u *Updater) verifiedChecksums(ctx context.Context, rel *Release) (map[string]string, error) {
	sumsURL, ok := rel.AssetURL(domain.ChecksumsAssetName)
	if !ok {
		return nil, fmt.Errorf("release %s carries no %s, so the download cannot be checked",
			rel.Tag, domain.ChecksumsAssetName)
	}
	sumsBody, err := u.fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return nil, err
	}

	if err := u.verifySignature(ctx, rel, sumsBody); err != nil {
		return nil, err
	}

	sums := domain.ParseChecksums(string(sumsBody))
	if len(sums) == 0 {
		return nil, fmt.Errorf("release %s published an empty %s", rel.Tag, domain.ChecksumsAssetName)
	}
	return sums, nil
}

// verifySignature checks the detached signature over the checksums, if this
// build demands one.
//
// A build that carries a key never accepts an unsigned release: a missing
// signature asset is an error rather than a reason to proceed, or an attacker
// could downgrade the check simply by deleting it.
func (u *Updater) verifySignature(ctx context.Context, rel *Release, sums []byte) error {
	key := strings.TrimSpace(u.signingKey)
	if key == "" {
		u.logger.Debug("release signature not checked",
			domain.F("reason", "this build carries no signing key"),
			domain.F("release", rel.Tag),
		)
		return nil
	}

	pub, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("the release signing key stamped into this build is malformed")
	}

	sigURL, ok := rel.AssetURL(SignatureAssetName)
	if !ok {
		return fmt.Errorf("this build only installs signed releases, but %s carries no %s",
			rel.Tag, SignatureAssetName)
	}
	sigBody, err := u.fetch(ctx, sigURL, 4<<10)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigBody)))
	if err != nil {
		return fmt.Errorf("release signature is not valid base64: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), sums, sig) {
		return fmt.Errorf("release checksums are not signed by the key this build trusts; refusing to update")
	}
	return nil
}

// checkAssetURL rejects an asset URL that does not point at the release host.
//
// Asset URLs arrive inside the API response, so treating them as addresses to
// fetch from would let anything able to influence that response — a tampered
// reply, a future API change, a redirect service — hand the updater a binary
// from a host of its choosing. Since the whole point is that this file becomes
// the executable, the download host is pinned rather than trusted.
func (u *Updater) checkAssetURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("release asset URL is malformed: %w", err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("release asset URL is not https: %s", raw)
	}

	host := strings.ToLower(parsed.Hostname())
	if host == u.assetHost {
		return nil
	}
	// GitHub serves release assets from github.com and redirects them to its
	// object storage under githubusercontent.com.
	if host == "github.com" || host == "api.github.com" ||
		host == "githubusercontent.com" || strings.HasSuffix(host, ".githubusercontent.com") {
		return nil
	}
	return fmt.Errorf("release asset is hosted at %s, which is not a release host; refusing to download it", host)
}

// fetch reads a URL into memory, bounded by limit.
func (u *Updater) fetch(ctx context.Context, assetURL string, limit int64) ([]byte, error) {
	if err := u.checkAssetURL(assetURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", assetURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: unexpected status %s", assetURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// download streams a URL to path.
func (u *Updater) download(ctx context.Context, assetURL, path string) (err error) {
	if err := u.checkAssetURL(assetURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", assetURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", assetURL, resp.Status)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write next to the current binary (%s): %w — "+
				"reinstall manually or run with elevated privileges", filepath.Dir(path), err)
		}
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxAssetBytes)); err != nil {
		return fmt.Errorf("download %s: %w", assetURL, err)
	}
	return nil
}

// sanityCheck runs the freshly downloaded binary with --version.
func (u *Updater) sanityCheck(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("the downloaded binary does not run on this machine: %w", err)
	}
	if !strings.Contains(string(out), "kinopub") {
		return fmt.Errorf("the downloaded binary did not identify itself as kinopub")
	}
	return nil
}

// fileSHA256 returns the lowercase hex SHA-256 of a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceExecutable moves newPath over execPath.
//
// On Unix a running executable can be renamed over: the process keeps the old
// inode. Windows locks the image and refuses, so the current file is moved
// aside first and cleaned up on a later run.
func replaceExecutable(newPath, execPath string) error {
	if runtime.GOOS != "windows" {
		if err := fsutil.AtomicRename(newPath, execPath); err != nil {
			if os.IsPermission(err) {
				return fmt.Errorf("cannot replace %s: %w — reinstall manually or run with elevated privileges",
					execPath, err)
			}
			return fmt.Errorf("replace %s: %w", execPath, err)
		}
		return nil
	}

	backup := execPath + ".old"
	_ = os.Remove(backup) // left over from a previous update
	if err := os.Rename(execPath, backup); err != nil {
		return fmt.Errorf("move the current binary aside: %w", err)
	}
	if err := os.Rename(newPath, execPath); err != nil {
		// Put the working binary back rather than leaving nothing installed.
		if rerr := os.Rename(backup, execPath); rerr != nil {
			return fmt.Errorf("replace %s: %w (and the previous binary is left at %s)",
				execPath, err, backup)
		}
		return fmt.Errorf("replace %s: %w", execPath, err)
	}
	// Windows cannot delete the running image; the next run clears it.
	_ = os.Remove(backup)
	return nil
}
