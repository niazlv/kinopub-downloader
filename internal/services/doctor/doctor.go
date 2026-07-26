// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package doctor implements the "doctor" subcommand that verifies downloaded
// files against the state file and repairs inconsistencies.
//
// State files are discovered both at the root of the output directory (the
// legacy layout) and inside each immediate series subdirectory
// (<output>/<Series Title>/.kinopub-state.json, the layout the store writes
// today); every discovered state file is checked and the findings are merged
// into a single report.
//
// It detects:
//   - Files recorded in state but missing on disk
//   - Files whose on-disk size doesn't match the recorded bytes
//   - State entries with empty path or zero bytes (incomplete records)
//   - Orphan intermediate files left from interrupted downloads (.tmp, .raw,
//     .raw.part, .part, .ts files and .hls-tmp segment-cache directories)
//   - Files whose duration doesn't match the source (resolved via the same
//     InputResolver → FeedParser → MediaResolver pipeline as the downloader)
//
// Repair actions:
//   - Remove state entries for broken files so they get re-downloaded
//   - Delete truncated/corrupt files from disk
//   - Delete orphan intermediate files and directories
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/lib/fsutil"
)

// Issue describes a single problem found during verification.
type Issue struct {
	Key         string // "S1E16" style key
	Season      int
	Episode     int
	Kind        IssueKind
	Detail      string
	StatePath   string // path recorded in state
	StateBytes  int64  // bytes recorded in state
	ActualBytes int64  // actual file size on disk (-1 if missing)
	StateFile   string // state file the issue was found in (identifies the series folder)
}

// IssueKind classifies the type of problem.
type IssueKind int

const (
	IssueMissing          IssueKind = iota // file not found on disk
	IssueTruncated                         // file smaller than recorded
	IssueSizeMismatch                      // file size differs (larger)
	IssueNoPath                            // state entry has no path
	IssueOrphanTmp                         // orphan intermediate file or directory
	IssueDurationMismatch                  // local duration < source duration
)

// String returns a human-readable label for the issue kind.
func (k IssueKind) String() string {
	switch k {
	case IssueMissing:
		return "MISSING"
	case IssueTruncated:
		return "TRUNCATED"
	case IssueSizeMismatch:
		return "SIZE_MISMATCH"
	case IssueNoPath:
		return "NO_PATH"
	case IssueOrphanTmp:
		return "ORPHAN_TMP"
	case IssueDurationMismatch:
		return "DURATION_MISMATCH"
	default:
		return "UNKNOWN"
	}
}

// Report is the result of a doctor check. When several state files are
// discovered (one per series subdirectory), their findings are merged into a
// single Report and each Issue carries the state file it came from.
type Report struct {
	StateFile    string   // checked state file path(s), comma-separated for display
	StateFiles   []string // every state file that was checked
	SeriesID     string
	SeriesTitle  string
	TotalInState int
	Healthy      int
	Issues       []Issue
	OrphanTmps   []string
	Skipped      int // entries where remote probe was not possible
}

// HasIssues reports whether any problems were found.
func (r *Report) HasIssues() bool {
	return len(r.Issues) > 0 || len(r.OrphanTmps) > 0
}

// Deps holds the injectable dependencies for the doctor — same interfaces
// used by the main download engine, enabling full reuse of the resolution pipeline.
type Deps struct {
	Logger        domain.Logger
	InputResolver domain.InputResolver
	FeedParser    domain.FeedParser
	MediaResolver domain.MediaResolver
}

// Options configures the doctor behavior.
type Options struct {
	// OutputDir is the directory where downloads are stored and the state file lives.
	OutputDir string
	// Fix when true, automatically repairs the state file.
	Fix bool
	// CleanTmp when true, deletes orphan intermediate files and directories.
	CleanTmp bool
	// SkipProbe disables duration verification (faster, no network).
	SkipProbe bool
	// FFprobePath for local file probing (default: "ffprobe").
	FFprobePath string
	// DurationTolerance is the acceptable ratio difference (0.0–1.0). Default 0.05.
	DurationTolerance float64
}

const stateFileName = ".kinopub-state.json"
const defaultDurationTolerance = 0.05

