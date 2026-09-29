// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// Журнал просмотров аккаунта — «История» у площадки: по записи на серию,
// с позицией и временем последнего просмотра, от свежих к старым.
//
// Как и «смотрю», нужен платформе только для однократного переноса: в «смотрю»
// нет того, что уже досмотрели и убрали, а в журнале есть. Только чтение.

// HistoryEntry — запись журнала: серия (Media) тайтла (Item) и позиция в ней.
type HistoryEntry struct {
	Time      int   `json:"time"` // позиция, секунды
	Counter   int   `json:"counter"`
	FirstSeen int64 `json:"first_seen"`
	LastSeen  int64 `json:"last_seen"`
	Item      Item  `json:"item"`
	Media     Video `json:"media"`
}

// LastSeenTime — когда серию смотрели последний раз.
func (e HistoryEntry) LastSeenTime() time.Time { return time.Unix(e.LastSeen, 0) }

type historyResponse struct {
	History    []HistoryEntry `json:"history"`
	Pagination Pagination     `json:"pagination"`
}

// maxHistoryPages — предохранитель: по двадцать записей это двадцать тысяч.
const maxHistoryPages = 1000

// HistorySince — записи журнала, просмотренные не раньше since. Журнал
// общий за годы (десятки тысяч записей), поэтому обход идёт от свежих и
// останавливается на первой записи старше границы.
func (c *Client) HistorySince(ctx context.Context, since time.Time) ([]HistoryEntry, error) {
	var out []HistoryEntry
	for page := 1; page <= maxHistoryPages; page++ {
		var r historyResponse
		q := url.Values{"page": {strconv.Itoa(page)}}
		if err := c.getOnce(ctx, "/history", q, &r); err != nil {
			return out, err
		}
		for _, e := range r.History {
			if e.LastSeenTime().Before(since) {
				return out, nil
			}
			out = append(out, e)
		}
		if !r.Pagination.HasNext() || len(r.History) == 0 {
			break
		}
	}
	return out, nil
}

// Повторы для разовых обходов — переноса истории и папок.
//
// Площадка за общим аккаунтом и VPN-узлом изредка обрывает соединение или
// отдаёт пустое тело посреди обхода, и без повтора перенос папки в двадцать
// страниц терял всё на восемнадцатой. Обычный get не повторяет намеренно:
// плееру важнее быстро узнать, что площадка лежит, чем дождаться её.
var onceDelays = []time.Duration{time.Second, 3 * time.Second, 6 * time.Second}

// getOnce — get с повторами на сбоях связи и просьбах притормозить. Отказ
// в доступе и «не найдено» не повторяются: второй раз ответ будет тем же.
func (c *Client) getOnce(ctx context.Context, path string, q url.Values, out any) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = c.get(ctx, path, q, out); err == nil {
			return nil
		}
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) ||
			ctx.Err() != nil || attempt >= len(onceDelays) {
			return err
		}
		c.logger.Debug("kinopub: повтор запроса", "path", path, "attempt", attempt+1, "err", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(onceDelays[attempt]):
		}
	}
}
