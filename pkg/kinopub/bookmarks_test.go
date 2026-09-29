// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBookmarkFoldersDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bookmarks" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"status":200,"items":[
			{"id":104510,"title":"Буду смотреть","views":44,"count":43,"created":1508258343,"updated":1765040085},
			{"id":893479,"title":"Данис","views":51,"count":419,"created":1621400056,"updated":1735341365}]}`))
	}))
	defer srv.Close()

	got, err := New(srv.Client(), srv.URL+"/v1", "t").BookmarkFolders(context.Background())
	if err != nil {
		t.Fatalf("BookmarkFolders: %v", err)
	}
	if len(got) != 2 || got[1].ID != 893479 || got[1].Title != "Данис" || got[1].Count != 419 {
		t.Errorf("folders = %+v", got)
	}
}

// Папка отдаётся страницами: собрать надо все, а не первую.
func TestBookmarkItemsWalksPages(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/bookmarks/893479" {
			t.Errorf("path = %q", r.URL.Path)
		}
		p := r.URL.Query().Get("page")
		pages = append(pages, p)
		fmt.Fprintf(w, `{"status":200,"folder":{"id":893479},
			"items":[{"id":%s0,"type":"serial","title":"t%s"},{"id":%s1,"type":"movie","title":"u%s"}],
			"pagination":{"total":3,"current":%s,"perpage":20}}`, p, p, p, p, p)
	}))
	defer srv.Close()

	got, err := New(srv.Client(), srv.URL+"/v1", "t").BookmarkItems(context.Background(), 893479)
	if err != nil {
		t.Fatalf("BookmarkItems: %v", err)
	}
	if len(got) != 6 || got[0].ID != 10 || got[5].ID != 31 || got[5].Type != "movie" {
		t.Errorf("items = %+v", got)
	}
	if fmt.Sprint(pages) != "[1 2 3]" {
		t.Errorf("pages = %v", pages)
	}
}
