package hlsdownloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// nopLogger is a no-op domain.Logger for FetchMasterPlaylist tests.
type nopLogger struct{}

func (nopLogger) Debug(string, ...domain.Field) {}
func (nopLogger) Info(string, ...domain.Field)  {}
func (nopLogger) Warn(string, ...domain.Field)  {}
func (nopLogger) Error(string, ...domain.Field) {}
func (l nopLogger) With(...domain.Field) domain.Logger {
	return l
}
func (l nopLogger) Component(string) domain.Logger {
	return l
}

func TestParseMasterPlaylistVariants(t *testing.T) {
	const base = "https://cdn.example.com/path/master.m3u8"
	body := `#EXTM3U
#EXT-X-VERSION:4
#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=1280x720,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud1"
720p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2560000,RESOLUTION=1920x1080,CODECS="hvc1.1.6.L120"
https://other.example.com/1080p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=640000,RESOLUTION=640x360
/abs/360p/index.m3u8
`
	mp, err := parseMasterPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMasterPlaylist: %v", err)
	}
	if len(mp.Variants) != 3 {
		t.Fatalf("expected 3 variants, got %d", len(mp.Variants))
	}

	v0 := mp.Variants[0]
	if v0.Bandwidth != 1280000 {
		t.Errorf("v0 bandwidth = %d, want 1280000", v0.Bandwidth)
	}
	if v0.Resolution != "1280x720" {
		t.Errorf("v0 resolution = %q", v0.Resolution)
	}
	if v0.Width != 1280 || v0.Height != 720 {
		t.Errorf("v0 dims = %dx%d, want 1280x720", v0.Width, v0.Height)
	}
	if v0.Codecs != "avc1.640028,mp4a.40.2" {
		t.Errorf("v0 codecs = %q", v0.Codecs)
	}
	if v0.AudioGroup != "aud1" {
		t.Errorf("v0 audio group = %q, want aud1", v0.AudioGroup)
	}
	// Relative URL resolved against base directory.
	if v0.URL != "https://cdn.example.com/path/720p/index.m3u8" {
		t.Errorf("v0 url = %q", v0.URL)
	}

	// Absolute http(s) URL preserved as-is.
	if mp.Variants[1].URL != "https://other.example.com/1080p/index.m3u8" {
		t.Errorf("v1 url = %q", mp.Variants[1].URL)
	}
	if mp.Variants[1].Codecs != "hvc1.1.6.L120" {
		t.Errorf("v1 codecs = %q", mp.Variants[1].Codecs)
	}

	// Root-relative URL resolved against base host.
	if mp.Variants[2].URL != "https://cdn.example.com/abs/360p/index.m3u8" {
		t.Errorf("v2 url = %q", mp.Variants[2].URL)
	}
	if mp.Variants[2].AudioGroup != "" {
		t.Errorf("v2 audio group should be empty, got %q", mp.Variants[2].AudioGroup)
	}
}

