// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// byteSinkRecorder records both percent and byte-level progress updates.
// It deliberately implements domain.ByteProgressSink (unlike the existing
// testProgressSink) so we can exercise the byte-reporting branches in
// chunked.go.
type byteSinkRecorder struct {
	pct       []int
	bytesDone []int64
	bytesTot  []int64
}

func (s *byteSinkRecorder) TrackProgress(_ domain.EpisodeKey, _ domain.TrackRef, percent int) {
	s.pct = append(s.pct, percent)
}

func (s *byteSinkRecorder) ByteProgress(_ domain.EpisodeKey, downloaded, total int64) {
	s.bytesDone = append(s.bytesDone, downloaded)
	s.bytesTot = append(s.bytesTot, total)
}

// ---------------------------------------------------------------------------
// formatBytes boundaries
// ---------------------------------------------------------------------------

func TestFormatBytes_Boundaries(t *testing.T) {
	const (
		kb int64 = 1024
		mb       = 1024 * kb
		gb       = 1024 * mb
	)
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"one byte", 1, "1 B"},
		{"just under KB", kb - 1, "1023 B"},
		{"exactly KB", kb, "1.0 KB"},
		{"1.5 KB", kb + kb/2, "1.5 KB"},
		{"just under MB", mb - 1, "1024.0 KB"},
		{"exactly MB", mb, "1.0 MB"},
		{"2.5 MB", mb*2 + mb/2, "2.5 MB"},
		{"just under GB", gb - 1, "1024.0 MB"},
		{"exactly GB", gb, "1.0 GB"},
		{"3.2 GB", gb*3 + gb/5, "3.2 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBytes(tt.in); got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// applyAuth header-injection rules
// ---------------------------------------------------------------------------

func newChunkedForAuth(auth domain.RequestAuth) *ChunkedDownloader {
	return NewChunked(&http.Client{}, auth, testLogger{})
}

func TestApplyAuth_CookieOnlyToKinoPubHost(t *testing.T) {
	auth := domain.RequestAuth{
		Cookie:    "cf_clearance=abc",
		UserAgent: "TestUA/1.0",
		Headers:   map[string]string{"Referer": "https://kino.pub/"},
	}
	c := newChunkedForAuth(auth)

	tests := []struct {
		name       string
		url        string
		wantCookie bool
	}{
		{"kino.pub gets cookie", "https://api.kino.pub/v1/file", true},
		{"kino.pub subdomain gets cookie", "https://www.kino.pub/x", true},
		{"cdn host no cookie", "https://stream.cdntogo.net/video.mp4", false},
		{"unrelated host no cookie", "https://example.com/a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			c.applyAuth(req)

			if got := req.Header.Get("Cookie"); (got != "") != tt.wantCookie {
				t.Errorf("Cookie header = %q, wantCookie=%v", got, tt.wantCookie)
			}
			// User-Agent and extra headers always applied.
			if got := req.Header.Get("User-Agent"); got != "TestUA/1.0" {
				t.Errorf("User-Agent = %q, want TestUA/1.0", got)
			}
			if got := req.Header.Get("Referer"); got != "https://kino.pub/" {
				t.Errorf("Referer = %q, want set", got)
			}
		})
	}
}

