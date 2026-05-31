// Package downloader — chunked.go implements resumable HTTP Range-based
// downloading for progressive MP4 sources. It downloads the raw video file
// in chunks, resuming from where it left off on failure. After the raw file
// is fully downloaded, ffmpeg remuxes it into the final container with
// metadata, poster, and audio/subtitle labels.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"kinopub_downloader/internal/domain"
)

const (
	// defaultChunkSize is the size of each HTTP Range request (10 MB).
	defaultChunkSize = 10 * 1024 * 1024

	// maxResumeAttempts is the maximum number of retry attempts for a single
	// chunk before giving up.
	maxResumeAttempts = 10

	// retryBaseDelay is the initial delay between retries (doubles each attempt).
	retryBaseDelay = 3 * time.Second
)

// ChunkedDownloader downloads files using HTTP Range requests with resume
// capability. It writes chunks to a partial file (.part) and can resume
// from where it left off if interrupted.
type ChunkedDownloader struct {
	client *http.Client
	auth   domain.RequestAuth
	logger domain.Logger
}

// NewChunked creates a ChunkedDownloader with the given HTTP client and auth.
// The client's Timeout is cleared internally because chunked downloads need
// per-chunk timeouts (via context), not a global request timeout that would
// fire on large chunks over slow connections.
func NewChunked(client *http.Client, auth domain.RequestAuth, logger domain.Logger) *ChunkedDownloader {
	// Create a copy of the client without the global Timeout.
	// The global Timeout covers the entire request lifecycle including body
	// reading, which is too restrictive for large chunk downloads.
	noTimeoutClient := *client
	noTimeoutClient.Timeout = 0

	return &ChunkedDownloader{
		client: &noTimeoutClient,
		auth:   auth,
		logger: logger.Component("chunked"),
	}
}

// CanHandle reports whether this source can be downloaded via chunked HTTP.
// Only progressive (direct MP4/MKV) sources with a known URL are supported.
// HLS sources require segment-by-segment handling which is not implemented.
func (c *ChunkedDownloader) CanHandle(media domain.ResolvedMedia) bool {
	return media.Source.Kind == domain.MediaProgressive && media.Source.URL != ""
}

// Download fetches the raw media file using HTTP Range requests with resume.
// It writes to outPath+".part" during download and renames to outPath on
// completion. The sink receives progress updates based on bytes downloaded.
//
// Returns nil on success. On failure, the .part file is preserved for resume
// on the next attempt.
func (c *ChunkedDownloader) Download(ctx context.Context, url string, outPath string, key domain.EpisodeKey, sink domain.ProgressSink) error {
	partPath := outPath + ".part"

	// 1. Determine total file size via HEAD request.
	totalSize, supportsRange, err := c.probeSize(ctx, url)
	if err != nil {
		return fmt.Errorf("chunked probe: %w", err)
	}

	if !supportsRange || totalSize <= 0 {
		return fmt.Errorf("server does not support Range requests or unknown content length")
	}

	c.logger.Info("chunked download starting",
		domain.F("episode", fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
		domain.F("total_size", formatBytes(totalSize)),
		domain.F("url", truncateURL(url)),
	)

	track := domain.TrackRef{Kind: domain.TrackVideo, Index: 0}

	// 2. Check if we have a partial file from a previous attempt.
	var offset int64
	if info, err := os.Stat(partPath); err == nil {
		offset = info.Size()
		if offset >= totalSize {
			// File is already complete — just rename.
			c.logger.Info("partial file already complete, renaming",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
			)
			// Report 100% before rename.
			if sink != nil {
				sink.TrackProgress(key, track, 100)
				if byteSink, ok := sink.(domain.ByteProgressSink); ok {
					byteSink.ByteProgress(key, totalSize, totalSize)
				}
			}
			return os.Rename(partPath, outPath)
		}
		// Sanity check: if the partial file is larger than the total size
		// reported by the server (e.g., URL changed to a different file),
		// discard the partial and start fresh.
		if offset > totalSize {
			c.logger.Warn("partial file larger than server reports, starting fresh",
				domain.F("partial_size", offset),
				domain.F("server_size", totalSize),
			)
			os.Remove(partPath)
			offset = 0
		} else {
			c.logger.Info("resuming from partial file",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
				domain.F("offset", formatBytes(offset)),
				domain.F("remaining", formatBytes(totalSize-offset)),
			)
			// Report initial progress from the existing partial file.
			if sink != nil && totalSize > 0 {
				pct := int(offset * 100 / totalSize)
				sink.TrackProgress(key, track, pct)
				if byteSink, ok := sink.(domain.ByteProgressSink); ok {
					byteSink.ByteProgress(key, offset, totalSize)
				}
			}
		}
	}

	// 3. Open the partial file for appending.
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open part file: %w", err)
	}

	// Seek to the current offset.
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return fmt.Errorf("seek part file: %w", err)
		}
	}

	// 4. Download in chunks with retry.
	consecutiveFailures := 0
	hasStarted := offset > 0 // true if we already have data (resume)

	for offset < totalSize {
		if ctx.Err() != nil {
			f.Close()
			return ctx.Err()
		}

		// Calculate chunk range.
		end := offset + defaultChunkSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}

		// Download this chunk with retries.
		n, err := c.downloadChunk(ctx, f, url, offset, end)
		if err != nil {
			// If we haven't successfully downloaded any chunk yet in this
			// session, fail fast — the URL is likely expired or CDN is blocking.
			// The caller (engine) can re-resolve and retry with a fresh URL.
			if !hasStarted {
				f.Close()
				return fmt.Errorf("initial chunk failed (URL may be expired): %w", err)
			}

			consecutiveFailures++
			if consecutiveFailures >= maxResumeAttempts {
				f.Close()
				return fmt.Errorf("chunked download failed after %d consecutive errors at offset %d: %w",
					maxResumeAttempts, offset, err)
			}

			c.logger.Warn("chunk download failed, retrying",
				domain.F("episode", fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
				domain.F("offset", offset),
				domain.F("attempt", consecutiveFailures),
				domain.F("error", err.Error()),
			)

			// Exponential backoff.
			delay := retryBaseDelay * time.Duration(1<<(consecutiveFailures-1))
			if delay > 60*time.Second {
				delay = 60 * time.Second
			}
			select {
			case <-ctx.Done():
				f.Close()
				return ctx.Err()
			case <-time.After(delay):
			}

			// Re-seek to the correct position (in case partial write happened).
			currentSize, _ := f.Seek(0, io.SeekEnd)
			offset = currentSize
			continue
		}

		// Success — advance offset, reset failure counter.
		offset += n
		consecutiveFailures = 0
		hasStarted = true

		// Report progress.
		if sink != nil && totalSize > 0 {
			pct := int(offset * 100 / totalSize)
			if pct > 100 {
				pct = 100
			}
			sink.TrackProgress(key, track, pct)

			// Report byte-level progress if the sink supports it.
			if byteSink, ok := sink.(domain.ByteProgressSink); ok {
				byteSink.ByteProgress(key, offset, totalSize)
			}
		}
	}

	// 5. Close and verify.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close part file: %w", err)
	}

	info, err := os.Stat(partPath)
	if err != nil {
		return fmt.Errorf("stat part file: %w", err)
	}
	if info.Size() != totalSize {
		return fmt.Errorf("size mismatch: got %d, expected %d", info.Size(), totalSize)
	}

	// 6. Rename .part → final raw path.
	if err := os.Rename(partPath, outPath); err != nil {
		return fmt.Errorf("rename part to final: %w", err)
	}

	c.logger.Info("chunked download complete",
		domain.F("episode", fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)),
		domain.F("size", formatBytes(totalSize)),
	)

	return nil
}