func TestParseMasterPlaylistAudioRenditions(t *testing.T) {
	const base = "https://cdn.example.com/path/master.m3u8"
	body := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud1",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en/index.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud1",NAME="Russian",LANGUAGE="ru",URI="https://other.example.com/audio/ru.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub1",NAME="English",LANGUAGE="en",URI="subs/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=1280x720,AUDIO="aud1"
720p/index.m3u8
`
	mp, err := parseMasterPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMasterPlaylist: %v", err)
	}
	// Only AUDIO type renditions are captured; SUBTITLES ignored.
	if len(mp.Audio) != 2 {
		t.Fatalf("expected 2 audio renditions, got %d", len(mp.Audio))
	}
	a0 := mp.Audio[0]
	if a0.GroupID != "aud1" || a0.Name != "English" || a0.Language != "en" {
		t.Errorf("a0 = %+v", a0)
	}
	if a0.URI != "https://cdn.example.com/path/audio/en/index.m3u8" {
		t.Errorf("a0 uri = %q", a0.URI)
	}
	if mp.Audio[1].URI != "https://other.example.com/audio/ru.m3u8" {
		t.Errorf("a1 uri = %q", mp.Audio[1].URI)
	}
	if len(mp.Variants) != 1 {
		t.Errorf("expected 1 variant, got %d", len(mp.Variants))
	}
}

func TestParseMasterPlaylistAudioLowercaseType(t *testing.T) {
	// TYPE matching is case-insensitive (ToUpper applied).
	const base = "https://cdn.example.com/master.m3u8"
	body := `#EXTM3U
#EXT-X-MEDIA:TYPE=audio,GROUP-ID="a",NAME="N",URI="a.m3u8"
`
	mp, err := parseMasterPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMasterPlaylist: %v", err)
	}
	if len(mp.Audio) != 1 {
		t.Fatalf("expected 1 audio rendition, got %d", len(mp.Audio))
	}
}

func TestParseMasterPlaylistEmptyAndCommentsOnly(t *testing.T) {
	mp, err := parseMasterPlaylist(strings.NewReader("#EXTM3U\n#EXT-X-VERSION:3\n"), "https://x/m.m3u8")
	if err != nil {
		t.Fatalf("parseMasterPlaylist: %v", err)
	}
	if len(mp.Variants) != 0 || len(mp.Audio) != 0 {
		t.Errorf("expected empty result, got %+v", mp)
	}
}

func TestParseMediaPlaylist(t *testing.T) {
	const base = "https://cdn.example.com/path/index.m3u8"
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:10
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:10.0,
seg0.ts
#EXTINF:9.5,
seg1.ts
#EXTINF:8.333,
https://cdn.example.com/path/seg2.ts
#EXT-X-ENDLIST
`
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(pl.Segments))
	}
	want := []struct {
		url string
		dur float64
		idx int
	}{
		{"https://cdn.example.com/path/seg0.ts", 10.0, 0},
		{"https://cdn.example.com/path/seg1.ts", 9.5, 1},
		{"https://cdn.example.com/path/seg2.ts", 8.333, 2},
	}
	for i, w := range want {
		s := pl.Segments[i]
		if s.URL != w.url {
			t.Errorf("seg[%d] url = %q, want %q", i, s.URL, w.url)
		}
		if s.Duration != w.dur {
			t.Errorf("seg[%d] dur = %v, want %v", i, s.Duration, w.dur)
		}
		if s.Index != w.idx {
			t.Errorf("seg[%d] index = %d, want %d", i, s.Index, w.idx)
		}
	}
	if got := pl.TotalDuration; got < 27.83 || got > 27.84 {
		t.Errorf("total duration = %v, want ~27.833", got)
	}
}

func TestParseMediaPlaylistExtinfNoComma(t *testing.T) {
	// #EXTINF without trailing comma should still parse the duration.
	const base = "https://cdn.example.com/index.m3u8"
	body := "#EXTM3U\n#EXTINF:5.0\nseg0.ts\n"
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(pl.Segments))
	}
	if pl.Segments[0].Duration != 5.0 {
		t.Errorf("duration = %v, want 5.0", pl.Segments[0].Duration)
	}
}

func TestParseMediaPlaylistZeroDurationEdge(t *testing.T) {
	// A valid #EXTINF:0 segment must be captured, not dropped: dropping it
	// would silently truncate the stream. The URI is captured with duration 0.
	const base = "https://cdn.example.com/index.m3u8"
	body := `#EXTM3U
#EXTINF:0,
seg0.ts
#EXTINF:6.0,
seg1.ts
`
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("expected 2 segments (zero-dur kept), got %d", len(pl.Segments))
	}
	if pl.Segments[0].URL != "https://cdn.example.com/seg0.ts" {
		t.Errorf("seg[0] url = %q", pl.Segments[0].URL)
	}
	if pl.Segments[0].Duration != 0 {
		t.Errorf("seg[0] duration = %v, want 0", pl.Segments[0].Duration)
	}
	if pl.Segments[0].Index != 0 {
		t.Errorf("seg[0] index = %d, want 0", pl.Segments[0].Index)
	}
	if pl.Segments[1].URL != "https://cdn.example.com/seg1.ts" {
		t.Errorf("seg[1] url = %q", pl.Segments[1].URL)
	}
	if pl.Segments[1].Index != 1 {
		t.Errorf("seg[1] index = %d, want 1", pl.Segments[1].Index)
	}
	if pl.TotalDuration != 6.0 {
		t.Errorf("total = %v, want 6.0", pl.TotalDuration)
	}
}

