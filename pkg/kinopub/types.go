// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: MIT OR GPL-3.0-or-later

package kinopub

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// The JSON API wraps every payload in an envelope with a numeric "status" that
// mirrors the HTTP status. Only the fields the downloader needs are modeled;
// unknown fields are ignored by encoding/json.

// User is the authenticated account, used to validate a token and report
// subscription state.
type User struct {
	Username     string       `json:"username"`
	Subscription Subscription `json:"subscription"`
}

// Subscription describes the account's active plan.
type Subscription struct {
	Active  bool    `json:"active"`
	EndTime int64   `json:"end_time"`
	Days    float64 `json:"days"`
}

// Item is a movie or a serial. Movies carry Videos; serials carry Seasons whose
// episodes have the same media shape as a movie's single video.
type Item struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	Type    string   `json:"type"` // "movie", "serial", "tvshow", …
	Subtype string   `json:"subtype"`
	Year    int      `json:"year"`
	Posters Posters  `json:"posters"`
	Videos  []Video  `json:"videos"`
	Seasons []Season `json:"seasons"`

	Plot      string `json:"plot"`
	Genres    []Ref  `json:"genres"`
	Countries []Ref  `json:"countries"`
	// Finished — сериал завершён. Для инкрементального обхода это подсказка,
	// что новых серий ждать не стоит.
	Finished bool `json:"finished"`

	// IMDB и Kinopoisk — ЧИСЛА или null (проверено живьём, не строки).
	// Указатели, потому что ноль и «неизвестно» здесь разные вещи.
	IMDB      *int `json:"imdb"`
	Kinopoisk *int `json:"kinopoisk"`

	// Duration — объект И в листинге, и в детали (проверено обоими
	// эндпоинтами). Не путать с Video.Duration, которое просто секунды.
	Duration Duration `json:"duration"`

	// UpdatedAt и CreatedAt — unix-секунды. UpdatedAt вместе с сортировкой
	// "-updated" даёт инкрементальный обход.
	UpdatedAt int64 `json:"updated_at"`
	CreatedAt int64 `json:"created_at"`
}

// Duration — суммарная и средняя длительность по элементу, в секундах.
//
// Оба поля float64, хотя выглядят целыми на большинстве элементов: average
// бывает дробным (1224.642857142857 у сериала с 14 сериями), и жёсткий int
// роняет разбор ВСЕГО элемента на таком. Одна проба показала бы целое —
// поэтому тип выбран по наблюдённому отказу, а не по одному образцу.
// Вызывающему, которому нужны целые секунды, служат аксессоры.
type Duration struct {
	Average float64 `json:"average"`
	Total   float64 `json:"total"`
}

// TotalSeconds — суммарная длительность целыми секундами.
func (d Duration) TotalSeconds() int { return int(d.Total) }

// AverageSeconds — средняя длительность, округлённая до секунды.
func (d Duration) AverageSeconds() int { return int(math.Round(d.Average)) }

// UpdatedTime — время последнего изменения. Нулевое, когда API его не дал.
func (i Item) UpdatedTime() time.Time {
	if i.UpdatedAt == 0 {
		return time.Time{}
	}
	return time.Unix(i.UpdatedAt, 0)
}

// IMDBID возвращает идентификатор в КАНОНИЧЕСКОЙ форме — "tt0898266".
//
// API отдаёт голое число (898266). Отдать его как есть значило бы, что
// склейка с любым другим источником молча не сработает: там идентификатор
// с префиксом и ведущими нулями.
func (i Item) IMDBID() string {
	if i.IMDB == nil || *i.IMDB <= 0 {
		return ""
	}
	return fmt.Sprintf("tt%07d", *i.IMDB)
}

// KinopoiskID возвращает идентификатор Кинопоиска строкой, пустую при его
// отсутствии.
func (i Item) KinopoiskID() string {
	if i.Kinopoisk == nil || *i.Kinopoisk <= 0 {
		return ""
	}
	return strconv.Itoa(*i.Kinopoisk)
}

// Posters holds the poster URLs at the sizes the API offers.
type Posters struct {
	Small  string `json:"small"`
	Medium string `json:"medium"`
	Big    string `json:"big"`
	Wide   string `json:"wide"`
}

// Season groups a serial's episodes.
type Season struct {
	ID       int     `json:"id"`
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	Episodes []Video `json:"episodes"`
}

