// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"strings"
	"testing"
)

// releaseBuild is what the release workflow stamps: a tag, an origin, a commit
// and the key its releases are signed with.
func releaseBuild() BuildInfo {
	return BuildInfo{
		Version:    "v1.2.3",
		Repo:       UpstreamRepo,
		Ref:        "refs/tags/v1.2.3",
		Commit:     "0123456789abcdef0123456789abcdef01234567",
		SigningKey: "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMDA=",
	}
}

func TestBuildInfo_ReleaseMayUpdate(t *testing.T) {
	ok, why := releaseBuild().SelfUpdateAllowed()
	if !ok {
		t.Errorf("a complete release build must be allowed to update: %s", why)
	}
	if !releaseBuild().IsRelease() {
		t.Error("IsRelease must agree with SelfUpdateAllowed")
	}
}

// Each of these is on its own sufficient to disqualify a build. The message is
// shown to the user, so it must name the actual reason.
func TestBuildInfo_RefusesSelfUpdate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*BuildInfo)
		mustSay string
	}{
		{"development build", func(b *BuildInfo) { b.Version = DevVersion }, "development build"},
		{"empty version", func(b *BuildInfo) { b.Version = "" }, "development build"},
		{"unparseable version", func(b *BuildInfo) { b.Version = "nightly-2026" }, "not a release version"},
		{"dirty tree", func(b *BuildInfo) { b.Dirty = true }, "uncommitted changes"},
		{"no origin", func(b *BuildInfo) { b.Repo = "" }, "no source repository"},
		{"built from a branch", func(b *BuildInfo) { b.Ref = "refs/heads/main" }, "rather than a release tag"},
		{"no ref at all", func(b *BuildInfo) { b.Ref = "" }, "rather than a release tag"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := releaseBuild()
			c.mutate(&b)

			ok, why := b.SelfUpdateAllowed()
			if ok {
				t.Fatalf("must refuse to self-update")
			}
			if !strings.Contains(why, c.mustSay) {
				t.Errorf("reason %q does not mention %q", why, c.mustSay)
			}
			if b.IsRelease() {
				t.Error("IsRelease must agree with SelfUpdateAllowed")
			}
		})
	}
}

// Signing is optional hardening, so a release built without a key still
// updates — over HTTPS from its own repository. Requiring one would block every
// fork on a setup step for a guarantee that a CI-held key does not really
// provide.
func TestBuildInfo_UnsignedReleaseStillUpdates(t *testing.T) {
	b := releaseBuild()
	b.SigningKey = ""

	ok, why := b.SelfUpdateAllowed()
	if !ok {
		t.Errorf("an unsigned release must still update: %s", why)
	}
	if b.RequiresSignature() {
		t.Error("a build with no key must not demand a signature")
	}
}

// The direction that matters: a build carrying a key always demands one, so a
// signature cannot be stripped to weaken the check.
func TestBuildInfo_RequiresSignature(t *testing.T) {
	if !releaseBuild().RequiresSignature() {
		t.Error("a build with a key must demand a signature")
	}
	for _, key := range []string{"", "   "} {
		b := releaseBuild()
		b.SigningKey = key
		if b.RequiresSignature() {
			t.Errorf("key %q must not count as configured", key)
		}
	}
}

// A fork building its own releases is a first-class case: its binaries must be
// allowed to update, from the fork's own repository.
func TestBuildInfo_ForkUpdatesFromItself(t *testing.T) {
	b := releaseBuild()
	b.Repo = "someone-else/kinopub-downloader"

	ok, why := b.SelfUpdateAllowed()
	if !ok {
		t.Fatalf("a fork's signed release must be allowed to update itself: %s", why)
	}
	if got := b.UpdateRepo(); got != "someone-else/kinopub-downloader" {
		t.Errorf("a fork must update from itself, got %q", got)
	}
}

// A build with no recorded origin still needs somewhere to point the user.
func TestBuildInfo_UpdateRepoFallsBackToUpstream(t *testing.T) {
	if got := (BuildInfo{}).UpdateRepo(); got != UpstreamRepo {
		t.Errorf("want %q, got %q", UpstreamRepo, got)
	}
}

func TestBuildInfo_Describe(t *testing.T) {
	t.Run("release", func(t *testing.T) {
		got := releaseBuild().Describe()
		if !strings.HasPrefix(got, "v1.2.3") {
			t.Errorf("want the version first, got %q", got)
		}
		// The commit is abbreviated rather than dumped in full.
		if !strings.Contains(got, "0123456789ab") || strings.Contains(got, "0123456789abcdef0123456789abcdef01234567") {
			t.Errorf("commit not abbreviated: %q", got)
		}
	})

	t.Run("dev", func(t *testing.T) {
		if got := (BuildInfo{Version: DevVersion}).Describe(); got != DevVersion {
			t.Errorf("want %q, got %q", DevVersion, got)
		}
	})

	t.Run("dirty is visible", func(t *testing.T) {
		b := releaseBuild()
		b.Dirty = true
		if !strings.Contains(b.Describe(), "dirty") {
			t.Errorf("a dirty build must say so: %q", b.Describe())
		}
	})

	// Knowing a binary came from somewhere else is the point of the field.
	t.Run("fork origin is visible", func(t *testing.T) {
		b := releaseBuild()
		b.Repo = "someone-else/kinopub-downloader"
		if !strings.Contains(b.Describe(), "someone-else/kinopub-downloader") {
			t.Errorf("a fork build must name its origin: %q", b.Describe())
		}
	})

	// Upstream is the default and would only be noise.
	t.Run("upstream origin is not repeated", func(t *testing.T) {
		if strings.Contains(releaseBuild().Describe(), UpstreamRepo) {
			t.Errorf("upstream origin should not be spelled out: %q", releaseBuild().Describe())
		}
	})
}
