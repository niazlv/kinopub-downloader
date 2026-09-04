// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import "context"

// Просматриваемое на стороне площадки.
//
// Единственное назначение в контексте платформы — ОДНОКРАТНЫЙ перенос
// накопленной истории. Обратная запись не предусмотрена намеренно.

// WatchingItem — тайтл из списка «смотрю».
type WatchingItem struct {
	ID      int     `json:"id"`
	Title   string  `json:"title"`
	Type    string  `json:"type"`
	Subtype string  `json:"subtype"`
	Posters Posters `json:"posters"`

	// Watched и Total заполняются только для сериалов: сколько серий
	// просмотрено из скольких. Позиции внутри серии здесь нет — она
	// приходит с деталями тайтла, в Video.Watching.
	Watched int `json:"watched"`
	Total   int `json:"total"`
	New     int `json:"new"`
}

type watchingResponse struct {
	Status int            `json:"status"`
	Items  []WatchingItem `json:"items"`
}

// WatchingSerials — сериалы, которые аккаунт смотрит.
func (c *Client) WatchingSerials(ctx context.Context) ([]WatchingItem, error) {
	var r watchingResponse
	if err := c.get(ctx, "/watching/serials", nil, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// WatchingMovies — начатые фильмы. Счётчиков серий здесь нет.
func (c *Client) WatchingMovies(ctx context.Context) ([]WatchingItem, error) {
	var r watchingResponse
	if err := c.get(ctx, "/watching/movies", nil, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// Watching возвращает всё просматриваемое одним списком.
//
// Отказ одной половины не отменяет вторую: перенести часть истории лучше,
// чем не перенести ничего.
func (c *Client) Watching(ctx context.Context) ([]WatchingItem, error) {
	serials, serr := c.WatchingSerials(ctx)
	movies, merr := c.WatchingMovies(ctx)
	if serr != nil && merr != nil {
		return nil, serr
	}
	return append(serials, movies...), nil
}
