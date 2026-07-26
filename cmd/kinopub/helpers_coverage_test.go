package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/browsercookies"
)

// --- splitShellArgs ---

func TestSplitShellArgs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"only_spaces", "   ", nil},
		{"single_word", "foo", []string{"foo"}},
		{"two_words", "foo bar", []string{"foo", "bar"}},
		{"multiple_spaces", "foo   bar", []string{"foo", "bar"}},
		{"leading_trailing_spaces", "  foo bar  ", []string{"foo", "bar"}},
		{"tabs", "foo\tbar", []string{"foo", "bar"}},
		{"mixed_tabs_spaces", "foo \t bar\tbaz", []string{"foo", "bar", "baz"}},
		{"double_quoted", `"foo bar"`, []string{"foo bar"}},
		{"single_quoted", `'foo bar'`, []string{"foo bar"}},
		{"double_with_tab_inside", "\"foo\tbar\"", []string{"foo\tbar"}},
		{"single_inside_double", `"it's fine"`, []string{"it's fine"}},
		{"double_inside_single", `'say "hi"'`, []string{`say "hi"`}},
		{"quoted_and_unquoted", `-c:v "libx265 fast" -crf 28`, []string{"-c:v", "libx265 fast", "-crf", "28"}},
		{"empty_double_quotes", `a "" b`, []string{"a", "b"}},
		{"adjacent_quote_join", `foo"bar"baz`, []string{"foobarbaz"}},
		{"ffmpeg_example", `-c:v libx265 -crf 28`, []string{"-c:v", "libx265", "-crf", "28"}},
		{"unterminated_double", `foo "bar baz`, []string{"foo", "bar baz"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := splitShellArgs(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitShellArgs(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

// --- headerList.Set ---

func TestHeaderListSet_Valid(t *testing.T) {
	var h headerList
	for _, v := range []string{"Accept: application/json", "X-Token:abc", "Empty-Val:"} {
		if err := h.Set(v); err != nil {
			t.Fatalf("Set(%q) unexpected error: %v", v, err)
		}
	}
	if len(h) != 3 {
		t.Fatalf("expected 3 entries, got %d: %#v", len(h), h)
	}
}

func TestHeaderListSet_MissingColon(t *testing.T) {
	var h headerList
	err := h.Set("NoColonHere")
	if err == nil {
		t.Fatal("expected error for header without colon")
	}
	if !errors.Is(err, domain.ErrInvalidFlag) {
		t.Errorf("expected ErrInvalidFlag, got %v", err)
	}
	if len(h) != 0 {
		t.Errorf("expected no entries on error, got %#v", h)
	}
}

func TestHeaderListSet_EmptyName(t *testing.T) {
	var h headerList
	for _, v := range []string{": value", "   : value"} {
		err := h.Set(v)
		if err == nil {
			t.Fatalf("expected error for empty name %q", v)
		}
		if !errors.Is(err, domain.ErrInvalidFlag) {
			t.Errorf("Set(%q): expected ErrInvalidFlag, got %v", v, err)
		}
	}
	if len(h) != 0 {
		t.Errorf("expected no entries, got %#v", h)
	}
}

// --- headerList.toMap ---

func TestHeaderListToMap(t *testing.T) {
	tests := []struct {
		name    string
		entries headerList
		want    map[string]string
	}{
		{"nil_when_empty", nil, nil},
		{"empty_slice_nil", headerList{}, nil},
		{"trims_name_and_value", headerList{"  Accept :  application/json  "}, map[string]string{"Accept": "application/json"}},
		{"plain", headerList{"X-Token:abc"}, map[string]string{"X-Token": "abc"}},
		{"multiple", headerList{"A:1", "B:2"}, map[string]string{"A": "1", "B": "2"}},
		// An entry whose name trims to empty is skipped by toMap.
		{"skip_empty_name", headerList{":val", "Good:1"}, map[string]string{"Good": "1"}},
		{"value_with_colon_kept_first_cut", headerList{"Date: a:b:c"}, map[string]string{"Date": "a:b:c"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.entries.toMap()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("toMap() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// --- headerList.String ---

func TestHeaderListString(t *testing.T) {
	var empty headerList
	if got := empty.String(); got != "" {
		t.Errorf("empty headerList.String() = %q, want \"\"", got)
	}
	h := headerList{"A:1", "B:2"}
	if got := h.String(); got != "A:1, B:2" {
		t.Errorf("headerList.String() = %q, want %q", got, "A:1, B:2")
	}
}

// --- ffmpegExtraList ---

func TestFFmpegExtraListSetAndString(t *testing.T) {
	var f ffmpegExtraList
	if got := f.String(); got != "" {
		t.Errorf("empty String() = %q, want \"\"", got)
	}
	for _, v := range []string{"-c:v", "libx265", "-crf", "28"} {
		if err := f.Set(v); err != nil {
			t.Fatalf("Set(%q) error: %v", v, err)
		}
	}
	if len(f) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(f))
	}
	if got := f.String(); got != "-c:v libx265 -crf 28" {
		t.Errorf("String() = %q, want %q", got, "-c:v libx265 -crf 28")
	}
}

// --- browserCookiesFlag ---

func TestBrowserCookiesFlagSet(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare_empty_to_auto", "", browsercookies.BrowserAuto},
		{"true_to_auto", "true", browsercookies.BrowserAuto},
		{"safari", "safari", browsercookies.BrowserSafari},
		{"uppercase_lowercased", "SAFARI", browsercookies.BrowserSafari},
		{"trims_whitespace", "  chrome  ", browsercookies.BrowserChrome},
		{"firefox", "Firefox", browsercookies.BrowserFirefox},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b browserCookiesFlag
			if err := b.Set(tc.input); err != nil {
				t.Fatalf("Set(%q) error: %v", tc.input, err)
			}
			if !b.set {
				t.Errorf("Set(%q): expected set=true", tc.input)
			}
			if b.value != tc.want {
				t.Errorf("Set(%q): value = %q, want %q", tc.input, b.value, tc.want)
			}
		})
	}
}

func TestBrowserCookiesFlagStringAndIsBoolFlag(t *testing.T) {
	var b browserCookiesFlag
	if got := b.String(); got != "" {
		t.Errorf("zero String() = %q, want \"\"", got)
	}
	if err := b.Set("safari"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if got := b.String(); got != browsercookies.BrowserSafari {
		t.Errorf("String() = %q, want %q", got, browsercookies.BrowserSafari)
	}
	if !b.IsBoolFlag() {
		t.Error("IsBoolFlag() = false, want true")
	}
}

// --- isKnownBrowser ---

func TestIsKnownBrowser(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"auto", true},
		{"safari", true},
		{"chrome", true},
		{"firefox", true},
		{"Safari", true},
		{"CHROME", true},
		{"edge", false},
		{"brave", false},
		{"", false},
		{"opera", false},
	}

	for _, tc := range tests {
		t.Run("input_"+tc.input, func(t *testing.T) {
			if got := isKnownBrowser(tc.input); got != tc.want {
				t.Errorf("isKnownBrowser(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// --- storedCredentialsAllowed ---

func TestStoredCredentialsAllowed(t *testing.T) {
	tests := []struct {
		name       string
		storedSite string
		target     domain.Site
		want       bool
	}{
		// The site the credentials were saved for is the site they are sent to.
		{"exact_host", "kino.watch", domain.Site{Host: "kino.watch"}, true},
		{"exact_host_other_known", "kino.pub", domain.Site{Host: "kino.pub"}, true},
		{"case_insensitive", "KINO.watch", domain.Site{Host: "kino.WATCH"}, true},
		{"target_carries_port", "kino.watch", domain.Site{Host: "kino.watch:8443"}, true},
		{"stored_as_url", "https://kino.watch/", domain.Site{Host: "kino.watch"}, true},
		// A subdomain belongs to the stored site.
		{"subdomain", "kino.watch", domain.Site{Host: "www.kino.watch"}, true},
		{"deep_subdomain", "kino.watch", domain.Site{Host: "a.b.kino.watch"}, true},
		// A parent domain does not: cookies for a subdomain are not the parent's.
		{"parent_of_stored", "www.kino.watch", domain.Site{Host: "kino.watch"}, false},
		// Zero target resolves to the default site.
		{"zero_target_default_site", domain.DefaultSiteHost, domain.Site{}, true},
		{"zero_target_other_site", "kino.pub", domain.Site{}, false},
		// Legacy file: no site recorded, so any host the service is known by is
		// allowed and everything else is not.
		{"legacy_known_target", "", domain.Site{Host: "kino.watch"}, true},
		{"legacy_other_known_target", "", domain.Site{Host: "kino.pub"}, true},
		{"legacy_known_subdomain", "", domain.Site{Host: "www.kino.pub"}, true},
		{"legacy_zero_target", "", domain.Site{}, true},
		{"legacy_blank_stored", "   ", domain.Site{Host: "kino.watch"}, true},
		{"legacy_unknown_target", "", domain.Site{Host: "evil.example"}, false},
		// The defect this guards: a "mirror" link must not receive the session.
		{"outright_mismatch", "kino.watch", domain.Site{Host: "evil.example"}, false},
		{"lookalike_suffix", "kino.watch", domain.Site{Host: "evilkino.watch"}, false},
		{"stored_site_as_suffix_of_target", "kino.watch", domain.Site{Host: "kino.watch.evil.example"}, false},
		{"other_known_host_not_implied", "kino.watch", domain.Site{Host: "kino.pub"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := storedCredentialsAllowed(tc.storedSite, tc.target); got != tc.want {
				t.Errorf("storedCredentialsAllowed(%q, %q) = %v, want %v",
					tc.storedSite, tc.target, got, tc.want)
			}
		})
	}
}

// --- exitCodeFor ---

func TestExitCodeFor(t *testing.T) {
	tests := []struct {
		name   string
		res    domain.RunResult
		ctxErr error
		want   int
	}{
		{"all succeeded", domain.RunResult{Total: 3, Succeeded: 3}, nil, 0},
		{"nothing to do", domain.RunResult{Total: 0}, nil, 0},
		{"partial failure", domain.RunResult{Total: 3, Succeeded: 2, Failed: 1}, nil, 1},
		{"every episode failed", domain.RunResult{Total: 3, Failed: 3}, nil, 1},
		{"all skipped", domain.RunResult{Total: 3, Skipped: 3}, nil, 1},
		{"interrupted", domain.RunResult{Total: 3, Succeeded: 1}, context.Canceled, 130},
		{"interrupted outranks failures", domain.RunResult{Total: 3, Failed: 2}, context.Canceled, 130},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCodeFor(tt.res, tt.ctxErr); got != tt.want {
				t.Errorf("exitCodeFor(%+v, %v) = %d, want %d", tt.res, tt.ctxErr, got, tt.want)
			}
		})
	}
}
