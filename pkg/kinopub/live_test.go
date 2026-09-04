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
	"math"
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

// TestLive_DiscoverItemFields печатает ПОЛНЫЙ набор полей одного элемента —
// от него зависит маппинг в каноническую модель: внешние идентификаторы для
// склейки между площадками и поле, по которому строится дельта-синк.
func TestLive_DiscoverItemFields(t *testing.T) {
	c := liveClient(t)
	body, _, err := rawGet(t, c, "/items?perpage=1")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Items) == 0 {
		t.Fatalf("разбор: %v", err)
	}
	t.Log("поля item (имя: тип) —")
	for _, k := range allKeys(env.Items[0]) {
		t.Logf("    %-18s %s", k, kindOf(env.Items[0][k]))
	}

	// Вложенные id — тот же класс расхождений, что у /types и /genres:
	// их надо смотреть отдельно, «массив из {id,title}» типа не выдаёт.
	for _, f := range []string{"genres", "countries"} {
		arr, ok := env.Items[0][f].([]any)
		if !ok || len(arr) == 0 {
			continue
		}
		if m, ok := arr[0].(map[string]any); ok {
			t.Logf("    %s[0].id = %s, .title = %s", f, kindOf(m["id"]), kindOf(m["title"]))
		}
	}
	if d, ok := env.Items[0]["duration"].(map[string]any); ok {
		t.Logf("    duration.average = %s, .total = %s", kindOf(d["average"]), kindOf(d["total"]))
	}
}