func TestParseMediaPlaylistNegativeDurationClamped(t *testing.T) {
	// A malformed negative duration must not drop the segment (would truncate
	// the stream) nor corrupt the total; it is captured with duration clamped
	// to 0.
	const base = "https://cdn.example.com/index.m3u8"
	body := "#EXTM3U\n#EXTINF:-1,\nseg0.ts\n"
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(pl.Segments))
	}
	if pl.Segments[0].Duration != 0 {
		t.Errorf("duration = %v, want 0 (clamped)", pl.Segments[0].Duration)
	}
	if pl.TotalDuration != 0 {
		t.Errorf("total = %v, want 0", pl.TotalDuration)
	}
}

func TestParseMediaPlaylistVeryLongLine(t *testing.T) {
	// A segment URL > 64KB exercises newPlaylistScanner's raised buffer; the
	// default bufio.Scanner would fail with bufio.ErrTooLong.
	const base = "https://cdn.example.com/index.m3u8"
	longQuery := strings.Repeat("a", 100*1024)
	longURL := "https://cdn.example.com/seg0.ts?token=" + longQuery
	body := "#EXTM3U\n#EXTINF:4.0,\n" + longURL + "\n"

	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if err != nil {
		t.Fatalf("parseMediaPlaylist with long line: %v", err)
	}
	if len(pl.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(pl.Segments))
	}
	if pl.Segments[0].URL != longURL {
		t.Errorf("long url not preserved (len got %d, want %d)", len(pl.Segments[0].URL), len(longURL))
	}
}

func TestParseMediaPlaylistEmpty(t *testing.T) {
	pl, err := parseMediaPlaylist(strings.NewReader(""), "https://x/i.m3u8")
	if err != nil {
		t.Fatalf("parseMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 0 || pl.TotalDuration != 0 {
		t.Errorf("expected empty playlist, got %+v", pl)
	}
}

// transientMarkers mirrors internal/app/kinopub's transientErrorMarkers list.
// The engine classifies a download error as retryable when its lowercased text
// contains any of these, so an "unsupported stream" error containing one would
// be retried forever instead of failing the episode once.
var transientMarkers = []string{
	"context deadline exceeded",
	"deadline exceeded",
	"timeout",
	"timed out",
	"unexpected eof",
	"connection reset",
	"connection refused",
	"broken pipe",
	"eof",
	"no such host",
	"temporary failure",
	"tls handshake",
	"http 429",
	"http 500",
	"http 502",
	"http 503",
	"http 504",
	"server misbehaving",
}

// assertFatalError checks that err exists, names the offending feature, and
// reads as permanent to the engine's transient-vs-fatal classifier.
func assertFatalError(t *testing.T, err error, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error mentioning %q, got nil", wantSubstr)
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, strings.ToLower(wantSubstr)) {
		t.Errorf("error %q does not mention %q", err, wantSubstr)
	}
	if !strings.Contains(msg, "not supported") {
		t.Errorf("error %q should state that the stream is not supported", err)
	}
	for _, marker := range transientMarkers {
		if strings.Contains(msg, marker) {
			t.Errorf("error %q contains transient marker %q, engine would retry it forever", err, marker)
		}
	}
}

func TestParseMediaPlaylistRejectsEncryption(t *testing.T) {
	// Segments of an encrypted stream cannot be concatenated into a playable
	// file, and this downloader implements no decryption — it must fail rather
	// than emit garbage.
	const base = "https://cdn.example.com/index.m3u8"
	cases := []struct {
		name   string
		tag    string
		method string
	}{
		{
			name:   "aes-128",
			tag:    `#EXT-X-KEY:METHOD=AES-128,URI="https://cdn.example.com/key.bin",IV=0x0123456789abcdef`,
			method: "aes-128",
		},
		{
			name:   "sample-aes",
			tag:    `#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://key"`,
			method: "sample-aes",
		},
		{
			name:   "lowercase method",
			tag:    `#EXT-X-KEY:METHOD=aes-128,URI="k.bin"`,
			method: "aes-128",
		},
		{
			name:   "missing method",
			tag:    `#EXT-X-KEY:URI="k.bin"`,
			method: "unspecified",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := "#EXTM3U\n" + c.tag + "\n#EXTINF:6.0,\nseg0.ts\n"
			pl, err := parseMediaPlaylist(strings.NewReader(body), base)
			if pl != nil {
				t.Errorf("expected no playlist on error, got %+v", pl)
			}
			assertFatalError(t, err, "encrypted")
			if !strings.Contains(strings.ToLower(err.Error()), c.method) {
				t.Errorf("error %q should name METHOD=%s", err, c.method)
			}
		})
	}
}

