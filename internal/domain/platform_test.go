// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package domain

import "testing"

func TestParsePlatformLink(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want PlatformLink
		ok   bool
	}{
		{"title page", "https://kino.example/#/title/201",
			PlatformLink{Origin: "https://kino.example", TitleID: 201}, true},
		{"episode page", "https://kino.example/#/title/201/1234",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, EpisodeID: 1234}, true},
		{"trailing slash", "https://kino.example/#/title/201/",
			PlatformLink{Origin: "https://kino.example", TitleID: 201}, true},
		{"no slash after hash", "https://kino.example/#title/201",
			PlatformLink{Origin: "https://kino.example", TitleID: 201}, true},
		{"surrounding whitespace", "  https://kino.example/#/title/201\n",
			PlatformLink{Origin: "https://kino.example", TitleID: 201}, true},
		{"platform under a path", "https://host.example/kino/#/title/7",
			PlatformLink{Origin: "https://host.example/kino", TitleID: 7}, true},
		{"app shell file under a path", "https://host.example/kino/index.html#/title/7",
			PlatformLink{Origin: "https://host.example/kino", TitleID: 7}, true},
		{"path routing", "https://kino.example/title/201/1234",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, EpisodeID: 1234}, true},
		{"http and port", "http://127.0.0.1:8080/#/title/3",
			PlatformLink{Origin: "http://127.0.0.1:8080", TitleID: 3}, true},
		{"query string before the hash", "https://kino.example/?utm=x#/title/201/s1e3",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, Ref: EpisodeRef{Season: 1, Episode: 3}}, true},
		{"trailing slash after the episode", "https://kino.example/#/title/201/s1e3/",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, Ref: EpisodeRef{Season: 1, Episode: 3}}, true},
		{"episode by season and number", "https://kino.example/#/title/201/s1e3",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, Ref: EpisodeRef{Season: 1, Episode: 3}}, true},
		{"zero-padded and upper case", "https://kino.example/#/title/201/S01E03",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, Ref: EpisodeRef{Season: 1, Episode: 3}}, true},
		{"a whole season", "https://kino.example/#/title/201/s2/",
			PlatformLink{Origin: "https://kino.example", TitleID: 201, Ref: EpisodeRef{Season: 2}}, true},
		{"season zero", "https://kino.example/#/title/201/s0e1", PlatformLink{}, false},
		{"episode zero", "https://kino.example/#/title/201/s1e0", PlatformLink{}, false},
		{"malformed suffix", "https://kino.example/#/title/201/s1e", PlatformLink{}, false},
		{"unknown suffix", "https://kino.example/#/title/201/latest", PlatformLink{}, false},

		{"kino.pub page", "https://kino.watch/item/view/38290", PlatformLink{}, false},
		{"kino.pub page with episode", "https://kino.watch/item/view/38290/s1e1", PlatformLink{}, false},
		{"podcast feed", "https://kino.watch/podcast/get/1/tok", PlatformLink{}, false},
		{"platform search page", "https://kino.example/#/search/naruto", PlatformLink{}, false},
		{"platform home", "https://kino.example/#/", PlatformLink{}, false},
		{"zero id", "https://kino.example/#/title/0", PlatformLink{}, false},
		{"zero episode", "https://kino.example/#/title/1/0", PlatformLink{}, false},
		{"absurdly long id", "https://kino.example/#/title/1234567890123456789012", PlatformLink{}, false},
		{"extra segment", "https://kino.example/#/title/1/2/3", PlatformLink{}, false},
		{"no host", "/#/title/201", PlatformLink{}, false},
		{"ftp", "ftp://kino.example/#/title/201", PlatformLink{}, false},
		{"empty", "", PlatformLink{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParsePlatformLink(tt.url)
			if ok != tt.ok {
				t.Fatalf("ParsePlatformLink(%q) ok = %v, want %v", tt.url, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ParsePlatformLink(%q) = %+v, want %+v", tt.url, got, tt.want)
			}
		})
	}
}

// The suffix of a platform link narrows a run exactly like the one of a
// kino.pub page link, so ApplyURLEpisodeRef needs no second code path.
func TestEpisodeRefFromPlatformLink(t *testing.T) {
	tests := []struct {
		url  string
		want EpisodeRef
	}{
		{"https://kino.example/#/title/201/s1e3", EpisodeRef{Season: 1, Episode: 3}},
		{"https://kino.example/#/title/201/s2", EpisodeRef{Season: 2}},
		{"https://kino.example/#/title/201/1234", EpisodeRef{}},
		{"https://kino.example/#/title/201", EpisodeRef{}},
		{"https://kino.watch/item/view/38290/s1e1", EpisodeRef{Season: 1, Episode: 1}},
	}
	for _, tt := range tests {
		if got := EpisodeRefFromURL(tt.url); got != tt.want {
			t.Errorf("EpisodeRefFromURL(%q) = %+v, want %+v", tt.url, got, tt.want)
		}
	}
}
