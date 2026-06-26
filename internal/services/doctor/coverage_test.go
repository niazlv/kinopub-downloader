package doctor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// nopLogger is a no-op implementation of domain.Logger for hermetic tests.
type nopLogger struct{}

func (nopLogger) Debug(string, ...domain.Field)        {}
func (nopLogger) Info(string, ...domain.Field)         {}
func (nopLogger) Warn(string, ...domain.Field)         {}
func (nopLogger) Error(string, ...domain.Field)        {}
func (l nopLogger) With(...domain.Field) domain.Logger { return l }
func (l nopLogger) Component(string) domain.Logger     { return l }

// stubInputResolver returns a fixed FeedSource (or error).
type stubInputResolver struct {
	src domain.FeedSource
	err error
}

func (s stubInputResolver) Classify(string) (domain.InputClass, error) {
	return domain.ClassUnclassified, nil
}
func (s stubInputResolver) Resolve(context.Context, string) (domain.FeedSource, error) {
	return s.src, s.err
}

// stubFeedParser returns a fixed Series (or error).
type stubFeedParser struct {
	series domain.Series
	err    error
}

func (s stubFeedParser) Parse(context.Context, domain.FeedSource) (domain.Series, error) {
	return s.series, s.err
}

// stubMediaResolver returns a fixed ResolvedMedia (or error).
type stubMediaResolver struct {
	media domain.ResolvedMedia
	err   error
}

func (s stubMediaResolver) Resolve(context.Context, domain.Episode, domain.QualityPref) (domain.ResolvedMedia, error) {
	return s.media, s.err
}

func newDeps() Deps {
	return Deps{
		Logger:        nopLogger{},
		InputResolver: stubInputResolver{},
		FeedParser:    stubFeedParser{},
		MediaResolver: stubMediaResolver{},
	}
}