func TestParseMediaPlaylistAllowsKeyMethodNone(t *testing.T) {
	// METHOD=NONE is the valid "no encryption here" marker and must parse.
	const base = "https://cdn.example.com/index.m3u8"
	for _, tag := range []string{"#EXT-X-KEY:METHOD=NONE", "#EXT-X-KEY:METHOD=none"} {
		body := "#EXTM3U\n" + tag + "\n#EXTINF:6.0,\nseg0.ts\n#EXTINF:6.0,\nseg1.ts\n"
		pl, err := parseMediaPlaylist(strings.NewReader(body), base)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tag, err)
		}
		if len(pl.Segments) != 2 {
			t.Errorf("%s: got %d segments, want 2", tag, len(pl.Segments))
		}
	}
}

func TestParseMediaPlaylistRejectsFMP4Map(t *testing.T) {
	// An fMP4 stream needs its initialization segment prepended and cannot be
	// assembled by concatenating media segments.
	const base = "https://cdn.example.com/index.m3u8"
	body := `#EXTM3U
#EXT-X-MAP:URI="init.mp4"
#EXTINF:6.0,
seg0.m4s
`
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if pl != nil {
		t.Errorf("expected no playlist on error, got %+v", pl)
	}
	assertFatalError(t, err, "#EXT-X-MAP")
}

func TestParseMediaPlaylistRejectsByterange(t *testing.T) {
	// Byte-range segments live inside a shared resource and need Range
	// requests, which fetchSegment does not make.
	const base = "https://cdn.example.com/index.m3u8"
	body := `#EXTM3U
#EXTINF:6.0,
#EXT-X-BYTERANGE:75232@0
all.ts
#EXTINF:6.0,
#EXT-X-BYTERANGE:82112@75232
all.ts
`
	pl, err := parseMediaPlaylist(strings.NewReader(body), base)
	if pl != nil {
		t.Errorf("expected no playlist on error, got %+v", pl)
	}
	assertFatalError(t, err, "#EXT-X-BYTERANGE")
}

func TestCheckUnsupportedMediaTagPassesOrdinaryLines(t *testing.T) {
	// Only the three unsupported tags trip the check; everything else in a
	// normal playlist (including similarly named tags) passes through.
	lines := []string{
		"",
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:10",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:6.0,",
		"#EXT-X-ENDLIST",
		"#EXT-X-DISCONTINUITY",
		"#EXT-X-PROGRAM-DATE-TIME:2026-01-01T00:00:00Z",
		"seg0.ts",
		"https://cdn.example.com/seg0.ts?range=0-100",
	}
	for _, line := range lines {
		if err := checkUnsupportedMediaTag(line); err != nil {
			t.Errorf("line %q rejected: %v", line, err)
		}
	}
}

func TestFetchMediaPlaylistSurfacesUnsupportedStream(t *testing.T) {
	// A parse error must abort immediately: FetchMediaPlaylist's retry loop is
	// for network failures, and re-fetching an encrypted playlist changes
	// nothing.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.bin\"\n#EXTINF:6.0,\nseg0.ts\n"))
	}))
	defer srv.Close()

	_, err := FetchMediaPlaylist(context.Background(), srv.Client(), srv.URL+"/index.m3u8", domain.RequestAuth{})
	assertFatalError(t, err, "encrypted")
	if hits != 1 {
		t.Errorf("playlist fetched %d times, want 1 (parse errors must not retry)", hits)
	}
}