// Run performs the doctor check and optionally repairs issues.
//
// State files are discovered at the legacy root location and in every
// immediate series subdirectory; each one is checked independently (fixes
// included) and the findings are merged into a single report.
func Run(ctx context.Context, deps Deps, opts Options) (*Report, error) {
	log := deps.Logger.Component("doctor")

	if opts.FFprobePath == "" {
		opts.FFprobePath = "ffprobe"
	}
	if opts.DurationTolerance == 0 {
		opts.DurationTolerance = defaultDurationTolerance
	}

	// 1. Discover state files: the legacy root location plus one per series
	//    subdirectory (the store writes <output>/<Series Title>/.kinopub-state.json).
	statePaths := discoverStateFiles(opts.OutputDir)
	if len(statePaths) == 0 {
		return nil, fmt.Errorf("state file not found at %s (nor in any series subdirectory) — nothing to check",
			filepath.Join(opts.OutputDir, stateFileName))
	}

	// ffprobe availability is machine-wide; look it up once for all state files.
	hasFFprobe := false
	if !opts.SkipProbe {
		if _, err := exec.LookPath(opts.FFprobePath); err == nil {
			hasFFprobe = true
		} else {
			log.Warn("ffprobe not found, skipping duration verification")
		}
	}

	// 2. Check every discovered state file and merge findings into one report.
	report := &Report{}
	for _, statePath := range statePaths {
		sub, err := checkStateFile(ctx, deps, opts, statePath, hasFFprobe, log)
		if err != nil {
			return nil, err
		}
		mergeReport(report, sub)
	}

	// Sort issues by key (then state file) for stable output.
	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Key != report.Issues[j].Key {
			return report.Issues[i].Key < report.Issues[j].Key
		}
		return report.Issues[i].StateFile < report.Issues[j].StateFile
	})

	// 3. Scan for orphan intermediate files and directories.
	orphans := findOrphanTmps(opts.OutputDir)
	report.OrphanTmps = orphans
	for _, tmp := range orphans {
		report.Issues = append(report.Issues, Issue{
			Kind:      IssueOrphanTmp,
			Detail:    tmp,
			StatePath: tmp,
		})
	}

	// 4. Clean orphans if requested. State-entry fixes were already applied
	//    per state file in checkStateFile; orphans belong to no state file.
	if opts.Fix && opts.CleanTmp {
		for _, tmp := range orphans {
			removeOrphan(tmp, log)
		}
	}

	return report, nil
}

// discoverStateFiles returns every state file under outputDir that should be
// checked: the legacy root location (if present) plus the state file of each
// immediate series subdirectory. Order is deterministic: root first, then
// subdirectories sorted by name (os.ReadDir returns sorted entries).
func discoverStateFiles(outputDir string) []string {
	var paths []string

	rootState := filepath.Join(outputDir, stateFileName)
	if _, err := os.Stat(rootState); err == nil {
		paths = append(paths, rootState)
	}

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return paths
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(outputDir, e.Name(), stateFileName)
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths
}

// mergeReport folds a per-state-file report into the aggregate report,
// tagging each issue with the state file it came from so multi-series
// reports stay unambiguous.
func mergeReport(dst, src *Report) {
	dst.StateFiles = append(dst.StateFiles, src.StateFile)
	dst.StateFile = joinNonEmpty(dst.StateFile, src.StateFile)
	dst.SeriesID = joinNonEmpty(dst.SeriesID, src.SeriesID)
	dst.SeriesTitle = joinNonEmpty(dst.SeriesTitle, src.SeriesTitle)
	dst.TotalInState += src.TotalInState
	dst.Healthy += src.Healthy
	dst.Skipped += src.Skipped
	for i := range src.Issues {
		if src.Issues[i].StateFile == "" {
			src.Issues[i].StateFile = src.StateFile
		}
	}
	dst.Issues = append(dst.Issues, src.Issues...)
}

// joinNonEmpty concatenates two values with ", ", skipping empty ones.
func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + ", " + b
}