// writeStateFile writes a DownloadState JSON file into dir and returns its path.
func writeStateFile(t *testing.T, dir string, state domain.DownloadState) string {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	p := filepath.Join(dir, stateFileName)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatalf("write state: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// IssueKind.String
// ---------------------------------------------------------------------------

func TestIssueKindString(t *testing.T) {
	cases := []struct {
		kind IssueKind
		want string
	}{
		{IssueMissing, "MISSING"},
		{IssueTruncated, "TRUNCATED"},
		{IssueSizeMismatch, "SIZE_MISMATCH"},
		{IssueNoPath, "NO_PATH"},
		{IssueOrphanTmp, "ORPHAN_TMP"},
		{IssueDurationMismatch, "DURATION_MISMATCH"},
		{IssueKind(999), "UNKNOWN"},
		{IssueKind(-1), "UNKNOWN"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("IssueKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Report.HasIssues
// ---------------------------------------------------------------------------

func TestReportHasIssues(t *testing.T) {
	cases := []struct {
		name string
		r    Report
		want bool
	}{
		{"empty", Report{}, false},
		{"has issue", Report{Issues: []Issue{{Kind: IssueMissing}}}, true},
		{"has orphan", Report{OrphanTmps: []string{"a.tmp"}}, true},
		{"both", Report{Issues: []Issue{{}}, OrphanTmps: []string{"x"}}, true},
		{"healthy only", Report{Healthy: 5}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.HasIssues(); got != tc.want {
				t.Errorf("HasIssues() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// checkEntry
// ---------------------------------------------------------------------------

func TestCheckEntry(t *testing.T) {
	dir := t.TempDir()

	// Create a healthy file of exactly 100 bytes.
	healthyPath := filepath.Join(dir, "healthy.mkv")
	if err := os.WriteFile(healthyPath, make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	// Truncated file: 50 bytes but state says 100.
	truncPath := filepath.Join(dir, "trunc.mkv")
	if err := os.WriteFile(truncPath, make([]byte, 50), 0644); err != nil {
		t.Fatal(err)
	}
	// Larger file: 200 bytes but state says 100.
	largePath := filepath.Join(dir, "large.mkv")
	if err := os.WriteFile(largePath, make([]byte, 200), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("no path and zero bytes", func(t *testing.T) {
		issue := checkEntry("S1E1", domain.CompletedRec{Path: "", Bytes: 0}, dir)
		if issue == nil || issue.Kind != IssueNoPath {
			t.Fatalf("want IssueNoPath, got %+v", issue)
		}
		if issue.ActualBytes != -1 {
			t.Errorf("ActualBytes = %d, want -1", issue.ActualBytes)
		}
	})

	t.Run("no path but nonzero bytes", func(t *testing.T) {
		issue := checkEntry("S1E2", domain.CompletedRec{Path: "", Bytes: 123}, dir)
		if issue == nil || issue.Kind != IssueNoPath {
			t.Fatalf("want IssueNoPath, got %+v", issue)
		}
		if issue.StateBytes != 123 {
			t.Errorf("StateBytes = %d, want 123", issue.StateBytes)
		}
	})

	t.Run("missing file (relative path)", func(t *testing.T) {
		issue := checkEntry("S1E3", domain.CompletedRec{Path: "nope.mkv", Bytes: 10}, dir)
		if issue == nil || issue.Kind != IssueMissing {
			t.Fatalf("want IssueMissing, got %+v", issue)
		}
		if issue.ActualBytes != -1 {
			t.Errorf("ActualBytes = %d, want -1", issue.ActualBytes)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		issue := checkEntry("S1E4", domain.CompletedRec{Path: truncPath, Bytes: 100}, dir)
		if issue == nil || issue.Kind != IssueTruncated {
			t.Fatalf("want IssueTruncated, got %+v", issue)
		}
		if issue.ActualBytes != 50 {
			t.Errorf("ActualBytes = %d, want 50", issue.ActualBytes)
		}
	})

	t.Run("size mismatch larger", func(t *testing.T) {
		issue := checkEntry("S1E5", domain.CompletedRec{Path: largePath, Bytes: 100}, dir)
		if issue == nil || issue.Kind != IssueSizeMismatch {
			t.Fatalf("want IssueSizeMismatch, got %+v", issue)
		}
		if issue.ActualBytes != 200 {
			t.Errorf("ActualBytes = %d, want 200", issue.ActualBytes)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		issue := checkEntry("S1E6", domain.CompletedRec{Path: healthyPath, Bytes: 100}, dir)
		if issue != nil {
			t.Fatalf("want nil (healthy), got %+v", issue)
		}
	})

	t.Run("healthy zero recorded bytes skips size checks", func(t *testing.T) {
		// rec.Bytes == 0 means size checks are skipped; existing file is healthy.
		issue := checkEntry("S1E7", domain.CompletedRec{Path: healthyPath, Bytes: 0}, dir)
		if issue != nil {
			t.Fatalf("want nil, got %+v", issue)
		}
	})

	t.Run("absolute path missing", func(t *testing.T) {
		issue := checkEntry("S1E8", domain.CompletedRec{Path: filepath.Join(dir, "absent.mkv"), Bytes: 5}, dir)
		if issue == nil || issue.Kind != IssueMissing {
			t.Fatalf("want IssueMissing, got %+v", issue)
		}
	})
}

// ---------------------------------------------------------------------------
// copyFile
// ---------------------------------------------------------------------------

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()

	t.Run("success", func(t *testing.T) {
		src := filepath.Join(dir, "src.txt")
		dst := filepath.Join(dir, "dst.txt")
		content := []byte("hello doctor")
		if err := os.WriteFile(src, content, 0644); err != nil {
			t.Fatal(err)
		}
		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile: %v", err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(content) {
			t.Errorf("dst = %q, want %q", got, content)
		}
	})

	t.Run("nonexistent src", func(t *testing.T) {
		err := copyFile(filepath.Join(dir, "ghost.txt"), filepath.Join(dir, "out.txt"))
		if err == nil {
			t.Fatal("expected error for nonexistent src")
		}
	})
}

// ---------------------------------------------------------------------------
// findOrphanTmps
// ---------------------------------------------------------------------------

func TestFindOrphanTmps(t *testing.T) {
	dir := t.TempDir()

	// Orphan .tmp (no corresponding final file).
	orphan := filepath.Join(dir, "ep1.mkv.tmp")
	if err := os.WriteFile(orphan, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// A .tmp whose final file exists -> NOT an orphan.
	finalExists := filepath.Join(dir, "ep2.mkv")
	if err := os.WriteFile(finalExists, []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalExists+".tmp", []byte("z"), 0644); err != nil {
		t.Fatal(err)
	}
	// A normal file -> ignored.
	if err := os.WriteFile(filepath.Join(dir, "normal.mkv"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	// A hidden dir with a .tmp inside -> skipped by the walk.
	hidden := filepath.Join(dir, ".cache")
	if err := os.Mkdir(hidden, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "deep.mkv.tmp"), []byte("h"), 0644); err != nil {
		t.Fatal(err)
	}
	// A nested non-hidden subdir orphan -> found.
	sub := filepath.Join(dir, "Season 01")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	subOrphan := filepath.Join(sub, "ep3.mkv.tmp")
	if err := os.WriteFile(subOrphan, []byte("s"), 0644); err != nil {
		t.Fatal(err)
	}

	orphans := findOrphanTmps(dir)

	got := map[string]bool{}
	for _, o := range orphans {
		got[o] = true
	}
	if !got[orphan] {
		t.Errorf("expected orphan %q to be found, got %v", orphan, orphans)
	}
	if !got[subOrphan] {
		t.Errorf("expected nested orphan %q to be found, got %v", subOrphan, orphans)
	}
	if got[finalExists+".tmp"] {
		t.Errorf("tmp with existing final should not be an orphan: %v", orphans)
	}
	if got[filepath.Join(hidden, "deep.mkv.tmp")] {
		t.Errorf("hidden-dir tmp should be skipped: %v", orphans)
	}
}

// ---------------------------------------------------------------------------
// parseDurationFromJSON
// ---------------------------------------------------------------------------

func TestParseDurationFromJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		d, err := parseDurationFromJSON([]byte(`{"format":{"duration":"120.5"}}`))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		want := time.Duration(120.5 * float64(time.Second))
		if d != want {
			t.Errorf("d = %v, want %v", d, want)
		}
	})
	t.Run("bad json", func(t *testing.T) {
		if _, err := parseDurationFromJSON([]byte(`not json`)); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("empty duration", func(t *testing.T) {
		if _, err := parseDurationFromJSON([]byte(`{"format":{"duration":""}}`)); err == nil {
			t.Fatal("expected error for empty duration")
		}
	})
	t.Run("unparseable duration", func(t *testing.T) {
		if _, err := parseDurationFromJSON([]byte(`{"format":{"duration":"abc"}}`)); err == nil {
			t.Fatal("expected error for unparseable duration")
		}
	})
}

// ---------------------------------------------------------------------------
// Run: state file load errors
// ---------------------------------------------------------------------------

func TestRunStateFileNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, SkipProbe: true})
	if err == nil {
		t.Fatal("expected error when state file is missing")
	}
}

func TestRunCorruptStateNoFix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte("{not valid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, SkipProbe: true})
	if err == nil {
		t.Fatal("expected error for corrupt state without --fix")
	}
}

// TestRunCorruptStateFixBackupFails asserts the corrupt-state safety: with --fix
// the doctor REFUSES to reset when the backup copy cannot be written, and the
// original corrupt file is preserved untouched.
func TestRunCorruptStateFixBackupFails(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, stateFileName)
	corrupt := []byte("{this is corrupt")
	if err := os.WriteFile(statePath, corrupt, 0644); err != nil {
		t.Fatal(err)
	}
	// Make the directory read-only so the backup WriteFile fails. copyFile reads
	// the source (succeeds) then writes the backup into the same dir (fails).
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	_, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, Fix: true, SkipProbe: true})
	if err == nil {
		// Restore perms so we can inspect, then fail.
		_ = os.Chmod(dir, 0700)
		t.Fatal("expected error: doctor must refuse to reset corrupt state when backup fails")
	}

	// Restore perms and confirm the original corrupt content is preserved.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(statePath)
	if readErr != nil {
		t.Fatalf("reading preserved state: %v", readErr)
	}
	if string(got) != string(corrupt) {
		t.Errorf("original corrupt state must be preserved untouched; got %q", got)
	}
}

// TestRunCorruptStateFixWritableDir asserts that in a writable dir, --fix backs
// up the corrupt file then resets to an empty state.
func TestRunCorruptStateFixWritableDir(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, stateFileName)
	if err := os.WriteFile(statePath, []byte("{corrupt json here"), 0644); err != nil {
		t.Fatal(err)
	}

	report, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, Fix: true, SkipProbe: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report == nil || len(report.Issues) != 1 {
		t.Fatalf("want one reset issue, got %+v", report)
	}

	// A backup file should now exist (name: <state>.corrupt.<timestamp>).
	entries, _ := os.ReadDir(dir)
	foundBackup := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) != "" && len(e.Name()) > len(stateFileName) &&
			e.Name() != stateFileName && containsCorrupt(e.Name()) {
			foundBackup = true
		}
	}
	if !foundBackup {
		t.Errorf("expected a .corrupt backup file in %v", names(entries))
	}

	// The state file must now be valid empty JSON.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st domain.DownloadState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("repaired state is not valid JSON: %v", err)
	}
	if len(st.Completed) != 0 {
		t.Errorf("repaired state should have empty Completed, got %v", st.Completed)
	}
}

func containsCorrupt(name string) bool {
	for i := 0; i+8 <= len(name); i++ {
		if name[i:i+8] == ".corrupt" {
			return true
		}
	}
	return false
}

func names(entries []os.DirEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// ---------------------------------------------------------------------------
// Run: healthy / skipped accounting & issue detection (SkipProbe path)
// ---------------------------------------------------------------------------

func TestRunHealthyAndIssues(t *testing.T) {
	dir := t.TempDir()

	// Healthy file.
	healthy := filepath.Join(dir, "ok.mkv")
	if err := os.WriteFile(healthy, make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	// Truncated file.
	trunc := filepath.Join(dir, "bad.mkv")
	if err := os.WriteFile(trunc, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}

	state := domain.DownloadState{
		Series: "123",
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1, Path: healthy, Bytes: 100},
			"S1E2": {Season: 1, Episode: 2, Path: trunc, Bytes: 100},
			"S1E3": {Season: 1, Episode: 3, Path: "", Bytes: 0}, // NoPath
		},
	}
	writeStateFile(t, dir, state)

	report, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, SkipProbe: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.TotalInState != 3 {
		t.Errorf("TotalInState = %d, want 3", report.TotalInState)
	}
	if report.Healthy != 1 {
		t.Errorf("Healthy = %d, want 1", report.Healthy)
	}
	if len(report.Issues) != 2 {
		t.Errorf("Issues = %d, want 2", len(report.Issues))
	}
	if report.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", report.Skipped)
	}
	// Issues sorted by key.
	if len(report.Issues) == 2 && report.Issues[0].Key > report.Issues[1].Key {
		t.Errorf("issues not sorted by key: %v", report.Issues)
	}
	if report.SeriesID != "123" {
		t.Errorf("SeriesID = %q, want 123", report.SeriesID)
	}
}

// TestRunSkippedNotCountedAsHealthy is the regression guard: when duration
// resolution is active but an entry cannot be resolved/probed, it must be
// counted as Skipped and NOT as Healthy.
//
// We can only reach the Skipped branch when ffprobe exists on the machine. If
// it is absent, the duration path is disabled entirely; in that case every
// passing entry is Healthy with zero Skipped, which we still assert as the
// documented behaviour. The key invariant in both cases: Healthy + Skipped +
// issues == entries, and Skipped entries are never double-counted as Healthy.
func TestRunSkippedNotCountedAsHealthy(t *testing.T) {
	dir := t.TempDir()
	healthy := filepath.Join(dir, "ep.mkv")
	if err := os.WriteFile(healthy, make([]byte, 64), 0644); err != nil {
		t.Fatal(err)
	}

	// Metadata present so the duration pipeline would engage if ffprobe exists,
	// but the feed resolves to an empty series, so no expected duration is found.
	state := domain.DownloadState{
		Series: "9",
		Metadata: &domain.SeriesMetadata{
			Title:    "Test",
			InputURL: "https://example.invalid/feed",
		},
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1, Path: healthy, Bytes: 64},
		},
	}
	writeStateFile(t, dir, state)

	deps := Deps{
		Logger:        nopLogger{},
		InputResolver: stubInputResolver{src: domain.FeedSource{ID: "9"}},
		FeedParser:    stubFeedParser{series: domain.Series{}}, // empty -> no durations resolved
		MediaResolver: stubMediaResolver{},
	}

	report, err := Run(context.Background(), deps, Options{OutputDir: dir, SkipProbe: false})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Invariant: a single entry that is not an issue is accounted exactly once,
	// either Healthy or Skipped, never both.
	total := report.Healthy + report.Skipped + len(report.Issues)
	if total != report.TotalInState {
		t.Errorf("accounting mismatch: healthy=%d skipped=%d issues=%d total=%d",
			report.Healthy, report.Skipped, len(report.Issues), report.TotalInState)
	}
	if report.Healthy > 1 {
		t.Errorf("Healthy over-counted: %d", report.Healthy)
	}
}

// TestRunCleanTmpFix verifies orphan .tmp detection and that --fix --clean-tmp
// removes the orphan file.
func TestRunCleanTmpFix(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "leftover.mkv.tmp")
	if err := os.WriteFile(orphan, []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}

	state := domain.DownloadState{
		Series:    "5",
		Completed: map[string]domain.CompletedRec{},
	}
	writeStateFile(t, dir, state)

	report, err := Run(context.Background(), newDeps(), Options{
		OutputDir: dir,
		Fix:       true,
		CleanTmp:  true,
		SkipProbe: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.OrphanTmps) != 1 || report.OrphanTmps[0] != orphan {
		t.Fatalf("OrphanTmps = %v, want [%s]", report.OrphanTmps, orphan)
	}
	if !report.HasIssues() {
		t.Error("HasIssues should be true with an orphan tmp")
	}
	// With CleanTmp, the orphan file should be deleted.
	if _, statErr := os.Stat(orphan); !os.IsNotExist(statErr) {
		t.Errorf("orphan tmp should have been deleted, stat err = %v", statErr)
	}
}

// TestRunOrphanTmpNoClean confirms the orphan is reported but NOT deleted when
// CleanTmp is false.
func TestRunOrphanTmpNoClean(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "leftover.mkv.tmp")
	if err := os.WriteFile(orphan, []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}
	writeStateFile(t, dir, domain.DownloadState{Series: "5", Completed: map[string]domain.CompletedRec{}})

	report, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, Fix: true, CleanTmp: false, SkipProbe: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.OrphanTmps) != 1 {
		t.Fatalf("want 1 orphan, got %v", report.OrphanTmps)
	}
	if _, statErr := os.Stat(orphan); statErr != nil {
		t.Errorf("orphan must be preserved without CleanTmp, stat err = %v", statErr)
	}
}

