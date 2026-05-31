package domain

// SeriesPlan describes the scope of a download run for progress tracking.
type SeriesPlan struct {
	Total   int         // total selected episodes
	Seasons map[int]int // season number → episode count in that season
}

// ProgressSnapshot captures the current progress state at a point in time.
type ProgressSnapshot struct {
	SeriesPercent int              // [0,100] floor
	SeasonPercent map[int]int      // season → [0,100] floor
	Episode       *EpisodeProgress
}

// EpisodeProgress tracks the download progress of a single episode.
type EpisodeProgress struct {
	Key     EpisodeKey
	Percent int              // [0,100] floor
	Tracks  map[TrackRef]int // per-track [0,100] floor
	State   EpisodeState
}

// EpisodeState represents the lifecycle state of an episode download.
type EpisodeState int

const (
	EpisodePending   EpisodeState = iota
	EpisodeRunning
	EpisodeCompleted
	EpisodeFailed
)

// ProgressSink receives per-track progress updates from the downloader.
type ProgressSink interface {
	TrackProgress(key EpisodeKey, track TrackRef, percent int)
}