// checkStateFile loads a single state file, verifies every completed entry,
// and — when opts.Fix is set — repairs that state file in place. It returns
// a per-file report that Run merges into the aggregate.
func checkStateFile(ctx context.Context, deps Deps, opts Options, stateFilePath string, hasFFprobe bool, log domain.Logger) (*Report, error) {
	// 1. Load and parse state file.
	data, err := os.ReadFile(stateFilePath)
	if err != nil {
		return nil, fmt.Errorf("cannot read state file %s: %w", stateFilePath, err)
	}

	var state domain.DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Error("state file is corrupt JSON",
			domain.F("path", stateFilePath),
			domain.F("error", err.Error()),
		)
		if opts.Fix {
			backupPath := stateFilePath + ".corrupt." + time.Now().Format("20060102-150405")
			// Never overwrite the only copy of corrupt-but-recoverable state if we
			// couldn't back it up first.
			if copyErr := copyFile(stateFilePath, backupPath); copyErr != nil {
				return nil, fmt.Errorf("refusing to reset corrupt state: backup to %s failed: %w", backupPath, copyErr)
			}
			log.Info("backed up corrupt state file", domain.F("backup", backupPath))
			emptyState := domain.DownloadState{
				Series:    "",
				Completed: make(map[string]domain.CompletedRec),
			}
			if writeErr := writeState(stateFilePath, emptyState); writeErr != nil {
				return nil, fmt.Errorf("failed to write repaired state: %w", writeErr)
			}
			log.Info("replaced corrupt state with empty state")
			return &Report{
				StateFile: stateFilePath,
				Issues: []Issue{{
					Kind:   IssueMissing,
					Detail: "state file was corrupt JSON and has been reset",
				}},
			}, nil
		}
		return nil, fmt.Errorf("state file is corrupt JSON: %w (use --fix to reset)", err)
	}

	if state.Completed == nil {
		state.Completed = make(map[string]domain.CompletedRec)
	}

	report := &Report{
		StateFile:    stateFilePath,
		SeriesID:     string(state.Series),
		TotalInState: len(state.Completed),
	}
	if state.Metadata != nil {
		report.SeriesTitle = state.Metadata.Title
	}

	// 2. Resolve the series catalog from the source to get fresh media URLs
	//    and durations for comparison. This uses the same pipeline as the downloader.
	var episodeDurations map[string]time.Duration // key = "S{n}E{n}" → expected duration

	if hasFFprobe && state.Metadata != nil {
		episodeDurations = resolveExpectedDurations(ctx, deps, state, log)
	}

	// 3. Check each completed entry.
	for key, rec := range state.Completed {
		issue := checkEntry(key, rec, opts.OutputDir)
		if issue != nil {
			report.Issues = append(report.Issues, *issue)
			continue
		}

		// File exists and size matches — verify duration against source.
		if hasFFprobe && rec.Path != "" && episodeDurations != nil {
			expectedDur, hasExpected := episodeDurations[key]
			if !hasExpected {
				// Could not resolve this episode from the feed — skip.
				report.Skipped++
				continue
			}

			fullPath := rec.Path
			if !filepath.IsAbs(fullPath) {
				fullPath = filepath.Join(opts.OutputDir, fullPath)
			}

			localDur, err := probeLocalDuration(ctx, opts.FFprobePath, fullPath)
			if err != nil {
				log.Debug("ffprobe failed on local file, skipping",
					domain.F("key", key),
					domain.F("error", err.Error()),
				)
				report.Skipped++
				continue
			}

			if expectedDur > 0 {
				ratio := float64(localDur) / float64(expectedDur)
				threshold := 1.0 - opts.DurationTolerance

				if ratio < threshold {
					report.Issues = append(report.Issues, Issue{
						Key:         key,
						Season:      rec.Season,
						Episode:     rec.Episode,
						Kind:        IssueDurationMismatch,
						Detail:      fmt.Sprintf("local %s vs source %s (%.1f%%) — file truncated", localDur, expectedDur, ratio*100),
						StatePath:   rec.Path,
						StateBytes:  rec.Bytes,
						ActualBytes: rec.Bytes,
					})
					continue
				}
			}
		}

		report.Healthy++
	}

	// 4. Apply fixes for this state file if requested (orphan cleanup is
	//    handled by Run, since orphans belong to no state file).
	if opts.Fix && len(report.Issues) > 0 {
		fixed := applyFixes(stateFilePath, &state, report, opts, log)
		log.Info("fixes applied",
			domain.F("state_file", stateFilePath),
			domain.F("entries_removed", fixed),
		)
	}

	return report, nil
}

