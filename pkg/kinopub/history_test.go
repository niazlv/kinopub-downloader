// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Журнал идёт от свежих к старым: обход кончается на первой записи старше
// границы, не дочитывая годы истории.
func TestHistorySinceStopsAtBoundary(t *testing.T) {
	now := time.Now().Unix()
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/history" {
			t.Errorf("path = %q", r.URL.Path)
		}
		p := r.URL.Query().Get("page")
		pages = append(pages, p)
		// Страница 1 — два дня назад и сегодня, страница 2 — пять и двадцать
		// дней назад, страница 3 не должна понадобиться.
		ages := map[string][2]int64{"1": {0, 2}, "2": {5, 20}, "3": {30, 31}}[p]
		fmt.Fprintf(w, `{"history":[
			{"time":737,"last_seen":%d,"item":{"id":16739,"type":"serial","title":"Проза"},"media":{"id":1,"number":12,"snumber":2,"duration":1420}},
			{"time":1420,"last_seen":%d,"item":{"id":26,"type":"movie","title":"Сияние"},"media":{"id":2,"number":1,"snumber":0,"duration":6480}}],
			"pagination":{"total":1253,"current":%s,"perpage":20}}`,
			now-ages[0]*86400, now-ages[1]*86400, p)
	}))
	defer srv.Close()

	got, err := New(srv.Client(), srv.URL+"/v1", "t").HistorySince(context.Background(), time.Now().Add(-14*24*time.Hour))
	if err != nil {
		t.Fatalf("HistorySince: %v", err)
	}
	if len(got) != 3 || got[0].Media.SNumber != 2 || got[0].Media.Number != 12 || got[0].Time != 737 || got[2].Item.ID != 16739 {
		t.Errorf("записи = %+v", got)
	}
	if fmt.Sprint(pages) != "[1 2]" {
		t.Errorf("страницы = %v, третья не нужна", pages)
	}
}

// Оборванное соединение посреди обхода повторяется, а не губит весь перенос.
func TestBookmarkItemsRetriesBrokenPage(t *testing.T) {
	defer func(d []time.Duration) { onceDelays = d }(onceDelays)
	onceDelays = []time.Duration{time.Millisecond, time.Millisecond}
	failed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		if p == "2" && !failed {
			failed = true
			// Пустое тело — как у площадки за VPN-узлом.
			return
		}
		fmt.Fprintf(w, `{"items":[{"id":%s,"type":"serial"}],"pagination":{"total":3,"current":%s}}`, p, p)
	}))
	defer srv.Close()

	got, err := New(srv.Client(), srv.URL+"/v1", "t").BookmarkItems(context.Background(), 1)
	if err != nil || len(got) != 3 || !failed {
		t.Fatalf("items=%+v err=%v failed=%v", got, err, failed)
	}
}
