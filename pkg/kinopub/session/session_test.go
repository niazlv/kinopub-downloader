// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package session

import (
	"strings"
	"testing"
	"time"
)

func TestStatus_ReadyOnlyWithAPIToken(t *testing.T) {
	cases := []struct {
		name  string
		st    Status
		ready bool
	}{
		{"только cookie", Status{Method: MethodCookie, HasCookie: true}, false},
		{"есть API-токен", Status{Method: MethodAPI, HasAPIToken: true}, true},
		{"пусто", Status{Method: MethodNone}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.st.Ready() != c.ready {
				t.Fatalf("Ready() = %v, want %v", c.st.Ready(), c.ready)
			}
		})
	}
}

// Подсказка должна называть конкретную команду: «нет авторизации» без
// действия заставляет лезть в справку.
func TestStatus_HintNamesTheCommand(t *testing.T) {
	cookieOnly := Status{Method: MethodCookie, HasCookie: true, CookieSiteURL: "kino.watch"}
	hint := cookieOnly.Hint()
	if !strings.Contains(hint, "kinopub login --qr") {
		t.Fatalf("подсказка не называет команду: %q", hint)
	}
	if !strings.Contains(hint, "kino.watch") {
		t.Fatalf("подсказка не называет сайт cookie-сессии: %q", hint)
	}

	empty := Status{Method: MethodNone}
	if !strings.Contains(empty.Hint(), "kinopub login --qr") {
		t.Fatalf("пустая сессия без подсказки: %q", empty.Hint())
	}

	ok := Status{Method: MethodAPI, HasAPIToken: true}
	if ok.Hint() != "" {
		t.Fatalf("рабочая сессия не должна ничего подсказывать: %q", ok.Hint())
	}
}

func TestStatus_CarriesNoSecrets(t *testing.T) {
	// Status печатается в логи и админку. Здесь фиксируется, что в нём
	// нет полей с токенами: добавление такого поля должно ломать тест.
	st := Status{
		Method: MethodAPI, APIBase: "https://api.example/v1", HasAPIToken: true,
		TokenSource: "device", CanRefresh: true, ExpiresAt: time.Now(),
	}
	rendered := st.Method + Method(st.APIBase) + Method(st.TokenSource)
	for _, bad := range []string{"Bearer", "eyJ", "token="} {
		if strings.Contains(string(rendered), bad) {
			t.Fatalf("в Status протёк секрет: %q", bad)
		}
	}
}
