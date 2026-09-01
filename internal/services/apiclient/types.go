// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package apiclient

// The JSON API wraps every payload in an envelope with a numeric "status" that
// mirrors the HTTP status. Only the fields the downloader needs are modeled;
// unknown fields are ignored by encoding/json.

// User is the authenticated account, used to validate a token and report
// subscription state.
type User struct {
	Username     string       `json:"username"`
	Subscription Subscription `json:"subscription"`
}

// Subscription describes the account's active plan.
type Subscription struct {
	Active  bool    `json:"active"`
	EndTime int64   `json:"end_time"`
	Days    float64 `json:"days"`
}

// Item is a movie or a serial. Movies carry Videos; serials carry Seasons whose
// episodes have the same media shape as a movie's single video.
type Item struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"` // "movie", "serial", "tvshow", …
	Year    int      `json:"year"`
	Posters Posters  `json:"posters"`
	Videos  []Video  `json:"videos"`
	Seasons []Season `json:"seasons"`
}

// Posters holds the poster URLs at the sizes the API offers.
type Posters struct {
	Small  string `json:"small"`
	Medium string `json:"medium"`
	Big    string `json:"big"`
	Wide   string `json:"wide"`
}

// Season groups a serial's episodes.
type Season struct {
	ID       int     `json:"id"`
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Episodes []Video `json:"episodes"`
}

// Video is one playable unit — a movie's video or a serial episode.
type Video struct {
	ID       int    `json:"id"`
	Number   int    `json:"number"`  // episode number within the season (1 for a movie)
	SNumber  int    `json:"snumber"` // season number (0 for a movie)
	Title    string `json:"title"`
	Duration int    `json:"duration"` // seconds
	Files    []File `json:"files"`
}

// File is one encoded rendition of a video. The signed URLs are ready to
// download without any auth header; the hls4 master additionally exposes every
// quality, audio track and subtitle for the download pipeline to pick from.
type File struct {
	Codec     string `json:"codec"` // "h264", "h265"
	W         int    `json:"w"`
	H         int    `json:"h"`
	Quality   string `json:"quality"`    // "1080p", "2160p", …
	QualityID int    `json:"quality_id"` // 1..4, higher is better
	URL       URLSet `json:"url"`
}

// URLSet holds the signed delivery URLs for a File.
type URLSet struct {
	HTTP string `json:"http"` // progressive MP4
	HLS  string `json:"hls"`  // CDN HLS master
	HLS4 string `json:"hls4"` // API-hosted HLS master (all qualities/audios/subs)
	HLS2 string `json:"hls2"`
}

// PosterURL returns the best available poster URL, largest first.
func (p Posters) PosterURL() string {
	for _, u := range []string{p.Big, p.Wide, p.Medium, p.Small} {
		if u != "" {
			return u
		}
	}
	return ""
}
