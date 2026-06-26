package termuxapi

import (
	"errors"
	"sync"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// recordingReporter is a minimal domain.ProgressReporter that records every
// call it receives. It deliberately does NOT implement any of the optional
// sink interfaces so we can verify the "inner does not implement the sink"
// branch of the forwarding methods.
type recordingReporter struct {
	mu sync.Mutex

	starts      []domain.SeriesPlan
	epStarted   []domain.EpisodeKey
	trackCalls  []trackCall
	epCompleted []domain.EpisodeKey
	epFailed    []failCall
	stops       int
}

type trackCall struct {
	key     domain.EpisodeKey
	track   domain.TrackRef
	percent int
}

type failCall struct {
	key domain.EpisodeKey
	err error
}

func (r *recordingReporter) Start(plan domain.SeriesPlan) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, plan)
}

func (r *recordingReporter) EpisodeStarted(key domain.EpisodeKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epStarted = append(r.epStarted, key)
}

func (r *recordingReporter) TrackProgress(key domain.EpisodeKey, track domain.TrackRef, percent int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trackCalls = append(r.trackCalls, trackCall{key, track, percent})
}

func (r *recordingReporter) EpisodeCompleted(key domain.EpisodeKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epCompleted = append(r.epCompleted, key)
}

func (r *recordingReporter) EpisodeFailed(key domain.EpisodeKey, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.epFailed = append(r.epFailed, failCall{key, err})
}

func (r *recordingReporter) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
}

// sinkReporter embeds recordingReporter and additionally implements all three
// optional sink interfaces, recording each forwarded call.
type sinkReporter struct {
	recordingReporter

	mu        sync.Mutex
	hlsCalls  []hlsCall
	segCalls  []segCall
	byteCalls []byteCall
}

type hlsCall struct {
	key    domain.EpisodeKey
	tracks []domain.TrackProgressInfo
}

type segCall struct {
	key                     domain.EpisodeKey
	done, total             int
	downloaded, approxTotal int64
}

type byteCall struct {
	key             domain.EpisodeKey
	downloaded, tot int64
}

func (s *sinkReporter) HLSProgress(key domain.EpisodeKey, tracks []domain.TrackProgressInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hlsCalls = append(s.hlsCalls, hlsCall{key, tracks})
}

func (s *sinkReporter) SegmentProgress(key domain.EpisodeKey, done, total int, downloaded, approxTotal int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.segCalls = append(s.segCalls, segCall{key, done, total, downloaded, approxTotal})
}

func (s *sinkReporter) ByteProgress(key domain.EpisodeKey, downloaded, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byteCalls = append(s.byteCalls, byteCall{key, downloaded, total})
}

// Compile-time assertions that our mocks satisfy the intended interfaces.
var (
	_ domain.ProgressReporter    = (*recordingReporter)(nil)
	_ domain.ProgressReporter    = (*sinkReporter)(nil)
	_ domain.HLSProgressSink     = (*sinkReporter)(nil)
	_ domain.SegmentProgressSink = (*sinkReporter)(nil)
	_ domain.ByteProgressSink    = (*sinkReporter)(nil)
)

func TestWrapReturnsInnerWhenTermuxUnavailable(t *testing.T) {
	if Available() {
		t.Skip("termux-notification present in PATH; cannot assert unavailable branch")
	}
	inner := &recordingReporter{}
	got := Wrap(inner)
	if got != domain.ProgressReporter(inner) {
		t.Fatalf("Wrap should return inner unchanged when termux unavailable; got %T", got)
	}
}

func TestAvailableIsFalseInCleanEnv(t *testing.T) {
	// In a hermetic test environment the Termux:API binary is not installed.
	// We don't hard-fail if it happens to be present, but we do exercise the
	// function for coverage and document the expectation.
	if Available() {
		t.Skip("termux-notification unexpectedly present; skipping expectation")
	}
}

func TestStartForwardsAndStoresPlan(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}

	plan := domain.SeriesPlan{Title: "My Show", Total: 5}
	n.Start(plan)

	if len(inner.starts) != 1 || inner.starts[0].Title != plan.Title || inner.starts[0].Total != plan.Total {
		t.Fatalf("Start not forwarded to inner: %+v", inner.starts)
	}

	n.mu.Lock()
	gotTotal, gotTitle := n.total, n.seriesTitle
	n.mu.Unlock()
	if gotTotal != 5 {
		t.Errorf("total = %d, want 5", gotTotal)
	}
	if gotTitle != "My Show" {
		t.Errorf("seriesTitle = %q, want %q", gotTitle, "My Show")
	}
}

func TestEpisodeStartedForwardsAndStoresCurrent(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 1, Episode: 3}
	n.EpisodeStarted(key)

	if len(inner.epStarted) != 1 || inner.epStarted[0] != key {
		t.Fatalf("EpisodeStarted not forwarded: %+v", inner.epStarted)
	}
	if ep, _ := n.currentEp.Load().(string); ep != "S01E03" {
		t.Errorf("currentEp = %q, want %q", ep, "S01E03")
	}
	if pct := n.currentPct.Load(); pct != 0 {
		t.Errorf("currentPct = %d, want 0", pct)
	}
}

func TestTrackProgressForwardsAndStoresPct(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 2, Episode: 4}
	track := domain.TrackRef{Index: 1}
	n.TrackProgress(key, track, 57)

	if len(inner.trackCalls) != 1 {
		t.Fatalf("TrackProgress not forwarded: %+v", inner.trackCalls)
	}
	got := inner.trackCalls[0]
	if got.key != key || got.track != track || got.percent != 57 {
		t.Errorf("forwarded call = %+v, want key=%+v track=%+v pct=57", got, key, track)
	}
	if pct := n.currentPct.Load(); pct != 57 {
		t.Errorf("currentPct = %d, want 57", pct)
	}
}

