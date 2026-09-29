// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"net/url"
	"strconv"
)

// Папки закладок аккаунта.
//
// Как и «смотрю», нужны платформе только для однократного переноса: у
// семейной подписки папки — единственное, что у каждого своё. Только
// чтение; обратная запись не предусмотрена намеренно.

// BookmarkFolder — папка закладок.
type BookmarkFolder struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Count int    `json:"count"` // тайтлов в папке
}

type foldersResponse struct {
	Status int              `json:"status"`
	Items  []BookmarkFolder `json:"items"`
}

// BookmarkFolders — все папки аккаунта, одним запросом.
func (c *Client) BookmarkFolders(ctx context.Context) ([]BookmarkFolder, error) {
	var r foldersResponse
	if err := c.get(ctx, "/bookmarks", nil, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// maxBookmarkPages — предохранитель от площадки, которая вечно обещает
// следующую страницу: по двадцать тайтлов это десять тысяч закладок.
const maxBookmarkPages = 500

// BookmarkItems — содержимое папки целиком. Площадка отдаёт его страницами
// по двадцать; элементы — как в каталоге, без файлов и серий.
func (c *Client) BookmarkItems(ctx context.Context, folderID int) ([]Item, error) {
	path := "/bookmarks/" + strconv.Itoa(folderID)
	var out []Item
	for page := 1; page <= maxBookmarkPages; page++ {
		var r itemsResponse
		q := url.Values{"page": {strconv.Itoa(page)}}
		if err := c.get(ctx, path, q, &r); err != nil {
			return out, err
		}
		out = append(out, r.Items...)
		if !r.Pagination.HasNext() || len(r.Items) == 0 {
			break
		}
	}
	return out, nil
}
