// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"fmt"
	"strings"
)

// UpstreamRepo is where the project is published, in "owner/name" form. It is
// only used to point users at downloads when a binary carries no origin of its
// own — a development build. It is deliberately not a permission check: see
// BuildInfo.
const UpstreamRepo = "niazlv/kinopub-downloader"

// BuildInfo records where a binary came from. The release workflow stamps every
// field through -ldflags; an ordinary `go build` leaves them empty, which is how
// a development build is recognized.
//
// Origin, not identity, is what governs self-update. A binary updates from the
// repository it was built from, accepting only releases signed by the key it was
// built with. Both travel inside the binary, so:
//
//   - Uploading an asset to a release does not make anyone install it: without a
//     signature from the matching private key it is rejected.
//   - A fork that builds its own signed releases updates its own users, from its
//     own repository — never from upstream, and never upstream's users.
//   - A locally built binary belongs to no release line and refuses to replace
//     itself at all.
//
// These fields are self-reported, and whoever passes -ldflags can set them to
// anything. That is not a weakness: forging them only affects the binary being
// built, and running someone else's binary already trusts them completely. What
// the fields prevent is one publisher's release stream reaching another's users.
type BuildInfo struct {
	// Version is the release tag, e.g. "v1.2.3". DevVersion for local builds.
	Version string
	// Repo is the "owner/name" whose releases this binary updates from.
	Repo string
	// Ref is the git ref built, e.g. "refs/tags/v1.2.3".
	Ref string
	// Commit is the commit built.
	Commit string
	// SigningKey is the base64 ed25519 public key whose signature a release must
	// carry to be installed. It is optional hardening, not a requirement:
	//
	// A key kept in the publishing repository's own secrets adds little, because
	// whoever can compromise the repository can also read the secret or add a
	// workflow step that signs anything. What it does buy is protection against
	// the release assets being swapped while the repository itself is intact —
	// so it is worth having, but not worth blocking every fork on.
	//
	// When present it is mandatory: a build that carries a key refuses any
	// release not signed by it, so an attacker cannot strip the signature to
	// downgrade the check. When absent, HTTPS to the origin repository is the
	// trust anchor, the same one `go install` and a manual download already rely
	// on.
	SigningKey string
	// Dirty reports uncommitted changes at build time. A release is always built
	// from a clean checkout, so only local tooling ever sets it.
	Dirty bool
}

// IsRelease reports whether the binary was produced by a release workflow from
// a version tag, with everything self-update needs.
func (b BuildInfo) IsRelease() bool { return b.selfUpdateRefusal() == "" }

// SelfUpdateAllowed reports whether this build may replace itself and, when it
// may not, why — phrased for the user rather than for a log.
func (b BuildInfo) SelfUpdateAllowed() (bool, string) {
	if reason := b.selfUpdateRefusal(); reason != "" {
		return false, reason
	}
	return true, ""
}

// selfUpdateRefusal returns why this build may not update itself, or "".
func (b BuildInfo) selfUpdateRefusal() string {
	if IsDevBuild(b.Version) {
		return "this is a development build, not a release"
	}
	if _, err := ParseVersion(b.Version); err != nil {
		return fmt.Sprintf("build version %q is not a release version", b.Version)
	}
	if b.Dirty {
		return "this build was made from a working tree with uncommitted changes"
	}
	if b.Repo == "" {
		return "this build records no source repository, so it was not produced by a release workflow"
	}
	if !strings.HasPrefix(b.Ref, "refs/tags/") {
		return fmt.Sprintf("this build came from %q rather than a release tag", b.Ref)
	}
	return ""
}

// RequiresSignature reports whether this build will only install a release
// signed by its stamped key. See SigningKey for why this is opt-in.
func (b BuildInfo) RequiresSignature() bool {
	return strings.TrimSpace(b.SigningKey) != ""
}

// UpdateRepo is the repository this binary updates from: the one it was built
// from, falling back to upstream only for messaging in builds that have none.
func (b BuildInfo) UpdateRepo() string {
	if b.Repo != "" {
		return b.Repo
	}
	return UpstreamRepo
}

// ShortCommit abbreviates a commit hash for display, the way `git log
// --abbrev=12` would. Twelve hex digits are unique in any repository this
// project will ever be, and are what `--version` and `update` both print, so
// the two can be compared by eye.
func ShortCommit(commit string) string {
	commit = strings.TrimSpace(commit)
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// Describe renders the build's provenance for `--version`.
func (b BuildInfo) Describe() string {
	var sb strings.Builder
	sb.WriteString(b.Version)
	if b.Commit != "" {
		fmt.Fprintf(&sb, " (%s", ShortCommit(b.Commit))
		if b.Dirty {
			sb.WriteString(", dirty")
		}
		sb.WriteString(")")
	} else if b.Dirty {
		sb.WriteString(" (dirty)")
	}
	if b.Repo != "" && !strings.EqualFold(b.Repo, UpstreamRepo) {
		fmt.Fprintf(&sb, " built from %s", b.Repo)
	}
	return sb.String()
}