func TestApplyAuth_EmptyAuthSetsNothing(t *testing.T) {
	c := newChunkedForAuth(domain.RequestAuth{})
	req, err := http.NewRequest(http.MethodGet, "https://kino.pub/x", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	c.applyAuth(req)
	if got := req.Header.Get("Cookie"); got != "" {
		t.Errorf("expected no Cookie, got %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("expected no User-Agent, got %q", got)
	}
}

func TestApplyAuth_CookieEmptyEvenOnKinoPub(t *testing.T) {
	// Cookie is empty so even kino.pub host must not get a Cookie header.
	c := newChunkedForAuth(domain.RequestAuth{UserAgent: "UA"})
	req, _ := http.NewRequest(http.MethodGet, "https://kino.pub/x", nil)
	c.applyAuth(req)
	if got := req.Header.Get("Cookie"); got != "" {
		t.Errorf("expected no Cookie with empty auth.Cookie, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// CanHandle
// ---------------------------------------------------------------------------

func TestChunked_CanHandle(t *testing.T) {
	c := newChunkedForAuth(domain.RequestAuth{})
	tests := []struct {
		name  string
		media domain.ResolvedMedia
		want  bool
	}{
		{
			name:  "progressive with url",
			media: domain.ResolvedMedia{Source: domain.MediaSource{Kind: domain.MediaProgressive, URL: "https://x/v.mp4"}},
			want:  true,
		},
		{
			name:  "progressive no url",
			media: domain.ResolvedMedia{Source: domain.MediaSource{Kind: domain.MediaProgressive}},
			want:  false,
		},
		{
			name:  "hls not handled",
			media: domain.ResolvedMedia{Source: domain.MediaSource{Kind: domain.MediaHLS, URL: "https://x/m.m3u8"}},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.CanHandle(tt.media); got != tt.want {
				t.Errorf("CanHandle = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// getPartialOffset
// ---------------------------------------------------------------------------

func TestGetPartialOffset(t *testing.T) {
	c := newChunkedForAuth(domain.RequestAuth{})

	t.Run("no file returns zero", func(t *testing.T) {
		dir := t.TempDir()
		got := c.getPartialOffset(filepath.Join(dir, "missing.part"), 100)
		if got != 0 {
			t.Errorf("offset = %d, want 0", got)
		}
	})

	t.Run("existing partial returns size", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "a.part")
		if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
		got := c.getPartialOffset(p, 100)
		if got != 5 {
			t.Errorf("offset = %d, want 5", got)
		}
	})

	t.Run("partial larger than total is deleted", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "big.part")
		if err := os.WriteFile(p, []byte("0123456789"), 0644); err != nil {
			t.Fatal(err)
		}
		got := c.getPartialOffset(p, 5)
		if got != 0 {
			t.Errorf("offset = %d, want 0", got)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected part file removed, stat err = %v", err)
		}
	})

	t.Run("total zero never deletes", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "z.part")
		if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		got := c.getPartialOffset(p, 0)
		if got != 4 {
			t.Errorf("offset = %d, want 4", got)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file should still exist: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// probeSize via httptest
// ---------------------------------------------------------------------------

func TestProbeSize(t *testing.T) {
	t.Run("content length from header", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead {
				t.Errorf("expected HEAD, got %s", r.Method)
			}
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := newChunkedForAuth(domain.RequestAuth{})
		size, err := c.probeSize(context.Background(), srv.URL)
		if err != nil {
			t.Fatalf("probeSize: %v", err)
		}
		if size != 4096 {
			t.Errorf("size = %d, want 4096", size)
		}
	})

	t.Run("non-200 returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer srv.Close()

		c := newChunkedForAuth(domain.RequestAuth{})
		_, err := c.probeSize(context.Background(), srv.URL)
		if err == nil {
			t.Fatal("expected error for HTTP 403")
		}
	})
}

// ---------------------------------------------------------------------------
// Full Download via httptest with Range support
// ---------------------------------------------------------------------------

// rangeFileServer serves the given content honoring HEAD and Range GET.
func rangeFileServer(content []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		total := int64(len(content))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", itoa(total))
			w.WriteHeader(http.StatusOK)
			return
		}
		// GET
		rangeHdr := r.Header.Get("Range")
		var start int64
		if rangeHdr != "" {
			// format "bytes=START-"
			s := strings.TrimPrefix(rangeHdr, "bytes=")
			s = strings.TrimSuffix(s, "-")
			start = parseInt(s)
			if start >= total {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Length", itoa(total-start))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[start:])
			return
		}
		w.Header().Set("Content-Length", itoa(total))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func parseInt(s string) int64 {
	var n int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int64(ch-'0')
	}
	return n
}

func TestChunked_Download_FullSuccess(t *testing.T) {
	content := []byte(strings.Repeat("A", 700*1024)) // > progressReportInterval
	srv := httptest.NewServer(rangeFileServer(content))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "out.mp4")

	c := newChunkedForAuth(domain.RequestAuth{})
	sink := &byteSinkRecorder{}
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 1}

	if err := c.Download(context.Background(), srv.URL, outPath, key, sink); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if len(got) != len(content) {
		t.Errorf("downloaded %d bytes, want %d", len(got), len(content))
	}
	// .part file must be gone after rename.
	if _, err := os.Stat(outPath + ".part"); !os.IsNotExist(err) {
		t.Errorf("expected .part removed, got %v", err)
	}
	// Progress should have been reported.
	if len(sink.pct) == 0 {
		t.Error("expected at least one progress update")
	}
	if len(sink.bytesTot) == 0 || sink.bytesTot[len(sink.bytesTot)-1] != int64(len(content)) {
		t.Error("expected final byte total to match content size")
	}
}

func TestChunked_Download_ResumeFromPartial(t *testing.T) {
	content := []byte(strings.Repeat("B", 200*1024))
	srv := httptest.NewServer(rangeFileServer(content))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "resume.mp4")
	partPath := outPath + ".part"

	// Pre-write half the content into the .part file.
	half := len(content) / 2
	if err := os.WriteFile(partPath, content[:half], 0644); err != nil {
		t.Fatal(err)
	}

	c := newChunkedForAuth(domain.RequestAuth{})
	sink := &byteSinkRecorder{}
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 2}

	if err := c.Download(context.Background(), srv.URL, outPath, key, sink); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("resumed content mismatch: got %d bytes want %d", len(got), len(content))
	}
}

func TestChunked_Download_AlreadyComplete(t *testing.T) {
	content := []byte(strings.Repeat("C", 1024))
	srv := httptest.NewServer(rangeFileServer(content))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "done.mp4")
	partPath := outPath + ".part"

	// Pre-write the full content — Download should just rename.
	if err := os.WriteFile(partPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	c := newChunkedForAuth(domain.RequestAuth{})
	sink := &byteSinkRecorder{}
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 3}

	if err := c.Download(context.Background(), srv.URL, outPath, key, sink); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(got) != string(content) {
		t.Error("already-complete content mismatch")
	}
	// Should report 100%.
	foundFull := false
	for _, p := range sink.pct {
		if p == 100 {
			foundFull = true
		}
	}
	if !foundFull {
		t.Error("expected a 100% progress update for already-complete file")
	}
}

