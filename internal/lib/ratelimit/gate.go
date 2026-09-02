// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Gate is an adaptive concurrency limiter: it admits up to a current limit of
// holders at once, shrinks that limit when the CDN pushes back with 429, and
// lets it climb back to max once the pushback stops.
//
// It is a counting semaphore whose ceiling moves. Waiters block on a broadcast
// channel that is closed (and replaced) on every state change, which makes
// Acquire cancellable via context without the lost-wakeup hazards of a plain
// condition variable and without any blocking sends that could deadlock.
type Gate struct {
	max int

	mu          sync.Mutex
	limit       int           // holders currently admitted at once, in [1, max]
	active      int           // holders in flight
	broadcast   chan struct{} // closed to wake all waiters, then replaced
	lastPenalty time.Time

	recoverAfter time.Duration
	now          func() time.Time
}

// defaultRecoverAfter is how long the gate stays shrunk after a 429 before it
// starts widening again — long enough that a burst of 429s does not immediately
// undo the backoff.
const defaultRecoverAfter = 5 * time.Second

// NewGate returns a gate admitting up to max holders. max < 1 is treated as 1.
func NewGate(max int) *Gate {
	if max < 1 {
		max = 1
	}
	return &Gate{
		max:          max,
		limit:        max,
		broadcast:    make(chan struct{}),
		recoverAfter: defaultRecoverAfter,
		now:          time.Now,
	}
}

// Acquire admits one holder, blocking while the gate is at its current limit or
// until ctx is done.
func (g *Gate) Acquire(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.active < g.limit {
			g.active++
			g.mu.Unlock()
			return nil
		}
		wait := g.broadcast
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
			// State changed; re-check under the lock.
		}
	}
}

// Release returns a slot and wakes any waiters.
func (g *Gate) Release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.wake()
	g.mu.Unlock()
}

// Penalize halves the admission limit (floor 1) in response to a 429. Holders
// already in flight are not evicted; the lower limit takes hold as they finish.
func (g *Gate) Penalize() {
	g.mu.Lock()
	g.lastPenalty = g.now()
	if g.limit > 1 {
		g.limit = (g.limit + 1) / 2 // halve, rounding up so it never reaches 0
	}
	g.mu.Unlock()
	// No wake: shrinking never lets a blocked waiter proceed.
}

// ReportSuccess signals a segment finished without pushback. Once the cooldown
// since the last penalty has elapsed, it widens the gate by one holder until it
// is back to max. Calling it on every success is fine — it grows at most one
// step per cooldown window.
func (g *Gate) ReportSuccess() {
	g.mu.Lock()
	if g.limit < g.max && g.now().Sub(g.lastPenalty) >= g.recoverAfter {
		g.limit++
		g.wake() // a newly freed slot may unblock a waiter
	}
	g.mu.Unlock()
}

// wake releases every current waiter and installs a fresh channel for the next
// ones. Callers must hold g.mu.
func (g *Gate) wake() {
	close(g.broadcast)
	g.broadcast = make(chan struct{})
}

// Limit reports the current admission ceiling, for logging and tests.
func (g *Gate) Limit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}