func TestEpisodeCompletedForwardsAndIncrementsCounter(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}

	n.mu.Lock()
	n.total = 4
	n.seriesTitle = "Show"
	n.mu.Unlock()

	key := domain.EpisodeKey{Season: 1, Episode: 1}
	n.EpisodeCompleted(key)
	n.EpisodeCompleted(domain.EpisodeKey{Season: 1, Episode: 2})

	if len(inner.epCompleted) != 2 {
		t.Fatalf("EpisodeCompleted not forwarded twice: %+v", inner.epCompleted)
	}
	n.mu.Lock()
	done := n.completed
	n.mu.Unlock()
	if done != 2 {
		t.Errorf("completed = %d, want 2", done)
	}
}

func TestEpisodeCompletedWithZeroTotalDoesNotPanic(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}
	// total stays 0 -> pct computation must guard against divide-by-zero.
	n.EpisodeCompleted(domain.EpisodeKey{Season: 1, Episode: 1})

	n.mu.Lock()
	done := n.completed
	n.mu.Unlock()
	if done != 1 {
		t.Errorf("completed = %d, want 1", done)
	}
}

func TestEpisodeFailedForwards(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 3, Episode: 7}
	wantErr := errors.New("boom")
	n.EpisodeFailed(key, wantErr)

	if len(inner.epFailed) != 1 {
		t.Fatalf("EpisodeFailed not forwarded: %+v", inner.epFailed)
	}
	if inner.epFailed[0].key != key || !errors.Is(inner.epFailed[0].err, wantErr) {
		t.Errorf("forwarded fail = %+v, want key=%+v err=%v", inner.epFailed[0], key, wantErr)
	}
}

func TestStopForwardsAndSetsStoppedAndRemoves(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}
	// completed (0) < total (3) -> takes the notification-remove branch (no-op
	// without the binary), and forwards Stop to inner.
	n.mu.Lock()
	n.total = 3
	n.seriesTitle = "Show"
	n.mu.Unlock()

	n.Stop()

	if inner.stops != 1 {
		t.Errorf("inner.Stop calls = %d, want 1", inner.stops)
	}
	n.mu.Lock()
	stopped := n.stopped
	n.mu.Unlock()
	if !stopped {
		t.Error("stopped flag not set after Stop()")
	}
}

func TestStopAllCompletedBranch(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}
	// done >= total and done > 0 -> takes the "final success notification" branch.
	n.mu.Lock()
	n.total = 2
	n.completed = 2
	n.seriesTitle = "Done Show"
	n.mu.Unlock()

	n.Stop()

	if inner.stops != 1 {
		t.Errorf("inner.Stop calls = %d, want 1", inner.stops)
	}
	n.mu.Lock()
	stopped := n.stopped
	n.mu.Unlock()
	if !stopped {
		t.Error("stopped flag not set after Stop()")
	}
}

func TestHLSProgressForwardsWhenInnerImplementsSink(t *testing.T) {
	inner := &sinkReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 1, Episode: 1}
	tracks := []domain.TrackProgressInfo{{Label: "Video", DoneSegments: 2, TotalSegments: 10}}
	n.HLSProgress(key, tracks)

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.hlsCalls) != 1 {
		t.Fatalf("HLSProgress not forwarded: %+v", inner.hlsCalls)
	}
	if inner.hlsCalls[0].key != key || len(inner.hlsCalls[0].tracks) != 1 {
		t.Errorf("HLSProgress forwarded args = %+v, want key=%+v 1 track", inner.hlsCalls[0], key)
	}
}

func TestHLSProgressNoopWhenInnerLacksSink(t *testing.T) {
	inner := &recordingReporter{} // does not implement HLSProgressSink
	n := &Notifier{inner: inner}
	// Must not panic and must not affect any other recorded state.
	n.HLSProgress(domain.EpisodeKey{Season: 1, Episode: 1}, nil)
}

func TestSegmentProgressForwardsWhenInnerImplementsSink(t *testing.T) {
	inner := &sinkReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 1, Episode: 2}
	n.SegmentProgress(key, 3, 12, 1024, 4096)

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.segCalls) != 1 {
		t.Fatalf("SegmentProgress not forwarded: %+v", inner.segCalls)
	}
	got := inner.segCalls[0]
	want := segCall{key, 3, 12, 1024, 4096}
	if got != want {
		t.Errorf("SegmentProgress forwarded = %+v, want %+v", got, want)
	}
}

func TestSegmentProgressNoopWhenInnerLacksSink(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}
	n.SegmentProgress(domain.EpisodeKey{Season: 1, Episode: 1}, 1, 2, 3, 4)
}

func TestByteProgressForwardsWhenInnerImplementsSink(t *testing.T) {
	inner := &sinkReporter{}
	n := &Notifier{inner: inner}

	key := domain.EpisodeKey{Season: 4, Episode: 5}
	n.ByteProgress(key, 2048, 8192)

	inner.mu.Lock()
	defer inner.mu.Unlock()
	if len(inner.byteCalls) != 1 {
		t.Fatalf("ByteProgress not forwarded: %+v", inner.byteCalls)
	}
	got := inner.byteCalls[0]
	want := byteCall{key, 2048, 8192}
	if got != want {
		t.Errorf("ByteProgress forwarded = %+v, want %+v", got, want)
	}
}

func TestByteProgressNoopWhenInnerLacksSink(t *testing.T) {
	inner := &recordingReporter{}
	n := &Notifier{inner: inner}
	n.ByteProgress(domain.EpisodeKey{Season: 1, Episode: 1}, 1, 2)
}
