// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package hlsdownloader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// testDownloader builds a Downloader wired to a test server's client. It
// bypasses New(), which would install the uTLS browser client used against real
// CDNs, so requests reach httptest servers unchanged.
func testDownloader(client *http.Client) *Downloader {
	return &Downloader{
		client:      client,
		logger:      nopLogger{},
		concurrency: defaultConcurrency,
	}
}

func TestFetchSegmentWritesAtomically(t *testing.T) {
	const body = "segment-payload"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_00000.ts")

	d := testDownloader(srv.Client())
	n, err := d.fetchSegment(context.Background(), srv.URL+"/seg0.ts", segPath)
	if err != nil {
		t.Fatalf("fetchSegment: %v", err)
	}
	if n != int64(len(body)) {
		t.Errorf("wrote %d bytes, want %d", n, len(body))
	}

	got, err := os.ReadFile(segPath)
	if err != nil {
		t.Fatalf("read segment: %v", err)
	}
	if string(got) != body {
		t.Errorf("segment content = %q, want %q", got, body)
	}

	// The scratch file must not survive a successful download.
	if _, err := os.Stat(segmentPartPath(segPath)); !os.IsNotExist(err) {
		t.Errorf("temp file still present after success: %v", err)
	}
}

func TestFetchSegmentTruncatedBodyLeavesNothingBehind(t *testing.T) {
	// The server promises more bytes than it delivers, so io.Copy fails partway
	// through — the same shape as a connection dying mid-segment. Neither the
	// final path nor the temp path may hold the partial data afterwards: a file
	// at the final path would be accepted by the resume check on the next run
	// and concatenated into a corrupt episode.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("only-a-few-bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_00000.ts")

	d := testDownloader(srv.Client())
	if _, err := d.fetchSegment(context.Background(), srv.URL+"/seg0.ts", segPath); err == nil {
		t.Fatal("expected an error for a short body")
	}

	if _, err := os.Stat(segPath); !os.IsNotExist(err) {
		t.Errorf("truncated segment published at final path: %v", err)
	}
	if _, err := os.Stat(segmentPartPath(segPath)); !os.IsNotExist(err) {
		t.Errorf("temp file not cleaned up after failure: %v", err)
	}
}

// anyFileWithData reports whether dir holds at least one non-empty file, i.e.
// whether some segment bytes have reached the disk regardless of the path the
// implementation chose for them.
func anyFileWithData(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read segment dir: %v", err)
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !info.IsDir() && info.Size() > 0 {
			return true
		}
	}
	return false
}

func TestFetchSegmentDoesNotPublishUntilComplete(t *testing.T) {
	// The heart of defect 1: while a segment is still streaming, its bytes must
	// not be observable at the final path. A process killed at this instant
	// (nothing gets to run a cleanup) previously left a truncated .ts that the
	// next run's resume check accepted as finished.
	var releaseOnce sync.Once
	released := make(chan struct{})
	release := func() { releaseOnce.Do(func() { close(released) }) }
	t.Cleanup(release)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise a full segment, deliver only its first chunk, then stall.
		w.Header().Set("Content-Length", "65536")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("a"), 8192))
		w.(http.Flusher).Flush()
		<-released
	}))
	defer srv.Close()

	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_00000.ts")
	partPath := segmentPartPath(segPath)

	d := testDownloader(srv.Client())
	errCh := make(chan error, 1)
	go func() {
		_, err := d.fetchSegment(context.Background(), srv.URL+"/seg0.ts", segPath)
		errCh <- err
	}()

	// Wait until the first chunk has reached the disk — wherever the
	// implementation put it — so the assertion below runs while a genuinely
	// partial segment exists in the dir rather than before the copy started.
	deadline := time.Now().Add(5 * time.Second)
	for !anyFileWithData(t, dir) {
		if time.Now().After(deadline) {
			release()
			t.Fatal("gave up waiting for partial segment data to reach the disk")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := os.Stat(segPath); !os.IsNotExist(err) {
		t.Errorf("partial segment visible at the final path mid-download: %v", err)
	}
	if info, err := os.Stat(partPath); err != nil || info.Size() == 0 {
		t.Errorf("partial data not held in the temp file: err=%v", err)
	}

	release()
	if err := <-errCh; err == nil {
		t.Fatal("expected an error for a short body")
	}
	if _, err := os.Stat(segPath); !os.IsNotExist(err) {
		t.Errorf("failed segment published at the final path: %v", err)
	}
	if _, err := os.Stat(partPath); !os.IsNotExist(err) {
		t.Errorf("temp file not cleaned up: %v", err)
	}
}

func TestFetchSegmentNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_00000.ts")

	d := testDownloader(srv.Client())
	_, err := d.fetchSegment(context.Background(), srv.URL+"/seg0.ts", segPath)
	if err == nil {
		t.Fatal("expected an error for HTTP 404")
	}
	// Nothing is created before the status check.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("expected no files, got %d", len(entries))
	}
}

