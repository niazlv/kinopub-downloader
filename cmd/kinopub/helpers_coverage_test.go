package main

import (
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