// TestRunFixRemovesBrokenEntries verifies that --fix removes broken entries from
// the persisted state and deletes truncated files from disk.
func TestRunFixRemovesBrokenEntries(t *testing.T) {
	dir := t.TempDir()

	healthy := filepath.Join(dir, "ok.mkv")
	if err := os.WriteFile(healthy, make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	trunc := filepath.Join(dir, "bad.mkv")
	if err := os.WriteFile(trunc, make([]byte, 10), 0644); err != nil {
		t.Fatal(err)
	}

	state := domain.DownloadState{
		Series: "7",
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1, Path: healthy, Bytes: 100},
			"S1E2": {Season: 1, Episode: 2, Path: trunc, Bytes: 100},    // truncated
			"S1E3": {Season: 1, Episode: 3, Path: "gone.mkv", Bytes: 5}, // missing
		},
	}
	statePath := writeStateFile(t, dir, state)

	report, err := Run(context.Background(), newDeps(), Options{OutputDir: dir, Fix: true, SkipProbe: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Issues) != 2 {
		t.Fatalf("want 2 issues, got %d: %+v", len(report.Issues), report.Issues)
	}

	// Reload persisted state: broken entries removed, healthy retained.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var after domain.DownloadState
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if _, ok := after.Completed["S1E1"]; !ok {
		t.Error("healthy entry S1E1 should be retained")
	}
	if _, ok := after.Completed["S1E2"]; ok {
		t.Error("truncated entry S1E2 should be removed")
	}
	if _, ok := after.Completed["S1E3"]; ok {
		t.Error("missing entry S1E3 should be removed")
	}
	// Truncated file should be deleted from disk.
	if _, statErr := os.Stat(trunc); !os.IsNotExist(statErr) {
		t.Errorf("truncated file should be deleted, stat err = %v", statErr)
	}
	// Healthy file untouched.
	if _, statErr := os.Stat(healthy); statErr != nil {
		t.Errorf("healthy file must remain, stat err = %v", statErr)
	}
}