// TestLive_DiscoverSortOptions ищет порядок, пригодный для инкрементального
// обхода: краулер обязан уметь забирать «что изменилось», а не весь каталог.
func TestLive_DiscoverSortOptions(t *testing.T) {
	c := liveClient(t)
	for _, sort := range []string{"-updated", "updated", "-created", "-id", "id"} {
		body, code, err := rawGet(t, c, "/items?perpage=1&sort="+sort)
		if err != nil {
			t.Logf("sort=%-10s ОШИБКА %v", sort, err)
			continue
		}
		var env struct {
			Items []struct {
				ID        int   `json:"id"`
				UpdatedAt int64 `json:"updated_at"`
				CreatedAt int64 `json:"created_at"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &env)
		if code != 200 || len(env.Items) == 0 {
			t.Logf("sort=%-10s %d (не принят)", sort, code)
			continue
		}
		t.Logf("sort=%-10s %d  первый id=%d updated_at=%d created_at=%d",
			sort, code, env.Items[0].ID, env.Items[0].UpdatedAt, env.Items[0].CreatedAt)
	}
}

// TestLive_DiscoverExternalIDTypes выясняет ФАКТИЧЕСКИЙ тип imdb/kinopoisk.
// От него зависит склейка одного тайтла между площадками, а null в первом
// попавшемся элементе типа не выдаёт: угадать здесь — значит получить
// молчаливо неработающий матчинг.
func TestLive_DiscoverExternalIDTypes(t *testing.T) {
	c := liveClient(t)
	body, _, err := rawGet(t, c, "/items?perpage=50&sort=-views")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("разбор: %v", err)
	}
	seen := map[string]map[string]int{"imdb": {}, "kinopoisk": {}}
	samples := map[string]string{}
	for _, it := range env.Items {
		for _, f := range []string{"imdb", "kinopoisk"} {
			k := kindOf(it[f])
			seen[f][k]++
			if it[f] != nil && samples[f] == "" {
				samples[f] = fmt.Sprintf("%v", it[f])
			}
		}
	}
	for _, f := range []string{"imdb", "kinopoisk"} {
		t.Logf("%-10s типы=%v пример=%q", f, seen[f], samples[f])
	}
}

// TestLive_DiscoverItemDetailShape сверяет форму ДЕТАЛЬНОГО ответа с формой
// листинга. Расхождение (например, duration числом вместо объекта) сломало бы
// Item(), на котором держится скачивание в CLI, — поэтому проверяется, а не
// предполагается.
func TestLive_DiscoverItemDetailShape(t *testing.T) {
	c := liveClient(t)
	list, _, err := rawGet(t, c, "/items?perpage=1&sort=-views")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var le struct {
		Items []struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(list, &le); err != nil || len(le.Items) == 0 {
		t.Fatalf("разбор листинга: %v", err)
	}
	id := le.Items[0].ID

	detail, code, err := rawGet(t, c, fmt.Sprintf("/items/%d", id))
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	t.Logf("GET /items/%d -> код %d, тело %d байт", id, code, len(detail))
	if len(detail) == 0 {
		t.Fatalf("пустое тело при коде %d", code)
	}
	var de struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(detail, &de); err != nil {
		t.Fatalf("разбор детали: %v", err)
	}
	t.Logf("item %d (%s): поля, важные для модели —", id, le.Items[0].Type)
	for _, k := range []string{"duration", "imdb", "kinopoisk", "updated_at", "created_at",
		"subtype", "type", "finished", "videos", "seasons", "genres", "countries", "posters"} {
		v, ok := de.Item[k]
		if !ok {
			t.Logf("    %-12s ОТСУТСТВУЕТ в детали", k)
			continue
		}
		t.Logf("    %-12s %s", k, kindOf(v))
	}
}

// TestLive_CatalogMethods проверяет НАПИСАННЫЕ методы против живого API.
// Разведка показывает формы, этот тест утверждает, что клиент их разбирает.
func TestLive_CatalogMethods(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	t.Run("Items с пагинацией", func(t *testing.T) {
		page, err := c.Items(ctx, kinopub.ItemsQuery{PerPage: 5, Sort: kinopub.SortViewsDesc})
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		if len(page.Items) != 5 {
			t.Fatalf("получено %d элементов, ожидалось 5", len(page.Items))
		}
		if page.Pagination.TotalItems == 0 || !page.Pagination.HasNext() {
			t.Fatalf("пагинация разобрана неверно: %+v", page.Pagination)
		}
		it := page.Items[0]
		if it.ID == 0 || it.Title == "" || it.Type == "" {
			t.Fatalf("элемент разобран неполно: id=%d title=%q type=%q", it.ID, it.Title, it.Type)
		}
		t.Logf("всего в каталоге: %d элементов, %d страниц по %d",
			page.Pagination.TotalItems, page.Pagination.Total, page.Pagination.PerPage)
	})

	t.Run("внешние идентификаторы канонизированы", func(t *testing.T) {
		page, err := c.Items(ctx, kinopub.ItemsQuery{PerPage: 20, Sort: kinopub.SortViewsDesc})
		if err != nil {
			t.Fatalf("Items: %v", err)
		}
		var withIMDB int
		for _, it := range page.Items {
			id := it.IMDBID()
			if id == "" {
				continue
			}
			withIMDB++
			// Форма обязана быть "tt" + минимум 7 цифр, иначе склейка
			// с другими источниками не сработает.
			if !strings.HasPrefix(id, "tt") || len(id) < 9 {
				t.Fatalf("IMDb id не канонизирован: %q", id)
			}
		}
		if withIMDB == 0 {
			t.Fatal("ни у одного из 20 популярных элементов нет imdb — разбор сломан")
		}
		t.Logf("imdb заполнен у %d из %d", withIMDB, len(page.Items))
	})

	t.Run("Search ранжирует", func(t *testing.T) {
		page, err := c.Search(ctx, "матрица", kinopub.ItemsQuery{PerPage: 5})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(page.Items) == 0 {
			t.Fatal("поиск ничего не вернул")
		}
		t.Logf("найдено %d, первый: %q (%d)", page.Pagination.TotalItems,
			page.Items[0].Title, page.Items[0].Year)
	})

	t.Run("Types и Genres", func(t *testing.T) {
		types, err := c.Types(ctx)
		if err != nil || len(types) == 0 {
			t.Fatalf("Types: %v (%d)", err, len(types))
		}
		genres, err := c.Genres(ctx)
		if err != nil || len(genres) == 0 {
			t.Fatalf("Genres: %v (%d)", err, len(genres))
		}
		t.Logf("типов %d, жанров %d", len(types), len(genres))
	})

	t.Run("ChangedSince останавливается на границе", func(t *testing.T) {
		since := time.Now().Add(-6 * time.Hour)
		var seen int
		var oldest time.Time
		err := c.ChangedSince(ctx, since, kinopub.ItemsQuery{PerPage: 25}, func(it kinopub.Item) error {
			seen++
			oldest = it.UpdatedTime()
			if seen >= 200 { // предохранитель, чтобы тест не гулял по каталогу
				return kinopub.ErrStopWalk
			}
			return nil
		})
		if err != nil {
			t.Fatalf("ChangedSince: %v", err)
		}
		if seen == 0 {
			t.Skip("за 6 часов ничего не менялось — границу проверить нечем")
		}
		if !oldest.After(since) {
			t.Fatalf("обход вышел за границу: последний %s, граница %s", oldest, since)
		}
		t.Logf("изменилось за 6 часов: %d элементов, самый старый %s", seen, oldest.Format(time.RFC3339))
	})
}

// TestLive_DiscoverRefIDTypes: /types отдаёт id строкой, /genres — числом.
// Расхождение реальное, зафиксировано тестом, чтобы один общий тип для обоих
// больше не казался хорошей идеей.
func TestLive_DiscoverRefIDTypes(t *testing.T) {
	c := liveClient(t)
	for _, path := range []string{"/types", "/genres"} {
		body, _, err := rawGet(t, c, path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var env struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &env); err != nil || len(env.Items) == 0 {
			t.Fatalf("%s разбор: %v", path, err)
		}
		first := env.Items[0]
		t.Logf("%-9s id=%s title=%s type=%s", path,
			kindOf(first["id"]), kindOf(first["title"]), kindOf(first["type"]))
	}
}

func kindOf(v any) string {
	switch t := v.(type) {
	case []any:
		if len(t) > 0 {
			if m, ok := t[0].(map[string]any); ok {
				return fmt.Sprintf("массив[%d] из {%s}", len(t), strings.Join(sortedKeys(m), ","))
			}
		}
		return fmt.Sprintf("массив[%d]", len(t))
	case map[string]any:
		return "объект{" + strings.Join(sortedKeys(t), ",") + "}"
	case float64:
		if t == math.Trunc(t) {
			return "число(целое)"
		}
		return "число(дробное)"
	case string:
		return "строка"
	case bool:
		return "булево"
	case nil:
		return "null"
	}
	return "?"
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

	// Лимит совпадает с клиентским: детальный ответ по длинному сериалу
	// перешагивает мегабайт, и меньший потолок молча рвёт JSON.
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
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

// allKeys — без обрезки: при разборе формы элемента нужен полный список.
func allKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
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
