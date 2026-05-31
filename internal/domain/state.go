package domain

import "time"

// DownloadState persists which episodes have been completed for a series.
type DownloadState struct {
	Series    SeriesID                `json:"series"`
	Completed map[string]CompletedRec `json:"completed"` // key = "S{n}E{n}"
}

// CompletedRec records metadata about a completed episode download.
type CompletedRec struct {
	Season      int       `json:"season"`
	Episode     int       `json:"episode"`
	Path        string    `json:"path"`
	Bytes       int64     `json:"bytes"`
	CompletedAt time.Time `json:"completed_at"`
}