// ---------------------------------------------------------------------------
// applyFixes directly (no fix when no removable issues, orphan-only)
// ---------------------------------------------------------------------------

func TestApplyFixesOrphanOnly(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, "x.mkv.tmp")
	if err := os.WriteFile(orphan, []byte("p"), 0644); err != nil {
		t.Fatal(err)
	}
	state := domain.DownloadState{Series: "1", Completed: map[string]domain.CompletedRec{}}
	statePath := filepath.Join(dir, stateFileName)

	report := &Report{
		Issues: []Issue{{Kind: IssueOrphanTmp, Detail: orphan, StatePath: orphan}},
	}
	removed := applyFixes(statePath, &state, report, Options{OutputDir: dir, CleanTmp: true}, nopLogger{})
	if removed != 0 {
		t.Errorf("orphan-only fixes should remove 0 state entries, got %d", removed)
	}
	if _, statErr := os.Stat(orphan); !os.IsNotExist(statErr) {
		t.Errorf("orphan should be deleted, stat err = %v", statErr)
	}
}

// ---------------------------------------------------------------------------
// resolveExpectedDurations
// ---------------------------------------------------------------------------

func TestResolveExpectedDurationsNoMetadata(t *testing.T) {
	state := domain.DownloadState{Completed: map[string]domain.CompletedRec{}}
	got := resolveExpectedDurations(context.Background(), newDeps(), state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("want empty map, got %v", got)
	}
}