func TestChunked_Download_ProbeFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	dir := t.TempDir()
	c := newChunkedForAuth(domain.RequestAuth{})
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 4}

	err := c.Download(context.Background(), srv.URL, filepath.Join(dir, "x.mp4"), key, nil)
	if err == nil {
		t.Fatal("expected error when probe fails")
	}
	if !strings.Contains(err.Error(), "probe") {
		t.Errorf("expected probe error, got %v", err)
	}
}

func TestChunked_Download_NilSink(t *testing.T) {
	content := []byte(strings.Repeat("D", 2048))
	srv := httptest.NewServer(rangeFileServer(content))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "nilsink.mp4")
	c := newChunkedForAuth(domain.RequestAuth{})
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 5}

	if err := c.Download(context.Background(), srv.URL, outPath, key, nil); err != nil {
		t.Fatalf("Download with nil sink: %v", err)
	}
	got, _ := os.ReadFile(outPath)
	if len(got) != len(content) {
		t.Errorf("got %d bytes, want %d", len(got), len(content))
	}
}

// ---------------------------------------------------------------------------
// outputFormat
// ---------------------------------------------------------------------------

func TestOutputFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/x/out.mp4", "mp4"},
		{"/x/out.mp4.tmp", "mp4"},
		{"/x/out.mkv", "matroska"},
		{"/x/out.mkv.tmp", "matroska"},
		{"/x/out.tmp", "matroska"},
		{"/x/out", "matroska"},
		{"/x/out.webm.tmp", "matroska"},
		{"/x/movie.MP4", "matroska"}, // case-sensitive: uppercase not matched
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := outputFormat(tt.in); got != tt.want {
				t.Errorf("outputFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// appendContainerMetadata
// ---------------------------------------------------------------------------

func TestAppendContainerMetadata_MatroskaWithPoster(t *testing.T) {
	job := domain.Job{
		Episode: domain.Episode{
			Title: "The Pilot",
			Key:   domain.EpisodeKey{Series: "show", Season: 2, Episode: 7},
		},
		SeriesTitle: "My Show",
		PosterPath:  "/posters/p.jpg",
	}
	args := appendContainerMetadata(nil, job, "matroska")
	s := strings.Join(args, " ")

	for _, want := range []string{
		"title=The Pilot",
		"SHOW=My Show",
		"episode_sort=7",
		"season_number=2",
		"episode_id=" + job.Episode.Key.Label(),
		"-attach", "/posters/p.jpg",
		"mimetype=image/jpeg",
		"filename=cover.jpg",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("metadata missing %q in: %s", want, s)
		}
	}
}

func TestAppendContainerMetadata_Mp4SkipsPoster(t *testing.T) {
	job := domain.Job{
		Episode:    domain.Episode{Key: domain.EpisodeKey{Series: "show", Season: 1, Episode: 1}},
		PosterPath: "/posters/p.jpg",
	}
	args := appendContainerMetadata(nil, job, "mp4")
	s := strings.Join(args, " ")
	if strings.Contains(s, "-attach") {
		t.Errorf("mp4 must not attach poster: %s", s)
	}
	// No episode title or series title — those metadata keys should be absent.
	if strings.Contains(s, "title=") {
		t.Errorf("did not expect title metadata with empty title: %s", s)
	}
	if strings.Contains(s, "SHOW=") {
		t.Errorf("did not expect SHOW metadata with empty series title: %s", s)
	}
	// But sort/season/id always present.
	if !strings.Contains(s, "episode_sort=1") || !strings.Contains(s, "season_number=1") {
		t.Errorf("expected sort/season metadata always present: %s", s)
	}
}

func TestAppendContainerMetadata_MatroskaNoPosterPath(t *testing.T) {
	job := domain.Job{
		Episode: domain.Episode{Key: domain.EpisodeKey{Series: "show", Season: 3, Episode: 9}},
	}
	args := appendContainerMetadata(nil, job, "matroska")
	s := strings.Join(args, " ")
	if strings.Contains(s, "-attach") {
		t.Errorf("no poster path should mean no -attach: %s", s)
	}
}

// ---------------------------------------------------------------------------
// makeUnique direct edge cases
// ---------------------------------------------------------------------------

func TestMakeUnique_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, []string{}},
		{"single", []string{"a"}, []string{"a"}},
		{"all unique", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"two dupes", []string{"a", "a"}, []string{"a", "a (2)"}},
		{
			"interleaved dupes",
			[]string{"a", "b", "a", "b", "a"},
			[]string{"a", "b", "a (2)", "b (2)", "a (3)"},
		},
		{"empty strings dupe", []string{"", ""}, []string{"", " (2)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeUnique(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resume correctness against uncooperative servers
// ---------------------------------------------------------------------------

// TestChunked_Download_ServerIgnoresRange covers the server that answers a
// resume request with 200 and the whole body instead of 206. The restarted body
// overwrites the .part file from byte 0, so the bytes already on disk must not
// be counted twice — doing so leaves an unwritten hole that still passes the
// final size check and yields a silently corrupt video.
func TestChunked_Download_ServerIgnoresRange(t *testing.T) {
	content := []byte(strings.Repeat("D", 300*1024))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		total := int64(len(content))
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", itoa(total))
			w.WriteHeader(http.StatusOK)
			return
		}
		// Always ignore Range and send the full body from the start.
		w.Header().Set("Content-Length", itoa(total))
		w.WriteHeader(http.StatusOK)
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "ignored-range.mp4")
	partPath := outPath + ".part"

	// Pre-write a partial file so Download issues a Range request.
	half := len(content) / 2
	if err := os.WriteFile(partPath, []byte(strings.Repeat("X", half)), 0644); err != nil {
		t.Fatal(err)
	}

	c := newChunkedForAuth(domain.RequestAuth{})
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 7}

	if err := c.Download(context.Background(), srv.URL, outPath, key, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch after Range-ignoring server: got %d bytes want %d",
			len(got), len(content))
	}
}

// TestChunked_Download_NoContentLength covers a server that reports no size.
// Completeness is unverifiable in that case, so Download must fail (letting the
// caller fall back to ffmpeg) rather than rename a possibly truncated .part.
func TestChunked_Download_NoContentLength(t *testing.T) {
	content := []byte(strings.Repeat("E", 4096))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			// No Content-Length header at all.
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "nosize.mp4")

	c := newChunkedForAuth(domain.RequestAuth{})
	key := domain.EpisodeKey{Series: "s", Season: 1, Episode: 8}

	err := c.Download(context.Background(), srv.URL, outPath, key, nil)
	if err == nil {
		t.Fatal("expected an error when the server reports no content length")
	}
	if _, statErr := os.Stat(outPath); !os.IsNotExist(statErr) {
		t.Errorf("output file must not be created without a verifiable size, got %v", statErr)
	}
}