// resolveExpectedDurations uses the existing InputResolver → FeedParser →
// MediaResolver pipeline to obtain the expected duration for each episode
// from the source. This is the same path the downloader uses — no hardcoded
// values, the source is the single source of truth.
func resolveExpectedDurations(ctx context.Context, deps Deps, state domain.DownloadState, log domain.Logger) map[string]time.Duration {
	result := make(map[string]time.Duration)

	if state.Metadata == nil {
		return result
	}

	// Determine the input URL to resolve the feed.
	inputURL := state.Metadata.InputURL
	if inputURL == "" {
		inputURL = state.Metadata.FeedURL
	}
	if inputURL == "" {
		log.Warn("no input_url or feed_url in state metadata, cannot resolve source durations")
		return result
	}

	// Resolve input → FeedSource (same as engine step 1).
	log.Info("resolving source for duration verification", domain.F("url", inputURL))
	feedSrc, err := deps.InputResolver.Resolve(ctx, inputURL)
	if err != nil {
		log.Warn("could not resolve input URL for verification",
			domain.F("url", inputURL),
			domain.F("error", err.Error()),
		)
		return result
	}

	// Parse feed → Series (same as engine step 2).
	series, err := deps.FeedParser.Parse(ctx, feedSrc)
	if err != nil {
		log.Warn("could not parse feed for verification",
			domain.F("error", err.Error()),
		)
		return result
	}

	// For each episode in the series, resolve media to get duration.
	for _, season := range series.Seasons {
		for _, ep := range season.Episodes {
			key := fmt.Sprintf("S%dE%d", ep.Key.Season, ep.Key.Episode)

			// Only resolve episodes that are in our state (no point checking others).
			if _, inState := state.Completed[key]; !inState {
				continue
			}

			log.Debug("resolving media for duration check", domain.F("key", key))

			resolved, err := deps.MediaResolver.Resolve(ctx, ep, "")
			if err != nil {
				log.Debug("media resolution failed, skipping duration check",
					domain.F("key", key),
					domain.F("error", err.Error()),
				)
				continue
			}

			if resolved.Duration > 0 {
				result[key] = resolved.Duration
			}
		}
	}

	log.Info("resolved source durations",
		domain.F("resolved", len(result)),
		domain.F("total_in_state", len(state.Completed)),
	)

	return result
}

// checkEntry verifies a single completed record against the filesystem (size only).
func checkEntry(key string, rec domain.CompletedRec, outputDir string) *Issue {
	base := Issue{
		Key:        key,
		Season:     rec.Season,
		Episode:    rec.Episode,
		StatePath:  rec.Path,
		StateBytes: rec.Bytes,
	}

	if rec.Path == "" {
		if rec.Bytes == 0 {
			base.Kind = IssueNoPath
			base.Detail = "state entry has no file path and zero bytes (incomplete record)"
			base.ActualBytes = -1
			return &base
		}
		base.Kind = IssueNoPath
		base.Detail = fmt.Sprintf("state entry has no file path but records %d bytes", rec.Bytes)
		base.ActualBytes = -1
		return &base
	}

	fullPath := rec.Path
	if !filepath.IsAbs(fullPath) {
		fullPath = filepath.Join(outputDir, fullPath)
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			base.Kind = IssueMissing
			base.Detail = fmt.Sprintf("file not found: %s", fullPath)
			base.ActualBytes = -1
			return &base
		}
		base.Kind = IssueMissing
		base.Detail = fmt.Sprintf("cannot stat file: %v", err)
		base.ActualBytes = -1
		return &base
	}

	actualSize := info.Size()
	base.ActualBytes = actualSize

	if rec.Bytes > 0 && actualSize < rec.Bytes {
		base.Kind = IssueTruncated
		base.Detail = fmt.Sprintf("file is truncated: %d bytes on disk vs %d recorded (%.1f%%)",
			actualSize, rec.Bytes, float64(actualSize)/float64(rec.Bytes)*100)
		return &base
	}

	if rec.Bytes > 0 && actualSize != rec.Bytes {
		base.Kind = IssueSizeMismatch
		base.Detail = fmt.Sprintf("size mismatch: %d bytes on disk vs %d recorded",
			actualSize, rec.Bytes)
		return &base
	}

	return nil
}

// probeLocalDuration runs ffprobe on a local file and returns its duration.
func probeLocalDuration(ctx context.Context, ffprobePath, filePath string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobePath,
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		filePath,
	)

	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}

	return parseDurationFromJSON(output)
}

// parseDurationFromJSON extracts duration from ffprobe JSON output.
func parseDurationFromJSON(data []byte) (time.Duration, error) {
	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, fmt.Errorf("parse ffprobe output: %w", err)
	}

	if result.Format.Duration == "" {
		return 0, fmt.Errorf("no duration in ffprobe output")
	}

	var secs float64
	if _, err := fmt.Sscanf(result.Format.Duration, "%f", &secs); err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", result.Format.Duration, err)
	}

	return time.Duration(secs * float64(time.Second)), nil
}

