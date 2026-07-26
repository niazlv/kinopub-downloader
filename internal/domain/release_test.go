// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"v1.2.3":       {Major: 1, Minor: 2, Patch: 3},
		"1.2.3":        {Major: 1, Minor: 2, Patch: 3},
		"v0.1.3":       {Minor: 1, Patch: 3},
		"v1.2":         {Major: 1, Minor: 2},
		"v2":           {Major: 2},
		"v1.2.3-rc1":   {Major: 1, Minor: 2, Patch: 3, Pre: "rc1"},
		"v1.2.3+build": {Major: 1, Minor: 2, Patch: 3}, // build metadata is ignored
		"  v1.2.3  ":   {Major: 1, Minor: 2, Patch: 3},
	}
	for in, want := range cases {
		got, err := ParseVersion(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: want %+v, got %+v", in, want, got)
		}
	}
}

func TestParseVersion_Rejects(t *testing.T) {
	for _, in := range []string{"", "  ", "abc", "v1.x.3", "v1.2.3.4", "v-1.2.3"} {
		if _, err := ParseVersion(in); err == nil {
			t.Errorf("%q: want an error, got nil", in)
		}
	}
}

func TestVersion_Compare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v1.0.1", -1},
		{"v1.0.1", "v1.0.0", 1},
		{"v1.0.0", "v1.1.0", -1},
		{"v1.0.0", "v2.0.0", -1},
		{"v2.0.0", "v1.9.9", 1},
		{"v0.1.3", "v0.2.0", -1},
		// A pre-release precedes the release it leads to.
		{"v1.0.0-rc1", "v1.0.0", -1},
		{"v1.0.0", "v1.0.0-rc1", 1},
		{"v1.0.0-rc1", "v1.0.0-rc2", -1},
		{"v1.0.0-rc1", "v1.0.0-rc1", 0},
		// Ordering across components still wins over pre-release.
		{"v1.0.0", "v1.0.1-rc1", -1},
	}
	for _, c := range cases {
		a, err := ParseVersion(c.a)
		if err != nil {
			t.Fatalf("%q: %v", c.a, err)
		}
		b, err := ParseVersion(c.b)
		if err != nil {
			t.Fatalf("%q: %v", c.b, err)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("%s vs %s: want %d, got %d", c.a, c.b, c.want, got)
		}
	}
}

// Compare must be a consistent ordering: reversing the operands negates it.
func TestVersion_CompareIsAntisymmetric(t *testing.T) {
	vs := []string{"v0.1.3", "v1.0.0-rc1", "v1.0.0", "v1.0.1", "v2.0.0"}
	for _, x := range vs {
		for _, y := range vs {
			a, _ := ParseVersion(x)
			b, _ := ParseVersion(y)
			if got, rev := a.Compare(b), b.Compare(a); got != -rev {
				t.Errorf("%s vs %s: %d but reversed %d", x, y, got, rev)
			}
		}
	}
}

func TestVersion_String(t *testing.T) {
	cases := map[string]string{
		"v1.2.3":     "v1.2.3",
		"1.2.3":      "v1.2.3",
		"v1.2.3-rc1": "v1.2.3-rc1",
		"v2":         "v2.0.0",
	}
	for in, want := range cases {
		v, err := ParseVersion(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got := v.String(); got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}
}

func TestIsDevBuild(t *testing.T) {
	for _, in := range []string{"", "  ", "dev"} {
		if !IsDevBuild(in) {
			t.Errorf("%q must count as a development build", in)
		}
	}
	for _, in := range []string{"v1.2.3", "1.0.0"} {
		if IsDevBuild(in) {
			t.Errorf("%q must not count as a development build", in)
		}
	}
}

// The names must match what the release workflow publishes; a mismatch turns
// every update into a 404.
func TestReleaseAssetName(t *testing.T) {
	cases := map[[2]string]string{
		{"darwin", "arm64"}:  "kinopub-darwin-arm64",
		{"darwin", "amd64"}:  "kinopub-darwin-amd64",
		{"linux", "amd64"}:   "kinopub-linux-amd64",
		{"linux", "arm64"}:   "kinopub-linux-arm64",
		{"windows", "amd64"}: "kinopub-windows-amd64.exe",
		{"android", "arm64"}: "kinopub-android-arm64",
	}
	for platform, want := range cases {
		got, ok := ReleaseAssetName(platform[0], platform[1])
		if !ok {
			t.Errorf("%s/%s: reported unsupported", platform[0], platform[1])
			continue
		}
		if got != want {
			t.Errorf("%s/%s: want %q, got %q", platform[0], platform[1], want, got)
		}
	}
}

// Platforms with no published binary must say so rather than yield a name that
// will 404.
func TestReleaseAssetName_UnsupportedPlatforms(t *testing.T) {
	for _, p := range [][2]string{
		{"linux", "386"},
		{"windows", "arm64"},
		{"freebsd", "amd64"},
		{"darwin", "386"},
		{"", ""},
	} {
		if name, ok := ReleaseAssetName(p[0], p[1]); ok {
			t.Errorf("%s/%s: want unsupported, got %q", p[0], p[1], name)
		}
	}
}

func TestParseChecksums(t *testing.T) {
	const in = `
d1a5f4d3b2c1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5  kinopub-linux-amd64
A1B5F4D3B2C1E0F9A8B7C6D5E4F3A2B1C0D9E8F7A6B5C4D3E2F1A0B9C8D7E6F5 *kinopub-darwin-arm64
`
	got := ParseChecksums(in)
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %v", len(got), got)
	}
	if got["kinopub-linux-amd64"] != "d1a5f4d3b2c1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5" {
		t.Errorf("linux entry wrong: %q", got["kinopub-linux-amd64"])
	}
	// Hashes are normalized to lowercase and the binary-mode '*' is stripped.
	if got["kinopub-darwin-arm64"] != "a1b5f4d3b2c1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5" {
		t.Errorf("darwin entry wrong: %q", got["kinopub-darwin-arm64"])
	}
}

// A truncated hash would fail every comparison; dropping it makes the release
// report a missing checksum instead of a mismatch, which is the truthful error.
func TestParseChecksums_SkipsMalformed(t *testing.T) {
	const in = `
# a comment
deadbeef  too-short
d1a5f4d3b2c1e0f9a8b7c6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5  good
missing-filename
`
	got := ParseChecksums(in)
	if len(got) != 1 {
		t.Fatalf("want only the well-formed entry, got %v", got)
	}
	if _, ok := got["good"]; !ok {
		t.Errorf("well-formed entry dropped: %v", got)
	}
}

func TestParseChecksums_Empty(t *testing.T) {
	if got := ParseChecksums(""); len(got) != 0 {
		t.Errorf("want no entries, got %v", got)
	}
}

// The updater derives this name from the constant; a drift would break signature
// lookup silently.
func TestChecksumsAssetName(t *testing.T) {
	if !strings.HasSuffix(ChecksumsAssetName, ".txt") {
		t.Errorf("unexpected checksums asset name %q", ChecksumsAssetName)
	}
}
