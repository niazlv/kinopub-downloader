// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

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
	"net/url"
	"os"
	"sort"
	"strconv"
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

// TestLive_DiscoverPlaybackPlane разбирает вторую половину API — ту, что
// отдаёт подписанные ссылки. От неё зависит схема доставки (ADR-0003).
//
// Значения URL не печатаются: они дают доступ к контенту. В вывод идут
// только хост, набор заполненных полей, дорожки и качества.
func TestLive_DiscoverPlaybackPlane(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	// Берём фильм: у него один Video, разбирать проще, чем сериал.
	page, err := c.Items(ctx, kinopub.ItemsQuery{Type: "movie", PerPage: 1, Sort: kinopub.SortViewsDesc})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("листинг фильмов: %v", err)
	}
	id := page.Items[0].ID

	item, err := c.Item(ctx, strconv.Itoa(id))
	if err != nil {
		t.Fatalf("Item(%d): %v", id, err)
	}
	t.Logf("item %d %q (%d), type=%s videos=%d seasons=%d",
		item.ID, item.Title, item.Year, item.Type, len(item.Videos), len(item.Seasons))

	if len(item.Videos) == 0 {
		t.Fatal("у фильма нет videos — модель расходится с API")
	}
	v := item.Videos[0]
	t.Logf("video: duration=%dс files=%d", v.Duration, len(v.Files))
	if len(v.Files) == 0 {
		t.Fatal("нет файлов — доставлять нечего")
	}

	for _, f := range v.Files {
		t.Logf("  файл: codec=%-5s %s %dx%d | http=%v hls=%v hls4=%v hls2=%v | хост=%s",
			f.Codec, f.Quality, f.W, f.H,
			f.URL.HTTP != "", f.URL.HLS != "", f.URL.HLS4 != "", f.URL.HLS2 != "",
			hostOf(firstNonEmpty(f.URL.HLS4, f.URL.HLS, f.URL.HTTP)))
	}

	// Сырой ответ — за дорожками озвучки и субтитрами: в модели их пока нет,
	// а для платформы переключение дубляжей обязательно.
	raw, _, err := rawGet(t, c, fmt.Sprintf("/items/%d", id))
	if err != nil {
		t.Fatalf("сырой item: %v", err)
	}
	var de struct {
		Item struct {
			Videos []map[string]any `json:"videos"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &de); err == nil && len(de.Item.Videos) > 0 {
		t.Log("поля video в сыром ответе —")
		for _, k := range allKeys(de.Item.Videos[0]) {
			t.Logf("    %-14s %s", k, kindOf(de.Item.Videos[0][k]))
		}
	}

	// Ссылку для фазы 0 отдаём ЧЕРЕЗ ФАЙЛ, а не через stdout и не через
	// argv: подписанный URL — это доступ к контенту, ему не место ни в
	// логах теста, ни в списке процессов.
	if out := os.Getenv("KINOPUB_URL_OUT"); out != "" {
		u := firstNonEmpty(v.Files[0].URL.HLS4, v.Files[0].URL.HLS, v.Files[0].URL.HTTP)
		if err := os.WriteFile(out, []byte(u), 0o600); err != nil {
			t.Fatalf("запись URL: %v", err)
		}
		t.Logf("ссылка записана в %s (0600), хост %s", out, hostOf(u))
	}
}

// TestLive_DiscoverLinkLifetime закрывает пункт (в) фазы 0 авторитетно:
// срок жизни приходит полем API, а не выковыривается из query подписи.
// Заодно выясняет хост сегментов — allowed_origins обязан включать и его.
func TestLive_DiscoverLinkLifetime(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	page, err := c.Items(ctx, kinopub.ItemsQuery{Type: "movie", PerPage: 1, Sort: kinopub.SortViewsDesc})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("листинг: %v", err)
	}
	raw, _, err := rawGet(t, c, fmt.Sprintf("/items/%d", page.Items[0].ID))
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	var de struct {
		Item struct {
			Videos []struct {
				Files []struct {
					Quality   string `json:"quality"`
					Codec     string `json:"codec"`
					ExpiresAt int64  `json:"expires_at"`
					URL       struct {
						HLS4 string `json:"hls4"`
					} `json:"url"`
				} `json:"files"`
			} `json:"videos"`
		} `json:"item"`
	}
	if err := json.Unmarshal(raw, &de); err != nil || len(de.Item.Videos) == 0 {
		t.Fatalf("разбор: %v", err)
	}
	files := de.Item.Videos[0].Files
	if len(files) == 0 {
		t.Fatal("нет файлов")
	}
	for _, f := range files[:min(3, len(files))] {
		exp := time.Unix(f.ExpiresAt, 0)
		t.Logf("%-5s %-6s expires_at=%d -> через %s",
			f.Codec, f.Quality, f.ExpiresAt, time.Until(exp).Round(time.Minute))
	}

	// Хост сегментов: тянем мастер-плейлист и смотрим, куда он указывает.
	master, code, err := rawGet2(t, files[0].URL.HLS4)
	if err != nil || code != 200 {
		t.Fatalf("мастер-плейлист: код %d %v", code, err)
	}
	base, _ := url.Parse(files[0].URL.HLS4)
	hosts := map[string]int{}
	for _, line := range strings.Split(string(master), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if ref, err := base.Parse(line); err == nil {
			hosts[ref.Host]++
		}
	}
	t.Logf("мастер на хосте %s; варианты указывают на: %v", base.Host, hosts)
}

// rawGet2 ходит по абсолютному URL без авторизации — подписанные ссылки
// её не требуют (это и есть вывод пункта «г» фазы 0).
func rawGet2(t *testing.T, u string) ([]byte, int, error) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Get(u)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return b, resp.StatusCode, err
}

// TestLive_DiscoverTrackFieldTypes — типы полей дорожек и субтитров.
// Проверяются по НЕСКОЛЬКИМ элементам: одиночная проба уже дважды соврала
// (duration.average, imdb), потому что null и целое встречаются рядом
// с дробным и строкой.
func TestLive_DiscoverTrackFieldTypes(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	page, err := c.Items(ctx, kinopub.ItemsQuery{Type: "movie", PerPage: 5, Sort: kinopub.SortViewsDesc})
	if err != nil {
		t.Fatalf("листинг: %v", err)
	}

	audio := map[string]map[string]int{}
	subs := map[string]map[string]int{}
	files := map[string]map[string]int{}
	note := func(acc map[string]map[string]int, m map[string]any) {
		for k, v := range m {
			if acc[k] == nil {
				acc[k] = map[string]int{}
			}
			acc[k][kindOf(v)]++
		}
	}

	for _, it := range page.Items {
		raw, _, err := rawGet(t, c, fmt.Sprintf("/items/%d", it.ID))
		if err != nil {
			continue
		}
		var de struct {
			Item struct {
				Videos []struct {
					Audios    []map[string]any `json:"audios"`
					Subtitles []map[string]any `json:"subtitles"`
					Files     []map[string]any `json:"files"`
				} `json:"videos"`
			} `json:"item"`
		}
		if json.Unmarshal(raw, &de) != nil || len(de.Item.Videos) == 0 {
			continue
		}
		v := de.Item.Videos[0]
		for _, a := range v.Audios {
			note(audio, a)
		}
		for _, x := range v.Subtitles {
			note(subs, x)
		}
		for _, f := range v.Files {
			note(files, f)
		}
	}
	report := func(name string, acc map[string]map[string]int) {
		t.Logf("%s —", name)
		keys := make([]string, 0, len(acc))
		for k := range acc {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("    %-12s %v", k, acc[k])
		}
	}
	report("audios", audio)
	report("subtitles", subs)
	report("files", files)
}

// TestLive_TracksDecode утверждает, что дорожки и субтитры разбираются
// типизированно, а подписи дорожек пригодны для меню выбора.
func TestLive_TracksDecode(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	page, err := c.Items(ctx, kinopub.ItemsQuery{Type: "movie", PerPage: 3, Sort: kinopub.SortViewsDesc})
	if err != nil {
		t.Fatalf("листинг: %v", err)
	}
	var checked int
	for _, it := range page.Items {
		item, err := c.Item(ctx, strconv.Itoa(it.ID))
		if err != nil || len(item.Videos) == 0 {
			continue
		}
		v := item.Videos[0]
		if len(v.Audios) == 0 {
			continue
		}
		checked++
		var withoutAuthor int
		for _, a := range v.Audios {
			if a.Label() == "" {
				t.Fatalf("дорожка без подписи: lang=%q codec=%q", a.Lang, a.Codec)
			}
			if a.Author == nil {
				withoutAuthor++
			}
		}
		if len(v.Files) > 0 && v.Files[0].Expires().IsZero() {
			t.Fatal("у файла нет срока жизни — тикет не сможет его выставить")
		}
		t.Logf("%q: дорожек %d (без студии %d), субтитров %d, срок ссылок %s",
			item.Title, len(v.Audios), withoutAuthor, len(v.Subtitles),
			time.Until(v.Files[0].Expires()).Round(time.Hour))
		t.Logf("    примеры подписей: %s", strings.Join(labels(v.Audios, 4), " | "))
	}
	if checked == 0 {
		t.Fatal("ни одного фильма с дорожками — разбор сломан")
	}
}

func labels(as []kinopub.AudioTrack, n int) []string {
	out := make([]string, 0, n)
	for _, a := range as {
		if len(out) == n {
			break
		}
		out = append(out, a.Label())
	}
	return out
}

// TestLive_DiscoverHistoryShape — формы истории просмотров.
// Нужны для ОДНОКРАТНОГО импорта при заведении пользователя: семья смотрит
// через площадку годами, и обнулить это при переезде значит вернуть всех
// в старое приложение в первый же вечер.
func TestLive_DiscoverHistoryShape(t *testing.T) {
	c := liveClient(t)
	for _, path := range []string{"/history?perpage=3", "/watching/serials", "/watching/movies"} {
		body, code, err := rawGet(t, c, path)
		if err != nil || code != 200 {
			t.Logf("%-26s код %d %v", path, code, err)
			continue
		}
		var env map[string]any
		if json.Unmarshal(body, &env) != nil {
			continue
		}
		t.Logf("%-26s %d", path, code)
		for _, k := range allKeys(env) {
			arr, ok := env[k].([]any)
			if !ok || len(arr) == 0 {
				t.Logf("    %-12s %s", k, kindOf(env[k]))
				continue
			}
			m, ok := arr[0].(map[string]any)
			if !ok {
				continue
			}
			t.Logf("    %s[%d] —", k, len(arr))
			for _, f := range allKeys(m) {
				t.Logf("        %-12s %s", f, kindOf(m[f]))
			}
		}
	}
}

// TestLive_DiscoverWatchingPositions ищет ПОЗИЦИЮ просмотра по сериям.
// Счётчики watched/total из /watching/serials говорят «сколько», но не
// «докуда», а для «продолжить с места» нужно именно второе.
func TestLive_DiscoverWatchingPositions(t *testing.T) {
	c := liveClient(t)
	body, code, err := rawGet(t, c, "/watching/serials")
	if err != nil || code != 200 {
		t.Fatalf("watching: код %d %v", code, err)
	}
	var wl struct {
		Items []struct {
			ID      int    `json:"id"`
			Title   string `json:"title"`
			Watched int    `json:"watched"`
			Total   int    `json:"total"`
		} `json:"items"`
	}
	if json.Unmarshal(body, &wl) != nil || len(wl.Items) == 0 {
		t.Fatal("пустой список просматриваемого")
	}
	first := wl.Items[0]
	t.Logf("сериал %d %q: просмотрено %d из %d", first.ID, first.Title, first.Watched, first.Total)

	raw, _, err := rawGet(t, c, fmt.Sprintf("/items/%d", first.ID))
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	var de struct {
		Item struct {
			Seasons []struct {
				Number   int `json:"number"`
				Episodes []struct {
					Number   int            `json:"number"`
					Duration int            `json:"duration"`
					Watching map[string]any `json:"watching"`
				} `json:"episodes"`
			} `json:"seasons"`
		} `json:"item"`
	}
	if json.Unmarshal(raw, &de) != nil || len(de.Item.Seasons) == 0 {
		t.Fatal("нет сезонов")
	}
	var shown int
	for _, s := range de.Item.Seasons {
		for _, e := range s.Episodes {
			if len(e.Watching) == 0 || shown >= 5 {
				continue
			}
			keys := make([]string, 0, len(e.Watching))
			for k, v := range e.Watching {
				keys = append(keys, fmt.Sprintf("%s=%v(%s)", k, v, kindOf(v)))
			}
			sort.Strings(keys)
			t.Logf("  s%02de%02d (%dс): watching{%s}", s.Number, e.Number, e.Duration,
				strings.Join(keys, " "))
			shown++
		}
	}
	if shown == 0 {
		t.Fatal("поле watching не найдено ни у одной серии")
	}

	// Повтор /history — прошлый раз отвалился по таймауту.
	if hb, hc, err := rawGet(t, c, "/history?perpage=2"); err == nil && hc == 200 {
		var env map[string]any
		if json.Unmarshal(hb, &env) == nil {
			if arr, ok := env["history"].([]any); ok && len(arr) > 0 {
				if m, ok := arr[0].(map[string]any); ok {
					t.Log("/history[0] —")
					for _, k := range allKeys(m) {
						t.Logf("    %-12s %s", k, kindOf(m[k]))
					}
				}
			}
		}
	} else {
		t.Logf("/history недоступен: код %d %v", hc, err)
	}
}

// TestLive_WatchingSemantics проверяет ЕДИНСТВЕННОЕ место, где семантика
// снята с наблюдения, а не подтверждена: единицы Watching.Time и значения
// Watching.Status.
//
// Если площадка отдаёт время не в секундах, импорт истории молча поставит
// всем неверные позиции — заметить это можно будет только по жалобам.
// Поэтому здесь утверждение, а не лог.
func TestLive_WatchingSemantics(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()

	items, err := c.WatchingSerials(ctx)
	if err != nil {
		t.Fatalf("watching: %v", err)
	}
	if len(items) == 0 {
		t.Skip("аккаунт ничего не смотрит — проверять нечего")
	}

	var checked int
	for _, it := range items {
		if checked >= 3 {
			break
		}
		detail, err := c.Item(ctx, strconv.Itoa(it.ID))
		if err != nil {
			continue
		}
		for _, s := range detail.Seasons {
			for _, v := range s.Episodes {
				if !v.Watching.Started() || v.Duration <= 0 {
					continue
				}
				checked++
				// Позиция в СЕКУНДАХ обязана укладываться в длительность
				// (с запасом на округление). Миллисекунды дали бы значение
				// в тысячу раз больше и здесь бы вылезли.
				if v.Watching.Time > v.Duration+60 {
					t.Fatalf("позиция %d больше длительности %d — Time не в секундах",
						v.Watching.Time, v.Duration)
				}
				if v.Watching.Status < -1 || v.Watching.Status > 1 {
					t.Fatalf("неожиданный Status=%d, ожидались -1, 0 или 1", v.Watching.Status)
				}
				t.Logf("%q s%02de%02d: позиция %dс из %dс, статус %d",
					it.Title, s.Number, v.Number, v.Watching.Time, v.Duration, v.Watching.Status)
				if checked >= 3 {
					break
				}
			}
		}
	}
	if checked == 0 {
		t.Skip("не нашлось начатых серий с известной длительностью")
	}
}

// TestLive_DiscoverWatchingPagination выясняет, отдаёт ли «я смотрю» весь
// список или только первую страницу. У аккаунта их под две тысячи, а первый
// ответ вернул ровно 50 — слишком круглое число, чтобы быть совпадением.
func TestLive_DiscoverWatchingPagination(t *testing.T) {
	c := liveClient(t)
	for _, q := range []string{"", "?page=2", "?perpage=100", "?page=2&perpage=100"} {
		body, code, err := rawGet(t, c, "/watching/serials"+q)
		if err != nil || code != 200 {
			t.Logf("%-24s код %d %v", q, code, err)
			continue
		}
		var env map[string]any
		if json.Unmarshal(body, &env) != nil {
			continue
		}
		n := 0
		if arr, ok := env["items"].([]any); ok {
			n = len(arr)
		}
		var first string
		if arr, ok := env["items"].([]any); ok && n > 0 {
			if m, ok := arr[0].(map[string]any); ok {
				first = fmt.Sprintf("%v", m["title"])
			}
		}
		label := q
		if label == "" {
			label = "(без параметров)"
		}
		t.Logf("%-24s items=%-4d ключи=%v первый=%.30s", label, n, sortedKeys(env), first)
	}
}

// TestLive_DiscoverFullWatchlist ищет эндпоинт с ПОЛНЫМ списком
// отслеживаемого: /watching/serials жёстко отдаёт 50, а в интерфейсе
// площадки счётчик заметно больше.
func TestLive_DiscoverFullWatchlist(t *testing.T) {
	c := liveClient(t)
	probes := []string{
		"/watching/serials?subscribed=1",
		"/watching/toggle-watchlist",
		"/items?subscribed=1&perpage=100",
		"/items/subscribed?perpage=100",
		"/bookmarks?perpage=100",
		"/watching?perpage=100",
		"/watching/serials/all",
	}
	for _, p := range probes {
		body, code, err := rawGet(t, c, p)
		if err != nil {
			t.Logf("%-38s ОШИБКА %v", p, err)
			continue
		}
		var env map[string]any
		if json.Unmarshal(body, &env) != nil {
			t.Logf("%-38s %d (не JSON)", p, code)
			continue
		}
		n := -1
		for _, k := range []string{"items", "history", "bookmarks"} {
			if arr, ok := env[k].([]any); ok {
				n = len(arr)
				break
			}
		}
		t.Logf("%-38s %d  элементов=%d  ключи=%v", p, code, n, sortedKeys(env))
	}
}

// TestLive_SubscribedPagination: сколько тайтлов на самом деле в подписке.
func TestLive_SubscribedPagination(t *testing.T) {
	c := liveClient(t)
	body, _, err := rawGet(t, c, "/items?subscribed=1&perpage=50")
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	var env struct {
		Items      []map[string]any `json:"items"`
		Pagination struct {
			Current, PerPage, Total, TotalItems int
		} `json:"pagination"`
	}
	if json.Unmarshal(body, &env) != nil {
		t.Fatal("разбор")
	}
	p := env.Pagination
	t.Logf("подписка: всего %d тайтлов, страниц %d по %d (сейчас страница %d, элементов %d)",
		p.TotalItems, p.Total, p.PerPage, p.Current, len(env.Items))

	// Вторая страница обязана отличаться, иначе пагинация декоративная.
	b2, _, err := rawGet(t, c, "/items?subscribed=1&perpage=50&page=2")
	if err == nil {
		var e2 struct {
			Items []map[string]any `json:"items"`
		}
		if json.Unmarshal(b2, &e2) == nil && len(e2.Items) > 0 && len(env.Items) > 0 {
			same := fmt.Sprintf("%v", e2.Items[0]["id"]) == fmt.Sprintf("%v", env.Items[0]["id"])
			t.Logf("страница 2: элементов %d, первый совпадает с первой страницей: %v",
				len(e2.Items), same)
		}
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(нет)"
	}
	return u.Host
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
