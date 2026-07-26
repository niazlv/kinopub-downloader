// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// DevVersion is the version a binary reports when it was not built from a
// release tag. Self-update refuses to act on such a build unless forced: it has
// no way to tell whether the local tree is ahead of or behind the release.
const DevVersion = "dev"

// Version is a parsed semantic version. Pre-release identifiers are compared as
// a whole string, which is enough to order the "-rc1"/"-beta2" suffixes this
// project uses without implementing the full SemVer precedence rules.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // pre-release suffix without the leading '-'
}

// ParseVersion parses "v1.2.3", "1.2.3" or "v1.2.3-rc1". Build metadata after
// '+' is ignored, as SemVer requires it to be for precedence.
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	raw = strings.TrimPrefix(raw, "v")
	if i := strings.IndexByte(raw, '+'); i >= 0 {
		raw = raw[:i]
	}

	var pre string
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		pre = raw[i+1:]
		raw = raw[:i]
	}

	parts := strings.Split(raw, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, fmt.Errorf("malformed version %q", s)
	}

	var v Version
	dst := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("malformed version %q", s)
		}
		*dst[i] = n
	}
	v.Pre = pre
	return v, nil
}

// String renders the version in the "v1.2.3" form the release tags use.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare orders two versions: -1 if v precedes o, +1 if it follows, 0 if they
// are equal. A pre-release precedes the release it leads to, so v1.0.0-rc1 is
// older than v1.0.0.
func (v Version) Compare(o Version) int {
	for _, pair := range [][2]int{
		{v.Major, o.Major},
		{v.Minor, o.Minor},
		{v.Patch, o.Patch},
	} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}

	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "": // a release outranks any pre-release of it
		return 1
	case o.Pre == "":
		return -1
	case v.Pre < o.Pre:
		return -1
	default:
		return 1
	}
}

// IsDevBuild reports whether a version string denotes a local build rather than
// a published release.
func IsDevBuild(s string) bool {
	t := strings.TrimSpace(s)
	return t == "" || t == DevVersion
}

// releaseTargets lists the platforms the release workflow publishes binaries
// for. A build running anywhere else can still check for updates but cannot
// install one, and should say so rather than fetching a 404.
var releaseTargets = map[string]bool{
	"darwin/arm64":  true,
	"darwin/amd64":  true,
	"linux/amd64":   true,
	"linux/arm64":   true,
	"windows/amd64": true,
	"android/arm64": true,
}

// ReleaseAssetName returns the release asset holding the binary for a platform,
// matching the names the release workflow produces. ok is false when no binary
// is published for that platform.
func ReleaseAssetName(goos, goarch string) (name string, ok bool) {
	if !releaseTargets[goos+"/"+goarch] {
		return "", false
	}
	name = fmt.Sprintf("kinopub-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name, true
}

// ChecksumsAssetName is the release asset listing the SHA-256 of every binary.
const ChecksumsAssetName = "checksums.txt"

// ParseChecksums reads the output format of sha256sum: one "<hex>  <name>" per
// line. Lines that are blank or malformed are skipped, since the file may carry
// comments, but a hash of the wrong length is rejected — it would silently fail
// every comparison later.
func ParseChecksums(s string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		sum, name := strings.ToLower(fields[0]), fields[1]
		if len(sum) != 64 {
			continue
		}
		// sha256sum marks binary mode with a '*' before the name.
		out[strings.TrimPrefix(name, "*")] = sum
	}
	return out
}