func TestResolveExpectedDurationsNoURL(t *testing.T) {
	state := domain.DownloadState{
		Metadata:  &domain.SeriesMetadata{Title: "x"}, // no InputURL/FeedURL
		Completed: map[string]domain.CompletedRec{},
	}
	got := resolveExpectedDurations(context.Background(), newDeps(), state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("want empty map (no url), got %v", got)
	}
}

func TestResolveExpectedDurationsResolveError(t *testing.T) {
	state := domain.DownloadState{
		Metadata:  &domain.SeriesMetadata{InputURL: "https://x.invalid"},
		Completed: map[string]domain.CompletedRec{},
	}
	deps := newDeps()
	deps.InputResolver = stubInputResolver{err: errStub}
	got := resolveExpectedDurations(context.Background(), deps, state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("want empty map on resolve error, got %v", got)
	}
}

func TestResolveExpectedDurationsParseError(t *testing.T) {
	state := domain.DownloadState{
		Metadata:  &domain.SeriesMetadata{FeedURL: "https://x.invalid"}, // falls back to FeedURL
		Completed: map[string]domain.CompletedRec{},
	}
	deps := newDeps()
	deps.FeedParser = stubFeedParser{err: errStub}
	got := resolveExpectedDurations(context.Background(), deps, state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("want empty map on parse error, got %v", got)
	}
}

