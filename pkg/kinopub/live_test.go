// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package kinopub_test

// Интеграционные тесты против живого API.
//
// Выключены по умолчанию: им нужна настоящая авторизация, и в CI им делать
// нечего. Включаются переменной окружения:
//
//	KINOPUB_LIVE=1 go test ./pkg/kinopub/... -run Live -v
//
// Авторизация подхватывается из ~/.config/kinopub/credentials.enc — ни токен,
// ни пароль в тесты не передаются и нигде не печатаются.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/niazlv/kinopub-downloader/pkg/kinopub"
	"github.com/niazlv/kinopub-downloader/pkg/kinopub/session"
)

func liveClient(t *testing.T) *kinopub.Client {
	t.Helper()
	if os.Getenv("KINOPUB_LIVE") == "" {
		t.Skip("живые тесты выключены; включить: KINOPUB_LIVE=1")
	}
	s, err := session.Load()
	if err != nil {
		t.Fatalf("сессия не загружена: %v", err)
	}
	st := s.Status()
	t.Logf("метод=%s источник=%s обновляемая=%v база=%s",
		st.Method, st.TokenSource, st.CanRefresh, st.APIBase)
	if !st.Ready() {
		t.Skipf("нет API-токена.\n%s", st.Hint())
	}
	c, err := s.Client(context.Background(), &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("клиент не собран: %v", err)
	}
	return c
}

func TestLive_UserValidatesToken(t *testing.T) {
	c := liveClient(t)
	u, err := c.User(context.Background())
	if err != nil {
		if errors.Is(err, kinopub.ErrUnauthorized) {
			t.Fatalf("токен отвергнут; обнови сессию: kinopub login --qr")
		}
		t.Fatalf("User: %v", err)
	}
	if u.Username == "" {
		t.Fatal("аккаунт без имени — ответ разобран неверно")
	}
	t.Logf("аккаунт ok, подписка активна=%v", u.Subscription.Active)
}

// TestLive_DiscoverCatalogEndpoints — не столько тест, сколько разведка:
// печатает реальные формы ответов каталожных эндпоинтов, чтобы клиент
// писался по фактам, а не по догадкам. Ничего не утверждает про формы,
// которые ещё не реализованы, — только показывает их.
func TestLive_DiscoverCatalogEndpoints(t *testing.T) {
	c := liveClient(t)
	probes := []string{
		"/items?perpage=2",
		"/items?type=serial&perpage=2",
		"/items?title=twin&perpage=2",
		"/items/search?q=matrix&perpage=2",
		"/items/fresh?perpage=2",
		"/items/popular?perpage=2",
		"/types",
		"/genres",
		"/watching/serials",
		"/history?perpage=2",
		"/bookmarks",
	}
	for _, p := range probes {
		body, code, err := rawGet(t, c, p)
		switch {
		case err != nil:
			t.Logf("%-34s ОШИБКА %v", p, err)
		default:
			t.Logf("%-34s %d  %s", p, code, shape(body))
		}
	}
}

// rawGet ходит по произвольному пути тем же клиентом. Нужен только разведке:
// в рабочем коде каждый эндпоинт имеет типизированный метод.
func rawGet(t *testing.T, c *kinopub.Client, path string) ([]byte, int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		c.Base()+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// shape печатает только структуру ответа: имена ключей и типы, без значений.
// Значения могут содержать подписанные URL — им не место в выводе теста.
func shape(b []byte) string {
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return "не JSON-объект"
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+describe(v[k]))
	}
	return strings.Join(parts, " ")
}

func describe(v any) string {
	switch t := v.(type) {
	case []any:
		if len(t) == 0 {
			return "[0]"
		}
		if m, ok := t[0].(map[string]any); ok {
			return fmt.Sprintf("[%d]{%s}", len(t), strings.Join(sortedKeys(m), ","))
		}
		return fmt.Sprintf("[%d]", len(t))
	case map[string]any:
		return "{" + strings.Join(sortedKeys(t), ",") + "}"
	case float64:
		return "=num"
	case string:
		return "=str"
	case bool:
		return "=bool"
	case nil:
		return "=null"
	}
	return ""
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) > 14 {
		ks = append(ks[:14:14], "…")
	}
	return ks
}
