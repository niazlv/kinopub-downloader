// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package ratelimit

import (
	"context"
	"io"
	"sync"
	"time"
)

// readChunk bounds how many bytes a limited Read pulls at once, so throttling
// stays smooth rather than arriving in one large burst per segment.
const readChunk = 32 * 1024

// Limiter is a token-bucket byte-rate limiter shared across every segment
// stream, so --limit-rate caps aggregate throughput rather than per-connection
// throughput. A nil *Limiter is unlimited, which is the disabled case.
type Limiter struct {
	rate  float64 // bytes per second
	burst float64

	mu     sync.Mutex
	tokens float64
	last   time.Time

	// now is overridable in tests; production uses time.Now.
	now func() time.Time
}

// NewLimiter returns a limiter capping throughput at bytesPerSec. A value <= 0
// returns nil, i.e. no limit — callers treat a nil *Limiter as "unlimited".
func NewLimiter(bytesPerSec int64) *Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	r := float64(bytesPerSec)
	return &Limiter{
		rate:   r,
		burst:  r, // one second of burst
		tokens: r,
		last:   time.Now(),
		now:    time.Now,
	}
}

// WaitN blocks until n bytes' worth of tokens have been consumed, or ctx ends.
// n larger than the burst is charged in full — the caller reads in readChunk
// pieces, so n never exceeds the burst in practice.
func (l *Limiter) WaitN(ctx context.Context, n int) error {
	if l == nil || n <= 0 {
		return nil
	}

	l.mu.Lock()
	now := l.now()
	l.tokens += now.Sub(l.last).Seconds() * l.rate
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
	l.last = now

	l.tokens -= float64(n)
	deficit := l.tokens
	l.mu.Unlock()

	if deficit >= 0 {
		return nil
	}

	// Sleep long enough for the borrowed tokens to be refilled. Concurrent
	// waiters each compute against the shared (now negative) balance, so the
	// aggregate rate converges on the configured limit.
	wait := time.Duration(-deficit / l.rate * float64(time.Second))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Reader wraps r so reads are paced to the limit. A nil *Limiter returns r
// unchanged.
func (l *Limiter) Reader(ctx context.Context, r io.Reader) io.Reader {
	if l == nil {
		return r
	}
	return &limitedReader{ctx: ctx, r: r, l: l}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if len(p) > readChunk {
		p = p[:readChunk]
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		// Charge for what was actually read, throttling the following read.
		if werr := lr.l.WaitN(lr.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}