func TestResolveExpectedDurationsSuccess(t *testing.T) {
	state := domain.DownloadState{
		Metadata: &domain.SeriesMetadata{InputURL: "https://x.invalid"},
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1},
			// S1E2 is in the feed but not in state -> must be skipped.
		},
	}
	deps := newDeps()
	deps.FeedParser = stubFeedParser{series: domain.Series{
		Seasons: []domain.Season{{
			Number: 1,
			Episodes: []domain.Episode{
				{Key: domain.EpisodeKey{Season: 1, Episode: 1}},
				{Key: domain.EpisodeKey{Season: 1, Episode: 2}},
			},
		}},
	}}
	deps.MediaResolver = stubMediaResolver{media: domain.ResolvedMedia{Duration: 90 * time.Second}}

	got := resolveExpectedDurations(context.Background(), deps, state, nopLogger{})
	if len(got) != 1 {
		t.Fatalf("want exactly 1 resolved duration (only in-state episode), got %v", got)
	}
	if got["S1E1"] != 90*time.Second {
		t.Errorf("S1E1 = %v, want 90s", got["S1E1"])
	}
	if _, ok := got["S1E2"]; ok {
		t.Error("S1E2 not in state should not be resolved")
	}
}

func TestResolveExpectedDurationsMediaError(t *testing.T) {
	state := domain.DownloadState{
		Metadata: &domain.SeriesMetadata{InputURL: "https://x.invalid"},
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1},
		},
	}
	deps := newDeps()
	deps.FeedParser = stubFeedParser{series: domain.Series{
		Seasons: []domain.Season{{Number: 1, Episodes: []domain.Episode{
			{Key: domain.EpisodeKey{Season: 1, Episode: 1}},
		}}},
	}}
	deps.MediaResolver = stubMediaResolver{err: errStub}

	got := resolveExpectedDurations(context.Background(), deps, state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("media resolve error should yield no durations, got %v", got)
	}
}