func TestParseVariantAttrs(t *testing.T) {
	tests := []struct {
		name  string
		attrs string
		want  Variant
	}{
		{
			name:  "full",
			attrs: `BANDWIDTH=1280000,RESOLUTION=1280x720,CODECS="avc1.4d401f",AUDIO="g1"`,
			want:  Variant{Bandwidth: 1280000, Resolution: "1280x720", Width: 1280, Height: 720, Codecs: "avc1.4d401f", AudioGroup: "g1"},
		},
		{
			name:  "no resolution",
			attrs: `BANDWIDTH=64000,CODECS="mp4a.40.2"`,
			want:  Variant{Bandwidth: 64000, Codecs: "mp4a.40.2"},
		},
		{
			name:  "bad bandwidth ignored",
			attrs: `BANDWIDTH=notanumber,RESOLUTION=640x360`,
			want:  Variant{Resolution: "640x360", Width: 640, Height: 360},
		},
		{
			name:  "malformed resolution",
			attrs: `RESOLUTION=1920`,
			want:  Variant{Resolution: "1920"},
		},
		{
			name:  "empty",
			attrs: ``,
			want:  Variant{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVariantAttrs(tt.attrs)
			if got != tt.want {
				t.Errorf("parseVariantAttrs(%q) = %+v, want %+v", tt.attrs, got, tt.want)
			}
		})
	}
}

