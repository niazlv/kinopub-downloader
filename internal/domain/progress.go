package domain

// SeriesPlan describes the scope of a download run for progress tracking.
type SeriesPlan struct {
	Total            int         // total episodes in the series (including already completed)
	Seasons          map[int]int // season number → total episode count in that season
	AlreadyCompleted int         // episodes already completed from previous sessions
	CompletedPerSeason map[int]int // season → already completed count from previous sessions
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

// ByteProgressSink extends ProgressSink with byte-level progress reporting.
// Implementations that support it can show download speed and file size.
type ByteProgressSink interface {
	ProgressSink
	// ByteProgress reports bytes downloaded out of total for an episode.
	ByteProgress(key EpisodeKey, downloaded, total int64)
}
