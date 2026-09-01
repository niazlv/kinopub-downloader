// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package apiclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

func TestUserSendsBearerAndUserAgent(t *testing.T) {
	var gotAuth, gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		w.Write([]byte(`{"status":200,"user":{"username":"vali","subscription":{"active":true,"end_time":1798887794,"days":122.8}}}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "tok123",
		WithUserAgent("Android KinoPub/1.34 (Linux;Android 16) ExoPlayerLib/2.11.8"))
	u, err := c.User(context.Background())
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if u.Username != "vali" || !u.Subscription.Active {
		t.Errorf("user = %+v", u)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "Android KinoPub/") {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotPath != "/v1/user" {
		t.Errorf("path = %q, want /v1/user", gotPath)
	}
}

func TestItemMovieDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/items/126715" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"status":200,"item":{
			"id":126715,"title":"The Brink of War","type":"movie","year":2026,
			"posters":{"big":"https://img/big.jpg","small":"https://img/small.jpg"},
			"videos":[{"id":1,"number":1,"snumber":0,"title":"","duration":7146,
				"files":[
					{"codec":"h264","w":1920,"h":800,"quality":"1080p","quality_id":3,
						"url":{"http":"https://cdn/f.mp4","hls":"https://cdn/f.m3u8","hls4":"https://api/hls4/x.m3u8"}}
				]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "t")
	it, err := c.Item(context.Background(), "126715")
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if it.ID != 126715 || it.Type != "movie" || len(it.Videos) != 1 {
		t.Fatalf("item = %+v", it)
	}
	f := it.Videos[0].Files[0]
	if f.URL.HLS4 != "https://api/hls4/x.m3u8" || f.QualityID != 3 || f.Codec != "h264" {
		t.Errorf("file = %+v", f)
	}
	if it.Posters.PosterURL() != "https://img/big.jpg" {
		t.Errorf("poster = %q", it.Posters.PosterURL())
	}
}

func TestItemSerialDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":200,"item":{
			"id":66136,"title":"Show","type":"serial",
			"seasons":[{"id":10,"number":1,"title":"S1","episodes":[
				{"id":5,"number":1,"snumber":1,"title":"Pilot","duration":2600,
					"files":[{"codec":"h265","quality":"2160p","quality_id":4,"url":{"hls4":"https://api/e1.m3u8"}}]}
			]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "t")
	it, err := c.Item(context.Background(), "66136")
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if len(it.Seasons) != 1 || len(it.Seasons[0].Episodes) != 1 {
		t.Fatalf("seasons = %+v", it.Seasons)
	}
	ep := it.Seasons[0].Episodes[0]
	if ep.SNumber != 1 || ep.Number != 1 || ep.Title != "Pilot" {
		t.Errorf("episode = %+v", ep)
	}
}

func TestUnauthorizedMapsToSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"status":401}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "expired")
	_, err := c.User(context.Background())
	if !errors.Is(err, domain.ErrAPIUnauthorized) {
		t.Fatalf("err = %v, want ErrAPIUnauthorized", err)
	}
}

func TestNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "t")
	if _, err := c.Item(context.Background(), "1"); err == nil {
		t.Fatal("want error on HTTP 500")
	}
}
