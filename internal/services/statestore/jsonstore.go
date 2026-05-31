// Package statestore implements JSON-file-based download state persistence
// (Req 12). It records which episodes have been completed so that interrupted
// downloads can resume without re-downloading.
package statestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"kinopub_downloader/internal/domain"
	"kinopub_downloader/internal/lib/fsutil"
)

const stateFileName = ".kinopub-state.json"

// JSONStore persists download state as a JSON file in the output directory.
// It implements domain.StateStore.
type JSONStore struct {
	outputDir string
	logger    domain.Logger
}

// New creates a JSONStore that persists state under outputDir.
func New(outputDir string, logger domain.Logger) *JSONStore {
	return &JSONStore{
		outputDir: outputDir,
		logger:    logger.Component("statestore"),
	}
}

// statePath returns the full path to the state file.
func (s *JSONStore) statePath() string {
	return filepath.Join(s.outputDir, stateFileName)
}

// Load reads the persisted state for the given series. If the file is missing,
// an empty state is returned (no error). If the file is corrupt or unreadable,
// an empty state is returned with a warn log (Req 12.5).
func (s *JSONStore) Load(_ context.Context, series domain.SeriesID) (domain.DownloadState, error) {
	empty := domain.DownloadState{
		Series:    series,
		Completed: make(map[string]domain.CompletedRec),
	}

	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return empty, nil
		}
		s.logger.Warn("could not read state file; treating as empty state",
			domain.F("path", s.statePath()),
			domain.F("error", err.Error()),
		)
		return empty, nil
	}

	var state domain.DownloadState
	if err := json.Unmarshal(data, &state); err != nil {
		s.logger.Warn("state file is corrupt; treating as empty state",
			domain.F("path", s.statePath()),
			domain.F("error", err.Error()),
		)
		return empty, nil
	}

	// If the loaded state is for a different series, return empty.
	if state.Series != series {
		return empty, nil
	}

	// Ensure the map is never nil.
	if state.Completed == nil {
		state.Completed = make(map[string]domain.CompletedRec)
	}

	return state, nil
}

// episodeKeyString formats an EpisodeKey as the map key used in state JSON.
func episodeKeyString(key domain.EpisodeKey) string {
	return fmt.Sprintf("S%dE%d", key.Season, key.Episode)
}

// MarkCompleted persists a completed record for the given episode atomically
// (temp + rename via fsutil.AtomicWrite) before the job is recorded as
// succeeded (Req 12.1, 12.3).
func (s *JSONStore) MarkCompleted(_ context.Context, key domain.EpisodeKey) error {
	// Load current state (or start fresh).
	state, _ := s.Load(context.Background(), key.Series)

	rec := domain.CompletedRec{
		Season:      key.Season,
		Episode:     key.Episode,
		CompletedAt: time.Now(),
	}

	state.Completed[episodeKeyString(key)] = rec

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("statestore: marshal state: %w", err)
	}

	if err := fsutil.AtomicWrite(s.statePath(), data, 0644); err != nil {
		return fmt.Errorf("statestore: persist state: %w", err)
	}

	return nil
}

// IsCompleted checks whether the given episode is marked as completed in the
// loaded state (Req 12.2).
func (s *JSONStore) IsCompleted(state domain.DownloadState, key domain.EpisodeKey) bool {
	_, ok := state.Completed[episodeKeyString(key)]
	return ok
}

// Verify that *JSONStore satisfies domain.StateStore at compile time.
var _ domain.StateStore = (*JSONStore)(nil)
