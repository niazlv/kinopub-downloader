// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Каталожные методы. Формы ответов сняты с живого API, а не выведены из
// документации: /items/fresh и /items/popular, например, отвечают 400 —
// их не существует, хотя имена напрашиваются.

// Pagination — конверт постраничности, общий для /items, /items/search
// и /history.
type Pagination struct {
	Current    int `json:"current"`
	PerPage    int `json:"perpage"`
	Total      int `json:"total"`       // всего СТРАНИЦ
	TotalItems int `json:"total_items"` // всего элементов
}

// HasNext сообщает, есть ли следующая страница.
func (p Pagination) HasNext() bool { return p.Current < p.Total }

// Ref — пара id/название для вложенных справочников элемента: жанры и
// страны внутри Item. Здесь id числовой (проверено).
type Ref struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// ContentType — элемент справочника /types.
//
// Отдельный тип, а не Ref: /types отдаёт id СТРОКОЙ ("movie", "serial"),
// тогда как /genres — числом. Один общий тип на оба справочника выглядит
// экономией ровно до первого запроса.
type ContentType struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Genre — жанр. Привязан к типу контента, поэтому один и тот же по названию
// жанр встречается несколько раз с разными id.
type Genre struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

// Сортировки, принятые API. Проверены живьём: "-updated" — то, на чём
// строится инкрементальный обход.
const (
	SortUpdatedDesc = "-updated"
	SortCreatedDesc = "-created"
	SortViewsDesc   = "-views"
	SortIDAsc       = "id"
)

// ItemsQuery — фильтры листинга. Нулевые поля не отправляются.
type ItemsQuery struct {
	Type    string // "movie", "serial", … — см. Types()
	Title   string // подстрока названия; API фильтрует сам
	GenreID int
	Sort    string
	Page    int
	PerPage int
}

func (q ItemsQuery) values() url.Values {
	v := url.Values{}
	if q.Type != "" {
		v.Set("type", q.Type)
	}
	if q.Title != "" {
		v.Set("title", q.Title)
	}
	if q.GenreID > 0 {
		v.Set("genre", strconv.Itoa(q.GenreID))
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if q.Page > 0 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	if q.PerPage > 0 {
		v.Set("perpage", strconv.Itoa(q.PerPage))
	}
	return v
}

// ItemsPage — страница листинга.
//
// Элементы страницы НЕ несут videos/seasons: файлы и подписанные ссылки
// отдаёт только Item(id). Это разделение принадлежит самому API, и его
// стоит сохранять: обход каталога и получение ссылки — разные по стоимости
// и по сроку годности операции.
type ItemsPage struct {
	Items      []Item     `json:"items"`
	Pagination Pagination `json:"pagination"`
}

type itemsResponse struct {
	Status     int        `json:"status"`
	Items      []Item     `json:"items"`
	Pagination Pagination `json:"pagination"`
}

// Items возвращает страницу каталога.
func (c *Client) Items(ctx context.Context, q ItemsQuery) (ItemsPage, error) {
	var r itemsResponse
	if err := c.get(ctx, "/items", q.values(), &r); err != nil {
		return ItemsPage{}, err
	}
	return ItemsPage{Items: r.Items, Pagination: r.Pagination}, nil
}

// Search ищет по названию через отдельный эндпоинт.
//
// Отличается от Items с Title: поиск ранжирует, а фильтр — отбирает.
// Для «найти по названию» нужен этот, для обхода каталога — Items.
func (c *Client) Search(ctx context.Context, query string, q ItemsQuery) (ItemsPage, error) {
	v := q.values()
	v.Set("q", query)
	var r itemsResponse
	if err := c.get(ctx, "/items/search", v, &r); err != nil {
		return ItemsPage{}, err
	}
	return ItemsPage{Items: r.Items, Pagination: r.Pagination}, nil
}

type typesResponse struct {
	Status int           `json:"status"`
	Items  []ContentType `json:"items"`
}

// Types возвращает справочник типов контента. Их id — то, что кладётся
// в ItemsQuery.Type.
func (c *Client) Types(ctx context.Context) ([]ContentType, error) {
	var r typesResponse
	if err := c.get(ctx, "/types", nil, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

type genresResponse struct {
	Status int     `json:"status"`
	Items  []Genre `json:"items"`
}

// Genres возвращает жанры вместе с типом, к которому они относятся.
func (c *Client) Genres(ctx context.Context) ([]Genre, error) {
	var r genresResponse
	if err := c.get(ctx, "/genres", nil, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// ErrStopWalk останавливает обход из колбэка без ошибки.
var ErrStopWalk = fmt.Errorf("kinopub: stop walk")

// maxWalkPages страхует от бесконечного обхода, если API соврёт о числе
// страниц. Без этого сбой на стороне площадки превращается в вечный цикл.
const maxWalkPages = 10_000

// ChangedSince обходит каталог в порядке убывания времени изменения и
// отдаёт всё, что менялось ПОСЛЕ since, останавливаясь на первом элементе
// старше границы.
//
// Это то, что нужно инкрементальному синку: полный обход каталога ради
// вчерашних правок — и лишняя нагрузка на площадку, и часы работы.
// Нулевой since означает полный обход.
//
// Порядок задаётся принудительно: обход по любой другой сортировке не
// позволяет остановиться рано, а значит теряет весь смысл.
func (c *Client) ChangedSince(ctx context.Context, since time.Time, q ItemsQuery, fn func(Item) error) error {
	q.Sort = SortUpdatedDesc
	if q.PerPage <= 0 {
		q.PerPage = 50
	}
	q.Page = 1

	for pages := 0; pages < maxWalkPages; pages++ {
		page, err := c.Items(ctx, q)
		if err != nil {
			return err
		}
		if len(page.Items) == 0 {
			return nil
		}
		for _, it := range page.Items {
			// Элементы идут от свежих к старым, поэтому первый, оказавшийся
			// не новее границы, закрывает обход целиком.
			if !since.IsZero() && !it.UpdatedTime().After(since) {
				return nil
			}
			if err := fn(it); err != nil {
				if err == ErrStopWalk {
					return nil
				}
				return err
			}
		}
		if !page.Pagination.HasNext() {
			return nil
		}
		q.Page = page.Pagination.Current + 1

		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return fmt.Errorf("kinopub: walk exceeded %d pages", maxWalkPages)
}