// findOrphanTmps walks the output directory looking for intermediate files
// and directories left behind by interrupted downloads:
//
//	<name>.tmp       pre-rename mux output           orphan when final is missing
//	<name>.raw       chunked payload awaiting remux  always a leftover
//	<name>.raw.part  resumable chunked raw download  orphan when final exists
//	<name>.part      resumable chunked download      orphan when final exists
//	<name>.ts        HLS concat output               orphan when final exists
//	<name>.hls-tmp/  HLS segment cache directory     orphan when final exists
//
// "Final" is the episode file itself: the path with the suffix stripped.
// .tmp keeps its historical rule (garbage from an interrupted run once the
// final never appeared). .raw is never resumed by any code path, so it is
// always reported. The remaining patterns are resumable in-progress state:
// deleting them would lose resume progress, so they only count as orphans —
// and are only reported — once the final file already exists.
func findOrphanTmps(outputDir string) []string {
	var orphans []string
	_ = filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && path != outputDir {
				return filepath.SkipDir
			}
			// HLS segment caches are reported as a single unit; never
			// descend into (or individually report) their segment files.
			if strings.HasSuffix(info.Name(), ".hls-tmp") {
				if finalExists(strings.TrimSuffix(path, ".hls-tmp")) {
					orphans = append(orphans, path)
				}
				return filepath.SkipDir
			}
			return nil
		}

		switch name := info.Name(); {
		case strings.HasSuffix(name, ".tmp"):
			finalPath := strings.TrimSuffix(path, ".tmp")
			if _, statErr := os.Stat(finalPath); os.IsNotExist(statErr) {
				orphans = append(orphans, path)
			}
		case strings.HasSuffix(name, ".raw"):
			orphans = append(orphans, path)
		case strings.HasSuffix(name, ".raw.part"): // must precede the ".part" case
			if finalExists(strings.TrimSuffix(path, ".raw.part")) {
				orphans = append(orphans, path)
			}
		case strings.HasSuffix(name, ".part"):
			if finalExists(strings.TrimSuffix(path, ".part")) {
				orphans = append(orphans, path)
			}
		case strings.HasSuffix(name, ".ts"):
			if finalExists(strings.TrimSuffix(path, ".ts")) {
				orphans = append(orphans, path)
			}
		}
		return nil
	})
	return orphans
}

// finalExists reports whether the final output file for a leftover exists.
func finalExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// removeOrphan deletes an orphan leftover from disk. RemoveAll covers both
// plain files and .hls-tmp directories.
func removeOrphan(path string, log domain.Logger) {
	if err := os.RemoveAll(path); err == nil {
		log.Info("deleted orphan leftover", domain.F("path", path))
	}
}

// applyFixes modifies the state file to remove broken entries.
func applyFixes(stateFilePath string, state *domain.DownloadState, report *Report, opts Options, log domain.Logger) int {
	removed := 0

	for _, issue := range report.Issues {
		switch issue.Kind {
		case IssueMissing, IssueTruncated, IssueNoPath, IssueDurationMismatch:
			if issue.Key != "" {
				delete(state.Completed, issue.Key)
				removed++
				log.Info("removed state entry (will re-download)",
					domain.F("key", issue.Key),
					domain.F("reason", issue.Kind.String()),
				)
			}

			// Delete broken files from disk.
			if (issue.Kind == IssueTruncated || issue.Kind == IssueDurationMismatch) && issue.StatePath != "" {
				fullPath := issue.StatePath
				if !filepath.IsAbs(fullPath) {
					fullPath = filepath.Join(opts.OutputDir, fullPath)
				}
				if err := os.Remove(fullPath); err == nil {
					log.Info("deleted broken file", domain.F("path", fullPath))
				}
			}

		case IssueOrphanTmp:
			if opts.CleanTmp {
				removeOrphan(issue.Detail, log)
			}
		}
	}

	if removed > 0 {
		if err := writeState(stateFilePath, *state); err != nil {
			log.Error("failed to write repaired state", domain.F("error", err.Error()))
		} else {
			log.Info("state file repaired", domain.F("entries_removed", removed))
		}
	}

	return removed
}

// writeState serializes and atomically writes the state to disk.
func writeState(path string, state domain.DownloadState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	return fsutil.AtomicWrite(path, data, 0644)
}

// copyFile copies src to dst for backup purposes.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