// probeSize sends a HEAD request to determine the file size and whether
// the server supports Range requests. Uses a short timeout since this is
// just a probe — if the server doesn't respond quickly, the URL is likely dead.
func (c *ChunkedDownloader) probeSize(ctx context.Context, url string) (int64, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false, err
	}
	c.applyAuth(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("HEAD returned HTTP %d", resp.StatusCode)
	}

	// Check Accept-Ranges header.
	supportsRange := strings.Contains(resp.Header.Get("Accept-Ranges"), "bytes")

	// Content-Length gives us the total size.
	size := resp.ContentLength
	if size <= 0 {
		// Try parsing Content-Length header directly.
		clStr := resp.Header.Get("Content-Length")
		if clStr != "" {
			size, _ = strconv.ParseInt(clStr, 10, 64)
		}
	}

	// Even if Accept-Ranges is not explicitly set, many CDNs support it.
	// We'll try Range requests anyway and fall back if they fail.
	if !supportsRange && size > 0 {
		supportsRange = true // optimistic — will fail gracefully in downloadChunk
	}

	return size, supportsRange, nil
}

// downloadChunk downloads bytes [start, end] from url and writes them to w.
// Returns the number of bytes written. Uses a per-chunk timeout proportional
// to the chunk size (minimum 60s, allows ~170KB/s minimum speed).
func (c *ChunkedDownloader) downloadChunk(ctx context.Context, w io.WriterAt, url string, start, end int64) (int64, error) {
	chunkSize := end - start + 1
	// Timeout: at least 60s, or 1 second per 500KB (allows ~500KB/s minimum).
	timeout := time.Duration(chunkSize/500000+1) * time.Second
	if timeout < 60*time.Second {
		timeout = 60 * time.Second
	}

	chunkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(chunkCtx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	c.applyAuth(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	// Accept both 206 Partial Content and 200 OK (some servers ignore Range).
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Expected — server supports Range.
	case http.StatusOK:
		// Server ignored Range header — cannot do chunked download.
		return 0, fmt.Errorf("server returned 200 instead of 206 (Range not supported)")
	case http.StatusRequestedRangeNotSatisfiable:
		// We're past the end — file is complete.
		return 0, nil
	default:
		return 0, fmt.Errorf("chunk request returned HTTP %d", resp.StatusCode)
	}

	// Write the response body at the correct offset.
	// We use a SectionWriter pattern via WriteAt.
	written, err := copyAt(w, resp.Body, start)
	if err != nil {
		return written, err
	}

	return written, nil
}

// copyAt copies from r to w starting at offset, returning bytes written.
func copyAt(w io.WriterAt, r io.Reader, offset int64) (int64, error) {
	buf := make([]byte, 32*1024) // 32KB buffer
	var total int64

	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			nw, writeErr := w.WriteAt(buf[:n], offset+total)
			total += int64(nw)
			if writeErr != nil {
				return total, writeErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// applyAuth sets authentication headers on the request.
func (c *ChunkedDownloader) applyAuth(req *http.Request) {
	if c.auth.UserAgent != "" {
		req.Header.Set("User-Agent", c.auth.UserAgent)
	}
	if c.auth.Cookie != "" {
		req.Header.Set("Cookie", c.auth.Cookie)
	}
	for k, v := range c.auth.Headers {
		req.Header.Set(k, v)
	}
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// truncateURL shortens a URL for logging (removes query params).
func truncateURL(url string) string {
	if idx := strings.Index(url, "?"); idx > 0 {
		return url[:idx] + "?..."
	}
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
}
