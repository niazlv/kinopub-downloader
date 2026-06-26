package progress

import (
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/termx"
)

// ---------------------------------------------------------------------------
// computePercent (extra cases beyond the property tests)
// ---------------------------------------------------------------------------

func TestComputePercent_Table(t *testing.T) {
	cases := []struct {
		name      string
		completed int
		total     int
		want      int
	}{
		{"zero total", 5, 0, 0},
		{"negative total", 5, -3, 0},
		{"none done", 0, 10, 0},
		{"half floored", 1, 3, 33},
		{"exact half", 5, 10, 50},
		{"full", 10, 10, 100},
		{"over full clamps to 100", 20, 10, 100},
		{"negative completed clamps to 0", -5, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computePercent(tc.completed, tc.total); got != tc.want {
				t.Errorf("computePercent(%d, %d) = %d, want %d", tc.completed, tc.total, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// clampPercent
// ---------------------------------------------------------------------------

func TestClampPercent_Table(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-100, 0},
		{-1, 0},
		{0, 0},
		{50, 50},
		{100, 100},
		{101, 100},
		{1000, 100},
	}
	for _, tc := range cases {
		if got := clampPercent(tc.in); got != tc.want {
			t.Errorf("clampPercent(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// clampInt
// ---------------------------------------------------------------------------

func TestClampInt_Table(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{5, 0, 10, 5},
		{-5, 0, 10, 0},
		{15, 0, 10, 10},
		{0, 0, 10, 0},
		{10, 0, 10, 10},
		{14, 14, 42, 14},  // lower bound for labelCol
		{42, 14, 42, 42},  // upper bound for labelCol
		{100, 14, 42, 42}, // above upper
	}
	for _, tc := range cases {
		if got := clampInt(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("clampInt(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// computeLayout (narrow / normal / wide)
// ---------------------------------------------------------------------------

func TestComputeLayout(t *testing.T) {
	cases := []struct {
		name         string
		termWidth    int
		wantLabelCol int
		wantBarWidth int
	}{
		// Narrow: clamps to lower bounds.
		{"narrow", 20, 14, 10},
		{"very narrow zero", 0, 14, 10},
		// Normal: in the middle of both ranges.
		// width=80 -> labelCol=80*2/5=32, barWidth=80/5=16
		{"normal80", 80, 32, 16},
		// width=100 -> labelCol=40, barWidth=20
		{"normal100", 100, 40, 20},
		// Wide: clamps to upper bounds.
		// width=200 -> labelCol=80->42, barWidth=40->20
		{"wide", 200, 42, 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lay := computeLayout(tc.termWidth)
			if lay.width != tc.termWidth {
				t.Errorf("width = %d, want %d", lay.width, tc.termWidth)
			}
			if lay.labelCol != tc.wantLabelCol {
				t.Errorf("labelCol = %d, want %d", lay.labelCol, tc.wantLabelCol)
			}
			if lay.barWidth != tc.wantBarWidth {
				t.Errorf("barWidth = %d, want %d", lay.barWidth, tc.wantBarWidth)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatBytesShort (B / K / M / G boundaries)
// ---------------------------------------------------------------------------

func TestFormatBytesShort_Boundaries(t *testing.T) {
	const (
		KB = 1024.0
		MB = 1024.0 * KB
		GB = 1024.0 * MB
	)
	cases := []struct {
		name string
		in   float64
		want string
	}{
		{"zero", 0, "0B"},
		{"bytes", 512, "512B"},
		{"just below KB", 1023, "1023B"},
		{"exactly KB", KB, "1K"},
		{"between KB and MB", 1536, "2K"}, // 1.5K rounds to 2 with %.0f
		{"just below MB", MB - 1, "1024K"},
		{"exactly MB", MB, "1.0M"},
		{"1.5 MB", 1.5 * MB, "1.5M"},
		{"just below GB", GB - 1, "1024.0M"},
		{"exactly GB", GB, "1.0G"},
		{"2.5 GB", 2.5 * GB, "2.5G"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBytesShort(tc.in); got != tc.want {
				t.Errorf("formatBytesShort(%g) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// formatDuration (sec / min / hour, negative -> 0s)
// ---------------------------------------------------------------------------

func TestFormatDuration_Table(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"negative", -5 * time.Second, "0s"},
		{"zero", 0, "0s"},
		{"seconds", 45 * time.Second, "45s"},
		{"just under minute", 59 * time.Second, "59s"},
		{"exact minute", 60 * time.Second, "1m"},
		{"minutes and seconds", 90 * time.Second, "1m30s"},
		{"two and a half minutes", 150 * time.Second, "2m30s"},
		{"exact ten minutes", 10 * time.Minute, "10m"},
		{"just under hour", 59*time.Minute + 59*time.Second, "59m59s"},
		{"exact hour", time.Hour, "1h0m"},
		{"hour and minutes", time.Hour + 30*time.Minute, "1h30m"},
		{"rounds sub-second up", 1500 * time.Millisecond, "2s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatDuration(tc.in); got != tc.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// displayWidth (ASCII + Cyrillic)
// ---------------------------------------------------------------------------

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "hello", 5},
		{"cyrillic", "Серия", 5},
		{"mixed", "Серия 1", 7},
		{"ellipsis rune", "…", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayWidth(tc.in); got != tc.want {
				t.Errorf("displayWidth(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// padOrClip (ASCII + Cyrillic, cols<=0, cols==1 ellipsis, exact, longer)
// ---------------------------------------------------------------------------

func TestPadOrClip(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want string
	}{
		{"cols zero", "hello", 0, ""},
		{"cols negative", "hello", -3, ""},
		{"exact ascii", "hello", 5, "hello"},
		{"pad ascii", "hi", 5, "hi   "},
		{"clip ascii", "hello world", 8, "hello w…"},
		{"cols one clips to ellipsis", "hello", 1, "…"},
		{"cols one empty input pads", "", 1, " "}, // len 0 < 1 -> pad with one space
		{"exact cyrillic", "Серия", 5, "Серия"},
		{"pad cyrillic", "Тест", 6, "Тест  "},
		{"clip cyrillic", "Длинный текст", 5, "Длин…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := padOrClip(tc.in, tc.cols)
			if got != tc.want {
				t.Errorf("padOrClip(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
			}
			// Pad/clip must yield exactly cols runes when cols > 0.
			if tc.cols > 0 {
				if n := len([]rune(got)); n != tc.cols {
					t.Errorf("padOrClip(%q, %d) rune len = %d, want %d", tc.in, tc.cols, n, tc.cols)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// truncateText (ASCII + Cyrillic, cols<=0, cols==1 ellipsis, exact, longer)
// ---------------------------------------------------------------------------

func TestTruncateText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cols int
		want string
	}{
		{"cols zero", "hello", 0, ""},
		{"cols negative", "hello", -2, ""},
		{"shorter than cols", "hi", 5, "hi"},
		{"exact", "hello", 5, "hello"},
		{"longer ascii", "hello world", 8, "hello w…"},
		{"cols one", "hello", 1, "…"},
		{"shorter cyrillic", "Тест", 10, "Тест"},
		{"exact cyrillic", "Серия", 5, "Серия"},
		{"longer cyrillic", "Длинный текст здесь", 5, "Длин…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateText(tc.in, tc.cols); got != tc.want {
				t.Errorf("truncateText(%q, %d) = %q, want %q", tc.in, tc.cols, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// seriesPercent
// ---------------------------------------------------------------------------

func TestSeriesPercent(t *testing.T) {
	r := &LiveReporter{}
	// Total == 0 -> 0 regardless of completed.
	r.plan.Total = 0
	r.completedTotal = 5
	if got := r.seriesPercent(); got != 0 {
		t.Errorf("seriesPercent with zero total = %d, want 0", got)
	}

	r.plan.Total = 10
	r.completedTotal = 5
	if got := r.seriesPercent(); got != 50 {
		t.Errorf("seriesPercent(5/10) = %d, want 50", got)
	}

	r.completedTotal = 10
	if got := r.seriesPercent(); got != 100 {
		t.Errorf("seriesPercent(10/10) = %d, want 100", got)
	}

	// Over-complete clamps to 100.
	r.completedTotal = 25
	if got := r.seriesPercent(); got != 100 {
		t.Errorf("seriesPercent(25/10) = %d, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// episodePercent
// ---------------------------------------------------------------------------

func TestEpisodePercent(t *testing.T) {
	r := &LiveReporter{}

	// No tracks -> 0.
	emptyEp := &episodeState{tracks: map[domain.TrackRef]int{}}
	if got := r.episodePercent(emptyEp); got != 0 {
		t.Errorf("episodePercent with no tracks = %d, want 0", got)
	}

	// Single track at 50%.
	oneEp := &episodeState{tracks: map[domain.TrackRef]int{
		{Kind: domain.TrackVideo, Index: 0}: 50,
	}}
	if got := r.episodePercent(oneEp); got != 50 {
		t.Errorf("episodePercent single 50%% = %d, want 50", got)
	}

	// Two tracks: 100 and 0 -> average 50.
	twoEp := &episodeState{tracks: map[domain.TrackRef]int{
		{Kind: domain.TrackVideo, Index: 0}: 100,
		{Kind: domain.TrackAudio, Index: 0}: 0,
	}}
	if got := r.episodePercent(twoEp); got != 50 {
		t.Errorf("episodePercent 100&0 = %d, want 50", got)
	}

	// Two tracks: both 100 -> 100.
	fullEp := &episodeState{tracks: map[domain.TrackRef]int{
		{Kind: domain.TrackVideo, Index: 0}: 100,
		{Kind: domain.TrackAudio, Index: 0}: 100,
	}}
	if got := r.episodePercent(fullEp); got != 100 {
		t.Errorf("episodePercent both 100 = %d, want 100", got)
	}

	// Three tracks: 33+33+33 = 99 / 300 = 33 (floored).
	threeEp := &episodeState{tracks: map[domain.TrackRef]int{
		{Kind: domain.TrackVideo, Index: 0}: 33,
		{Kind: domain.TrackAudio, Index: 0}: 33,
		{Kind: domain.TrackAudio, Index: 1}: 34,
	}}
	if got := r.episodePercent(threeEp); got != 33 {
		t.Errorf("episodePercent 33/33/34 = %d, want 33", got)
	}
}

// ---------------------------------------------------------------------------
// progressBar (0 / 50 / 100, clamping)
// ---------------------------------------------------------------------------

func TestProgressBar(t *testing.T) {
	// Non-TTY reporter so colorize is a no-op and output is plain.
	r := &LiveReporter{isTTY: false}

	cases := []struct {
		name       string
		percent    int
		width      int
		wantFilled int
		wantPct    string
	}{
		{"zero", 0, 10, 0, "   0%"},
		{"fifty", 50, 10, 5, "  50%"},
		{"hundred", 100, 10, 10, " 100%"},
		{"negative clamps to 0", -20, 10, 0, "   0%"},
		{"over clamps to 100", 250, 10, 10, " 100%"},
		{"forty five floors", 45, 20, 9, "  45%"}, // 45*20/100 = 9
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.progressBar(tc.percent, tc.width, "")
			filled := strings.Count(got, "█")
			empty := strings.Count(got, "░")
			if filled != tc.wantFilled {
				t.Errorf("progressBar(%d,%d) filled = %d, want %d", tc.percent, tc.width, filled, tc.wantFilled)
			}
			if filled+empty != tc.width {
				t.Errorf("progressBar(%d,%d) total cells = %d, want %d", tc.percent, tc.width, filled+empty, tc.width)
			}
			if !strings.HasSuffix(got, tc.wantPct) {
				t.Errorf("progressBar(%d,%d) = %q, want suffix %q", tc.percent, tc.width, got, tc.wantPct)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// episodeState.sampleSpeed (seed, positive window, zero-byte decay)
// ---------------------------------------------------------------------------

func TestSampleSpeed_Seed(t *testing.T) {
	ep := &episodeState{}
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	// First call seeds without producing a sample.
	ep.sampleSpeed(0, base)
	if ep.speed != 0 {
		t.Errorf("after seed, speed = %v, want 0", ep.speed)
	}
	if !ep.lastSpeedTime.Equal(base) {
		t.Errorf("after seed, lastSpeedTime = %v, want %v", ep.lastSpeedTime, base)
	}
	if ep.lastSpeedBytes != 0 {
		t.Errorf("after seed, lastSpeedBytes = %d, want 0", ep.lastSpeedBytes)
	}
}

func TestSampleSpeed_BelowWindowIgnored(t *testing.T) {
	ep := &episodeState{}
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	ep.sampleSpeed(0, base)
	// Less than 1s elapsed -> no update.
	ep.sampleSpeed(1_000_000, base.Add(500*time.Millisecond))
	if ep.speed != 0 {
		t.Errorf("sub-window call updated speed = %v, want 0", ep.speed)
	}
	// lastSpeedTime/lastSpeedBytes unchanged from seed.
	if !ep.lastSpeedTime.Equal(base) {
		t.Errorf("lastSpeedTime changed = %v, want %v", ep.lastSpeedTime, base)
	}
}

func TestSampleSpeed_PositiveWindow(t *testing.T) {
	ep := &episodeState{}
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	ep.sampleSpeed(0, base)
	// 1MiB over exactly 1 second -> instantSpeed ~ 1MiB/s, speed seeded to it.
	ep.sampleSpeed(1024*1024, base.Add(1*time.Second))
	if ep.speed <= 0 {
		t.Fatalf("speed after positive window = %v, want > 0", ep.speed)
	}
	wantInstant := float64(1024 * 1024)
	if ep.speed != wantInstant {
		t.Errorf("first-sample speed = %v, want %v", ep.speed, wantInstant)
	}
	if ep.lastSpeedBytes != 1024*1024 {
		t.Errorf("lastSpeedBytes = %d, want %d", ep.lastSpeedBytes, 1024*1024)
	}

	// A second positive window applies the EMA: speed*0.7 + instant*0.3.
	prev := ep.speed
	ep.sampleSpeed(1024*1024+512*1024, base.Add(2*time.Second)) // +512KiB over 1s
	instant2 := float64(512 * 1024)
	wantEMA := prev*0.7 + instant2*0.3
	if ep.speed != wantEMA {
		t.Errorf("EMA speed = %v, want %v", ep.speed, wantEMA)
	}
}

func TestSampleSpeed_ZeroByteWindowDecays(t *testing.T) {
	ep := &episodeState{}
	base := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	ep.sampleSpeed(0, base)
	// Build up a high speed: 10MiB in 1s.
	ep.sampleSpeed(10*1024*1024, base.Add(1*time.Second))
	high := ep.speed
	if high <= 0 {
		t.Fatalf("setup speed = %v, want > 0", high)
	}

	// Zero-byte window: same byte count over 1s -> decays toward 0.
	ep.sampleSpeed(10*1024*1024, base.Add(2*time.Second))
	if ep.speed >= high {
		t.Errorf("zero-byte window speed = %v, want < %v (decayed)", ep.speed, high)
	}
	if ep.speed < 0 {
		t.Errorf("speed went negative: %v", ep.speed)
	}

	// Repeated zero-byte windows eventually clamp to exactly 0 (below 1024).
	for i := 3; i < 30 && ep.speed != 0; i++ {
		ep.sampleSpeed(10*1024*1024, base.Add(time.Duration(i)*time.Second))
	}
	if ep.speed != 0 {
		t.Errorf("after repeated zero-byte windows, speed = %v, want 0", ep.speed)
	}
}

// ---------------------------------------------------------------------------
// colorize / repeatChar (small pure helpers)
// ---------------------------------------------------------------------------

func TestColorize(t *testing.T) {
	// Non-TTY: returns text unchanged.
	noTTY := &LiveReporter{isTTY: false}
	if got := noTTY.colorize(termx.Red, "x"); got != "x" {
		t.Errorf("non-TTY colorize = %q, want %q", got, "x")
	}

	// TTY: wraps in color + reset.
	tty := &LiveReporter{isTTY: true}
	got := tty.colorize(termx.Red, "x")
	want := termx.Red + "x" + termx.Reset
	if got != want {
		t.Errorf("TTY colorize = %q, want %q", got, want)
	}
}

func TestRepeatChar(t *testing.T) {
	r := &LiveReporter{}
	cases := []struct {
		ch   rune
		n    int
		want string
	}{
		{'-', 0, ""},
		{'-', -5, ""},
		{'-', 3, "---"},
		{'─', 2, "──"},
	}
	for _, tc := range cases {
		if got := r.repeatChar(tc.ch, tc.n); got != tc.want {
			t.Errorf("repeatChar(%q, %d) = %q, want %q", tc.ch, tc.n, got, tc.want)
		}
	}
}