func TestHasCompleteSegment(t *testing.T) {
	dir := t.TempDir()

	t.Run("missing", func(t *testing.T) {
		if _, ok := hasCompleteSegment(filepath.Join(dir, "nope.ts")); ok {
			t.Error("missing file reported as complete")
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(dir, "empty.ts")
		if err := os.WriteFile(p, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if _, ok := hasCompleteSegment(p); ok {
			t.Error("zero-byte file reported as complete")
		}
	})

	t.Run("directory", func(t *testing.T) {
		p := filepath.Join(dir, "adir.ts")
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		if _, ok := hasCompleteSegment(p); ok {
			t.Error("directory reported as complete segment")
		}
	})

	t.Run("complete file", func(t *testing.T) {
		p := filepath.Join(dir, "good.ts")
		if err := os.WriteFile(p, []byte("0123456789"), 0644); err != nil {
			t.Fatal(err)
		}
		size, ok := hasCompleteSegment(p)
		if !ok {
			t.Fatal("complete file not recognized")
		}
		if size != 10 {
			t.Errorf("size = %d, want 10", size)
		}
	})
}

func TestHasCompleteSegmentIgnoresLeftoverPartFile(t *testing.T) {
	// A run killed mid-copy leaves only the ".part" file. Resume must not see
	// it as a finished segment, otherwise the truncated bytes get concatenated
	// into the episode and the corruption is reported as a success.
	dir := t.TempDir()
	segPath := filepath.Join(dir, "seg_00007.ts")
	if err := os.WriteFile(segmentPartPath(segPath), []byte("truncated-half-of-a-segment"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, ok := hasCompleteSegment(segPath); ok {
		t.Fatal("leftover .part file mistaken for a complete segment")
	}
}

func TestSegmentPartPathStaysInSegmentDir(t *testing.T) {
	// The rename is only atomic within a single filesystem, so the temp file
	// must be a sibling of the segment.
	segPath := filepath.Join("a", "b", "seg_00001.ts")
	part := segmentPartPath(segPath)
	if filepath.Dir(part) != filepath.Dir(segPath) {
		t.Errorf("part path %q not in segment dir %q", part, filepath.Dir(segPath))
	}
	if part == segPath {
		t.Error("part path must differ from the final segment path")
	}
	if strings.HasSuffix(part, ".ts") {
		t.Errorf("part path %q would be picked up as a segment file", part)
	}
}

func TestVariantFingerprintDistinguishesRenditions(t *testing.T) {
	v720 := Variant{URL: "https://cdn/720/index.m3u8", Resolution: "1280x720", Bandwidth: 2000000, Codecs: "avc1"}
	v1080 := Variant{URL: "https://cdn/1080/index.m3u8", Resolution: "1920x1080", Bandwidth: 4000000, Codecs: "avc1"}
	audioEN := AudioRendition{Name: "English", URI: "https://cdn/audio/en.m3u8"}
	audioRU := AudioRendition{Name: "Russian", URI: "https://cdn/audio/ru.m3u8"}

	base := variantFingerprint(v720, []AudioRendition{audioEN}, nil, false)

	t.Run("stable for identical input", func(t *testing.T) {
		if again := variantFingerprint(v720, []AudioRendition{audioEN}, nil, false); again != base {
			t.Errorf("fingerprint not stable:\n%q\n%q", base, again)
		}
	})

	t.Run("video variant change", func(t *testing.T) {
		if variantFingerprint(v1080, []AudioRendition{audioEN}, nil, false) == base {
			t.Error("720p and 1080p share a fingerprint")
		}
	})

	t.Run("bandwidth change at same url", func(t *testing.T) {
		// The CDN re-advertising BANDWIDTH is enough for SelectVariant to pick
		// differently, so it has to be part of the identity.
		rebranded := v720
		rebranded.Bandwidth = 2500000
		if variantFingerprint(rebranded, []AudioRendition{audioEN}, nil, false) == base {
			t.Error("bandwidth change did not alter the fingerprint")
		}
	})

	t.Run("audio track set change", func(t *testing.T) {
		if variantFingerprint(v720, []AudioRendition{audioRU}, nil, false) == base {
			t.Error("different audio rendition shares a fingerprint")
		}
		if variantFingerprint(v720, []AudioRendition{audioEN, audioRU}, nil, false) == base {
			t.Error("extra audio rendition shares a fingerprint")
		}
		if variantFingerprint(v720, nil, nil, false) == base {
			t.Error("dropping audio did not alter the fingerprint")
		}
	})

	t.Run("audio order change", func(t *testing.T) {
		// audio_0 / audio_1 directories are positional, so swapping the order
		// remaps cached segments onto the wrong track.
		a := variantFingerprint(v720, []AudioRendition{audioEN, audioRU}, nil, false)
		b := variantFingerprint(v720, []AudioRendition{audioRU, audioEN}, nil, false)
		if a == b {
			t.Error("audio order change did not alter the fingerprint")
		}
	})
}

func TestEnsureVariantMarkerFirstUse(t *testing.T) {
	dir := t.TempDir()
	wiped, err := ensureVariantMarker(dir, "fingerprint-a")
	if err != nil {
		t.Fatalf("ensureVariantMarker: %v", err)
	}
	if wiped {
		t.Error("fresh directory reported as a wipe")
	}
	got, err := os.ReadFile(filepath.Join(dir, variantMarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "fingerprint-a" {
		t.Errorf("marker = %q, want %q", got, "fingerprint-a")
	}
}

func TestEnsureVariantMarkerSameVariantKeepsCache(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureVariantMarker(dir, "fingerprint-a"); err != nil {
		t.Fatal(err)
	}

	// Simulate an interrupted run: some segments already on disk.
	segDir := filepath.Join(dir, "video")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatal(err)
	}
	segPath := filepath.Join(segDir, "seg_00000.ts")
	if err := os.WriteFile(segPath, []byte("cached"), 0644); err != nil {
		t.Fatal(err)
	}

	wiped, err := ensureVariantMarker(dir, "fingerprint-a")
	if err != nil {
		t.Fatalf("ensureVariantMarker: %v", err)
	}
	if wiped {
		t.Error("matching fingerprint should not wipe the cache")
	}
	if _, ok := hasCompleteSegment(segPath); !ok {
		t.Error("cached segment was removed despite a matching fingerprint")
	}
}

func TestEnsureVariantMarkerMismatchWipesCache(t *testing.T) {
	dir := t.TempDir()
	if _, err := ensureVariantMarker(dir, "fingerprint-720p"); err != nil {
		t.Fatal(err)
	}

	// A 720p run left video and audio segments plus a concatenated track file.
	for _, sub := range []string{"video", "audio_0"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "seg_00000.ts"), []byte("720p-bytes"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "audio_0.ts"), []byte("720p-audio"), 0644); err != nil {
		t.Fatal(err)
	}

	// Resuming as 1080p must not reuse any of it.
	wiped, err := ensureVariantMarker(dir, "fingerprint-1080p")
	if err != nil {
		t.Fatalf("ensureVariantMarker: %v", err)
	}
	if !wiped {
		t.Error("variant change was not reported as a wipe")
	}
	for _, stale := range []string{
		filepath.Join(dir, "video", "seg_00000.ts"),
		filepath.Join(dir, "audio_0", "seg_00000.ts"),
		filepath.Join(dir, "audio_0.ts"),
	} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Errorf("stale file %q survived the wipe: %v", stale, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, variantMarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "fingerprint-1080p" {
		t.Errorf("marker = %q, want the new fingerprint", got)
	}

	// A second resume with the same variant is now a no-op.
	wiped, err = ensureVariantMarker(dir, "fingerprint-1080p")
	if err != nil {
		t.Fatal(err)
	}
	if wiped {
		t.Error("second resume with the same variant should not wipe")
	}
}

func TestEnsureVariantMarkerUnmarkedCacheWipes(t *testing.T) {
	// A temp dir from before the marker existed (or with a corrupt marker)
	// carries no proof of which rendition it holds, so it cannot be trusted.
	dir := t.TempDir()
	segDir := filepath.Join(dir, "video")
	if err := os.MkdirAll(segDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "seg_00000.ts"), []byte("unknown-origin"), 0644); err != nil {
		t.Fatal(err)
	}

	wiped, err := ensureVariantMarker(dir, "fingerprint-a")
	if err != nil {
		t.Fatalf("ensureVariantMarker: %v", err)
	}
	if !wiped {
		t.Error("unmarked cache was not reported as a wipe")
	}
	if _, err := os.Stat(filepath.Join(segDir, "seg_00000.ts")); !os.IsNotExist(err) {
		t.Errorf("unmarked segment survived: %v", err)
	}
}

func TestEnsureVariantMarkerMissingDir(t *testing.T) {
	// The caller MkdirAlls first, but the helper must cope with a missing dir
	// rather than treating ReadDir's ENOENT as a failure.
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	wiped, err := ensureVariantMarker(dir, "fingerprint-a")
	if err != nil {
		t.Fatalf("ensureVariantMarker: %v", err)
	}
	if wiped {
		t.Error("missing directory reported as a wipe")
	}
	if _, err := os.Stat(filepath.Join(dir, variantMarkerName)); err != nil {
		t.Errorf("marker not written: %v", err)
	}
}

func TestDirHasEntries(t *testing.T) {
	dir := t.TempDir()
	if has, err := dirHasEntries(dir); err != nil || has {
		t.Errorf("empty dir: has=%v err=%v", has, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if has, err := dirHasEntries(dir); err != nil || !has {
		t.Errorf("non-empty dir: has=%v err=%v", has, err)
	}
	if has, err := dirHasEntries(filepath.Join(dir, "missing")); err != nil || has {
		t.Errorf("missing dir: has=%v err=%v", has, err)
	}
}

// hlsTestServer serves a two-variant master playlist, a media playlist per
// variant, and segments whose bodies name the variant they came from, so a test
// can tell which rendition ended up in the concatenated output.
type hlsTestServer struct {
	*httptest.Server

	// mu guards hits: segments are fetched concurrently.
	mu   sync.Mutex
	hits map[string]int
}

// hitCounts returns a snapshot of the per-segment request counts.
func (h *hlsTestServer) hitCounts() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]int, len(h.hits))
	for k, v := range h.hits {
		out[k] = v
	}
	return out
}

func newHLSTestServer(t *testing.T, segmentsPerVariant int) *hlsTestServer {
	t.Helper()
	h := &hlsTestServer{hits: map[string]int{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "#EXTM3U\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1280x720,CODECS=\"avc1.4d401f\"\n"+
			"720p/index.m3u8\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=4000000,RESOLUTION=1920x1080,CODECS=\"avc1.640028\"\n"+
			"1080p/index.m3u8\n")
	})

	for _, name := range []string{"720p", "1080p"} {
		variant := name
		mux.HandleFunc("/"+variant+"/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
			for i := 0; i < segmentsPerVariant; i++ {
				fmt.Fprintf(&b, "#EXTINF:6.0,\nseg%d.ts\n", i)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			_, _ = io.WriteString(w, b.String())
		})
		mux.HandleFunc("/"+variant+"/", func(w http.ResponseWriter, r *http.Request) {
			h.mu.Lock()
			h.hits[r.URL.Path]++
			h.mu.Unlock()
			fmt.Fprintf(w, "%s-payload-for-%s", variant, filepath.Base(r.URL.Path))
		})
	}

	h.Server = httptest.NewServer(mux)
	t.Cleanup(h.Close)
	return h
}

func TestDownloadEpisodeResumeReusesCompleteSegments(t *testing.T) {
	srv := newHLSTestServer(t, 3)
	d := testDownloader(srv.Client())

	outPath := filepath.Join(t.TempDir(), "ep.ts")
	key := domain.EpisodeKey{Season: 1, Episode: 2}

	res, err := d.downloadEpisodeInternal(context.Background(), srv.URL+"/master.m3u8", domain.Quality("720p"), outPath, key, nil)
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	if res.Resolution != "1280x720" {
		t.Fatalf("resolution = %q, want 1280x720", res.Resolution)
	}
	if n := len(srv.hitCounts()); n != 3 {
		t.Fatalf("fetched %d distinct segments, want 3", n)
	}

	// A leftover temp file from a kill mid-copy must be re-downloaded, not
	// adopted; the complete segments around it are reused.
	partPath := segmentPartPath(filepath.Join(outPath+".hls-tmp", "video", "seg_00001.ts"))
	if err := os.WriteFile(partPath, []byte("truncated"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := d.downloadEpisodeInternal(context.Background(), srv.URL+"/master.m3u8", domain.Quality("720p"), outPath, key, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Every segment was already complete, so the resume refetched none of them.
	for path, hits := range srv.hitCounts() {
		if hits != 1 {
			t.Errorf("segment %s fetched %d times, want 1 (resume should reuse)", path, hits)
		}
	}

	body, err := os.ReadFile(res.VideoPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "truncated") {
		t.Error("leftover .part content leaked into the concatenated video")
	}
	if strings.Contains(string(body), "1080p") {
		t.Error("unexpected 1080p payload in a 720p download")
	}
	if got := strings.Count(string(body), "720p-payload"); got != 3 {
		t.Errorf("concatenated %d segments, want 3", got)
	}
}

func TestDownloadEpisodeQualityChangeWipesCache(t *testing.T) {
	// The core of defect 2: an interrupted 720p run resumed at 1080p must not
	// splice the two renditions into one file.
	srv := newHLSTestServer(t, 3)
	d := testDownloader(srv.Client())

	outPath := filepath.Join(t.TempDir(), "ep.ts")
	key := domain.EpisodeKey{Season: 1, Episode: 1}

	if _, err := d.downloadEpisodeInternal(context.Background(), srv.URL+"/master.m3u8", domain.Quality("720p"), outPath, key, nil); err != nil {
		t.Fatalf("720p download: %v", err)
	}

	res, err := d.downloadEpisodeInternal(context.Background(), srv.URL+"/master.m3u8", domain.Quality("1080p"), outPath, key, nil)
	if err != nil {
		t.Fatalf("1080p resume: %v", err)
	}
	if res.Resolution != "1920x1080" {
		t.Fatalf("resolution = %q, want 1920x1080", res.Resolution)
	}

	body, err := os.ReadFile(res.VideoPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "720p-payload") {
		t.Error("720p segments were reused for a 1080p download (renditions spliced)")
	}
	if n := strings.Count(string(body), "1080p-payload"); n != 3 {
		t.Errorf("concatenated %d 1080p segments, want 3", n)
	}

	// The marker now names the 1080p rendition.
	marker, err := os.ReadFile(filepath.Join(outPath+".hls-tmp", variantMarkerName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(marker), "1920x1080") {
		t.Errorf("marker = %q, want the 1080p rendition", marker)
	}
}

func TestFormatHLSBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "0.5 KB"},
		{2 * 1024, "2.0 KB"},
		{3 * 1024 * 1024, "3.0 MB"},
		{2 * 1024 * 1024 * 1024, "2.0 GB"},
	}
	for _, c := range cases {
		if got := formatHLSBytes(c.in); got != c.want {
			t.Errorf("formatHLSBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
