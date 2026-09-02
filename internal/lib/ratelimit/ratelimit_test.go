// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"1048576", 1048576, false},
		{"2M", 2 * 1024 * 1024, false},
		{"2m", 2 * 1024 * 1024, false},
		{"2MiB", 2 * 1024 * 1024, false},
		{"2M/s", 2 * 1024 * 1024, false},
		{"2MB/s", 2 * 1000 * 1000, false},
		{"500k", 500 * 1024, false},
		{"500kb", 500 * 1000, false},
		{"1.5m", int64(1.5 * 1024 * 1024), false},
		{"1G", 1024 * 1024 * 1024, false},
		{"  256k  ", 256 * 1024, false},
		{"-5", 0, true},
		{"abc", 0, true},
		{"12x", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseRate(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseRate(%q) = %d, want error", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRate(%q) unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRate(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// A nil limiter (disabled) must never block or wrap.
func TestNilLimiterIsUnlimited(t *testing.T) {
	var l *Limiter // NewLimiter(0) returns nil
	if got := NewLimiter(0); got != nil {
		t.Fatalf("NewLimiter(0) = %v, want nil", got)
	}
	if err := l.WaitN(context.Background(), 1<<20); err != nil {
		t.Errorf("nil WaitN: %v", err)
	}
}

// WaitN charges tokens against a controllable clock: the first burst is free,
// then further bytes must wait proportionally to the rate.
func TestLimiterWaitProportional(t *testing.T) {
	l := NewLimiter(1000) // 1000 B/s, burst 1000
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.last = now // align the bucket's clock with the frozen one

	// Draining the full burst is immediate.
	start := time.Now()
	if err := l.WaitN(context.Background(), 1000); err != nil {
		t.Fatalf("WaitN burst: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("burst WaitN blocked for %v, want ~0", elapsed)
	}

	// The next 500 bytes have no tokens and the clock is frozen, so the wait is
	// 500/1000 s = 500ms. Allow generous slack for scheduling.
	start = time.Now()
	if err := l.WaitN(context.Background(), 500); err != nil {
		t.Fatalf("WaitN deficit: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Errorf("deficit WaitN returned after %v, want >=~500ms", elapsed)
	}
}

func TestLimiterWaitCancellable(t *testing.T) {
	l := NewLimiter(1) // 1 B/s: any real wait is long
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	l.last = now
	_ = l.WaitN(context.Background(), 1) // drain the single token

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.WaitN(ctx, 100); err == nil {
		t.Error("WaitN with a cancelled context should return the context error")
	}
}

// Gate: permits are conserved through acquire/release even under penalize and
// recover, and the limit tracks penalties.
func TestGatePenalizeHalvesLimit(t *testing.T) {
	g := NewGate(8)
	if g.Limit() != 8 {
		t.Fatalf("initial limit = %d, want 8", g.Limit())
	}
	g.Penalize()
	if g.Limit() != 4 {
		t.Errorf("after one penalty limit = %d, want 4", g.Limit())
	}
	g.Penalize()
	g.Penalize()
	g.Penalize()
	if g.Limit() != 1 {
		t.Errorf("after repeated penalties limit = %d, want floor 1", g.Limit())
	}
}

func TestGateRecoversAfterCooldown(t *testing.T) {
	g := NewGate(4)
	now := time.Unix(1000, 0)
	g.now = func() time.Time { return now }
	g.recoverAfter = time.Second

	g.Penalize() // limit 4 -> 2
	if g.Limit() != 2 {
		t.Fatalf("limit = %d, want 2", g.Limit())
	}

	// Before the cooldown elapses, success does not widen the gate.
	g.ReportSuccess()
	if g.Limit() != 2 {
		t.Errorf("limit grew before cooldown: %d", g.Limit())
	}

	now = now.Add(2 * time.Second)
	g.ReportSuccess()
	if g.Limit() != 3 {
		t.Errorf("limit after cooldown = %d, want 3", g.Limit())
	}
}

// The core safety property: however the gate is penalized and recovered, the
// number of permits it can ever hand out simultaneously stays within [1, max],
// and acquire/release never deadlocks or leaks a permit.
func TestGatePermitsConserved(t *testing.T) {
	g := NewGate(4)
	now := time.Unix(0, 0)
	g.now = func() time.Time { return now }
	g.recoverAfter = 0

	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	inFlight, maxSeen := 0, 0

	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := g.Acquire(ctx); err != nil {
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > maxSeen {
				maxSeen = inFlight
			}
			mu.Unlock()

			if i%10 == 0 {
				g.Penalize()
			} else {
				g.ReportSuccess()
			}

			mu.Lock()
			inFlight--
			mu.Unlock()
			g.Release()
		}(i)
	}
	wg.Wait()

	if maxSeen > 4 {
		t.Errorf("max concurrent permits = %d, exceeds gate max 4", maxSeen)
	}
	// After everything drains, the gate must be able to hand out exactly its
	// current limit's worth of permits without blocking forever.
	got := 0
	for {
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		err := g.Acquire(short)
		cancel()
		if err != nil {
			break
		}
		got++
		if got > 4 {
			t.Fatal("gate handed out more than max permits after drain")
		}
	}
	if got < 1 {
		t.Error("gate handed out no permits after drain — a permit leaked")
	}
}