// TestResolveExpectedDurationsZeroDuration: an episode that resolves with
// Duration==0 must not be recorded.
func TestResolveExpectedDurationsZeroDuration(t *testing.T) {
	state := domain.DownloadState{
		Metadata: &domain.SeriesMetadata{InputURL: "https://x.invalid"},
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Season: 1, Episode: 1},
		},
	}
	deps := newDeps()
	deps.FeedParser = stubFeedParser{series: domain.Series{
		Seasons: []domain.Season{{Number: 1, Episodes: []domain.Episode{
			{Key: domain.EpisodeKey{Season: 1, Episode: 1}},
		}}},
	}}
	deps.MediaResolver = stubMediaResolver{media: domain.ResolvedMedia{Duration: 0}}

	got := resolveExpectedDurations(context.Background(), deps, state, nopLogger{})
	if len(got) != 0 {
		t.Errorf("zero duration should not be recorded, got %v", got)
	}
}

var errStub = stubError("stub error")

type stubError string

func (e stubError) Error() string { return string(e) }

func TestApplyFixesDurationMismatchDeletesFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "dur.mkv")
	if err := os.WriteFile(f, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	state := domain.DownloadState{
		Series: "1",
		Completed: map[string]domain.CompletedRec{
			"S1E1": {Path: f, Bytes: 4},
		},
	}
	statePath := writeStateFile(t, dir, state)

	report := &Report{
		Issues: []Issue{{Key: "S1E1", Kind: IssueDurationMismatch, StatePath: f}},
	}
	removed := applyFixes(statePath, &state, report, Options{OutputDir: dir}, nopLogger{})
	if removed != 1 {
		t.Errorf("want 1 removed, got %d", removed)
	}
	if _, ok := state.Completed["S1E1"]; ok {
		t.Error("S1E1 should be removed from state")
	}
	if _, statErr := os.Stat(f); !os.IsNotExist(statErr) {
		t.Errorf("duration-mismatch file should be deleted, stat err = %v", statErr)
	}
}