// Video is one playable unit — a movie's video or a serial episode.
type Video struct {
	ID       int    `json:"id"`
	Number   int    `json:"number"`  // episode number within the season (1 for a movie)
	SNumber  int    `json:"snumber"` // season number (0 for a movie)
	Title    string `json:"title"`
	Duration int    `json:"duration"` // seconds
	Files    []File `json:"files"`

	// Audios — дорожки озвучки. Для русского контента их бывает десяток,
	// и переключение между ними — базовая функция, а не украшение.
	Audios []AudioTrack `json:"audios"`
	// Subtitles — субтитры; часть вшита в контейнер (Embed), часть лежит
	// отдельным файлом по URL.
	Subtitles []Subtitle `json:"subtitles"`

	// Thumbnail — кадр из серии. Витрине он нужен: список серий одними
	// номерами читается заметно хуже, чем кадрами.
	Thumbnail string `json:"thumbnail"`

	// Watching — состояние просмотра НА СТОРОНЕ ПЛОЩАДКИ.
	//
	// Платформа сюда не пишет никогда: раздельное состояние у каждого
	// домочадца и есть причина её существования. Читается ровно один раз —
	// при переносе накопленной истории на новый аккаунт.
	Watching Watching `json:"watching"`
}

// Watching — позиция просмотра по версии площадки.
type Watching struct {
	// Status: 1 — просмотрено, 0 — в процессе, -1 — не начато.
	// Семантика снята с наблюдения и проверяется живым тестом; если она
	// окажется иной, тест упадёт, а не тихо испортит импорт.
	Status int `json:"status"`
	// Time — позиция в СЕКУНДАХ, как и Duration рядом.
	Time int `json:"time"`
}

// Started сообщает, начат ли просмотр.
func (w Watching) Started() bool { return w.Time > 0 || w.Status > 0 }

// NamedRef — вложенный справочный объект вида {id, title, short_title}.
//
// Поле id намеренно не моделируется: его тип не проверен, а платформе он
// не нужен — нужна подпись дорожки. Неизвестные поля encoding/json
// игнорирует, так что и рисковать незачем.
type NamedRef struct {
	Title      string `json:"title"`
	ShortTitle string `json:"short_title"`
}

// Name возвращает короткое имя, а при его отсутствии — полное.
func (n NamedRef) Name() string {
	if n.ShortTitle != "" {
		return n.ShortTitle
	}
	return n.Title
}

// AudioTrack — одна дорожка озвучки.
type AudioTrack struct {
	ID       int    `json:"id"`
	Index    int    `json:"index"`
	Lang     string `json:"lang"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`

	// Author — студия озвучки. ОБЪЕКТ, а не строка, и у части дорожек
	// равен null (проверено: 18 из 53 у выборки популярных фильмов).
	// Отсюда указатель: «оригинальная дорожка без студии» — законный случай.
	Author *NamedRef `json:"author"`
	// Type — вид дорожки (дубляж, многоголосый, оригинал). Тоже объект.
	Type NamedRef `json:"type"`
}

// Label — человекочитаемая подпись дорожки для меню выбора.
func (a AudioTrack) Label() string {
	switch {
	case a.Author != nil && a.Author.Name() != "":
		return a.Author.Name()
	case a.Type.Name() != "":
		return a.Type.Name()
	default:
		return a.Lang
	}
}

// Subtitle — дорожка субтитров.
type Subtitle struct {
	Lang   string `json:"lang"`
	Embed  bool   `json:"embed"`
	Forced bool   `json:"forced"`
	URL    string `json:"url"`
	File   string `json:"file"`
	// Shift — сдвиг тайминга. float64 по тому же уроку, что и duration:
	// целое в выборке не доказывает, что дробного не бывает.
	Shift float64 `json:"shift"`
}

// File is one encoded rendition of a video. The signed URLs are ready to
// download without any auth header; the hls4 master additionally exposes every
// quality, audio track and subtitle for the download pipeline to pick from.
type File struct {
	Codec     string `json:"codec"` // "h264", "h265"
	W         int    `json:"w"`
	H         int    `json:"h"`
	Quality   string `json:"quality"`    // "1080p", "2160p", …
	QualityID int    `json:"quality_id"` // 1..4, higher is better
	URL       URLSet `json:"url"`

	// ExpiresAt — unix-секунды, когда подписанные ссылки перестанут
	// работать. Приходит полем API, поэтому срок известен точно и его
	// не надо выковыривать из подписи в query.
	ExpiresAt int64 `json:"expires_at"`
}

// Expires — срок жизни ссылок файла. Нулевое время, если API его не дал.
func (f File) Expires() time.Time {
	if f.ExpiresAt == 0 {
		return time.Time{}
	}
	return time.Unix(f.ExpiresAt, 0)
}

// URLSet holds the signed delivery URLs for a File.
type URLSet struct {
	HTTP string `json:"http"` // progressive MP4
	HLS  string `json:"hls"`  // CDN HLS master
	HLS4 string `json:"hls4"` // API-hosted HLS master (all qualities/audios/subs)
	HLS2 string `json:"hls2"`
}

// PosterURL returns the best available poster URL, largest first.
func (p Posters) PosterURL() string {
	for _, u := range []string{p.Big, p.Wide, p.Medium, p.Small} {
		if u != "" {
			return u
		}
	}
	return ""
}