func TestParseHLSAttributes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{
			name: "quoted and unquoted mix",
			in:   `BANDWIDTH=1280000,CODECS="avc1.4d401f,mp4a.40.2",RESOLUTION=1280x720`,
			want: map[string]string{"BANDWIDTH": "1280000", "CODECS": "avc1.4d401f,mp4a.40.2", "RESOLUTION": "1280x720"},
		},
		{
			name: "spaces around pairs (quoted value recognized despite leading space)",
			// Whitespace after '=' is trimmed before the leading-quote check,
			// so a space before the opening quote still yields a quoted value
			// with the surrounding quotes stripped.
			in:   ` KEY1 = val1 , KEY2 = "val 2" `,
			want: map[string]string{"KEY1": "val1", "KEY2": "val 2"},
		},
		{
			name: "unterminated quote takes rest",
			in:   `NAME="unterminated`,
			want: map[string]string{"NAME": "unterminated"},
		},
		{
			name: "trailing equals no value",
			in:   `KEY=`,
			want: map[string]string{"KEY": ""},
		},
		{
			name: "no equals sign breaks",
			in:   `JUSTTEXT`,
			want: map[string]string{},
		},
		{
			name: "empty",
			in:   ``,
			want: map[string]string{},
		},
		{
			name: "quoted value followed by no comma then key",
			in:   `A="x"B=y`,
			want: map[string]string{"A": "x", "B": "y"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHLSAttributes(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseHLSAttributes(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q = %q, want %q (full: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

func TestResolveURL(t *testing.T) {
	const base = "https://cdn.example.com/path/sub/master.m3u8"
	tests := []struct {
		name string
		base string
		ref  string
		want string
	}{
		{"empty ref", base, "", ""},
		{"absolute https preserved", base, "https://other.com/x.ts", "https://other.com/x.ts"},
		{"absolute http preserved", base, "http://other.com/x.ts", "http://other.com/x.ts"},
		{"relative same dir", base, "seg.ts", "https://cdn.example.com/path/sub/seg.ts"},
		{"relative subdir", base, "a/b/seg.ts", "https://cdn.example.com/path/sub/a/b/seg.ts"},
		{"root relative", base, "/x/seg.ts", "https://cdn.example.com/x/seg.ts"},
		{"parent relative", base, "../seg.ts", "https://cdn.example.com/path/seg.ts"},
		{"with query", base, "seg.ts?t=1", "https://cdn.example.com/path/sub/seg.ts?t=1"},
		{"invalid base returns ref", "://bad", "seg.ts", "seg.ts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveURL(tt.base, tt.ref)
			if got != tt.want {
				t.Errorf("resolveURL(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveURLInvalidRef(t *testing.T) {
	// A ref that fails url.Parse falls back to returning ref unchanged.
	got := resolveURL("https://cdn.example.com/master.m3u8", "ht tp://bad ref")
	if got != "ht tp://bad ref" {
		t.Errorf("resolveURL = %q, want unchanged ref", got)
	}
}

func TestNewPlaylistScannerCapacity(t *testing.T) {
	// Verify the scanner reads a single token larger than the default 64KB
	// limit without error.
	line := strings.Repeat("z", 200*1024)
	s := newPlaylistScanner(strings.NewReader(line + "\n"))
	if !s.Scan() {
		t.Fatalf("scan failed: %v", s.Err())
	}
	if len(s.Text()) != len(line) {
		t.Errorf("read len %d, want %d", len(s.Text()), len(line))
	}
	if err := s.Err(); err != nil {
		t.Errorf("scanner error: %v", err)
	}
}

func TestFetchMasterPlaylistSuccess(t *testing.T) {
	const masterBody = `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=1280000,RESOLUTION=1280x720
720p/index.m3u8
`
	var gotUA, gotCookie, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotCookie = r.Header.Get("Cookie")
		gotExtra = r.Header.Get("X-Test")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(masterBody))
	}))
	defer srv.Close()

	auth := domain.RequestAuth{
		UserAgent: "agent/1.0",
		Cookie:    "cf_clearance=abc",
		Headers:   map[string]string{"X-Test": "yes"},
	}
	mp, err := FetchMasterPlaylist(context.Background(), srv.Client(), srv.URL+"/master.m3u8", auth, nopLogger{})
	if err != nil {
		t.Fatalf("FetchMasterPlaylist: %v", err)
	}
	if len(mp.Variants) != 1 || mp.Variants[0].Bandwidth != 1280000 {
		t.Errorf("unexpected variants: %+v", mp.Variants)
	}
	if gotUA != "agent/1.0" || gotCookie != "cf_clearance=abc" || gotExtra != "yes" {
		t.Errorf("auth headers not applied: ua=%q cookie=%q extra=%q", gotUA, gotCookie, gotExtra)
	}
	// Resolution-derived URL should be absolute against the server base.
	if !strings.HasSuffix(mp.Variants[0].URL, "/720p/index.m3u8") {
		t.Errorf("variant url = %q", mp.Variants[0].URL)
	}
}

func TestFetchMasterPlaylistNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := FetchMasterPlaylist(context.Background(), srv.Client(), srv.URL, domain.RequestAuth{}, nopLogger{})
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want HTTP 403 mention", err)
	}
}

func TestFetchMasterPlaylistBadURL(t *testing.T) {
	_, err := FetchMasterPlaylist(context.Background(), http.DefaultClient, "://bad-url", domain.RequestAuth{}, nopLogger{})
	if err == nil {
		t.Fatal("expected error for malformed URL")
	}
}

func TestFetchMediaPlaylistSuccess(t *testing.T) {
	const mediaBody = `#EXTM3U
#EXTINF:6.0,
seg0.ts
#EXTINF:6.0,
seg1.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mediaBody))
	}))
	defer srv.Close()

	pl, err := FetchMediaPlaylist(context.Background(), srv.Client(), srv.URL+"/index.m3u8", domain.RequestAuth{})
	if err != nil {
		t.Fatalf("FetchMediaPlaylist: %v", err)
	}
	if len(pl.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(pl.Segments))
	}
	if pl.TotalDuration != 12.0 {
		t.Errorf("total duration = %v, want 12.0", pl.TotalDuration)
	}
}

func TestFetchMediaPlaylistContextCancelled(t *testing.T) {
	// A pre-cancelled context returns immediately without contacting a server.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FetchMediaPlaylist(ctx, http.DefaultClient, "https://cdn.example.com/i.m3u8", domain.RequestAuth{})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestApplyHLSAuth(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
		applyHLSAuth(req, domain.RequestAuth{
			UserAgent: "ua",
			Cookie:    "c=1",
			Headers:   map[string]string{"X-A": "1", "X-B": "2"},
		})
		if req.Header.Get("User-Agent") != "ua" {
			t.Errorf("ua = %q", req.Header.Get("User-Agent"))
		}
		if req.Header.Get("Cookie") != "c=1" {
			t.Errorf("cookie = %q", req.Header.Get("Cookie"))
		}
		if req.Header.Get("X-A") != "1" || req.Header.Get("X-B") != "2" {
			t.Errorf("extra headers not set: %v", req.Header)
		}
	})

	t.Run("empty auth sets nothing", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://x/", nil)
		applyHLSAuth(req, domain.RequestAuth{})
		if req.Header.Get("User-Agent") != "" {
			t.Errorf("unexpected UA: %q", req.Header.Get("User-Agent"))
		}
		if req.Header.Get("Cookie") != "" {
			t.Errorf("unexpected cookie: %q", req.Header.Get("Cookie"))
		}
	})
}
