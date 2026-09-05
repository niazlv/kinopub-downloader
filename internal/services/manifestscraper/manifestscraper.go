// Package manifestscraper feeds the HLS pipeline from a platform manifest.
//
// Это третья реализация domain.PageScraper, рядом с HTML-скрапером и
// apiscraper. Выбор именно этого шва не случаен: манифест описывает ровно то
// же, что скраперы добывают со страницы, — мастер-плейлист и состав дорожек.
// Поэтому весь дальнейший конвейер (выбор качества, дорожек, субтитров,
// мультиплексирование, докачка) работает без единой правки, а в утилите не
// появляется ветки «а вот если источник — платформа».
//
// Авторизация площадки здесь не нужна и не используется: резолв уже сделан
// платформой, а адреса ведут через её шлюз.
package manifestscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
)

// SupportedVersion — версия контракта, которую понимает эта реализация.
// Манифест обещает: новые поля не меняют версию, поэтому неизвестные поля
// игнорируются молча, а вот рост v означает ломающее изменение.
const SupportedVersion = 1

// Track — дорожка озвучки или субтитров в манифесте.
type Track struct {
	ID       string `json:"id"`
	Language string `json:"language"`
	Title    string `json:"title"`
	URL      string `json:"url"`
}

// Rendition — качество, предлагаемое источником.
type Rendition struct {
	Height int    `json:"height"`
	Codec  string `json:"codec"`
}

// Selection — что именно просили забрать. Пустые списки означают «всё».
type Selection struct {
	MaxHeight int      `json:"maxHeight"`
	Audios    []string `json:"audios"`
	Subtitles []string `json:"subtitles"`
}

// Manifest — контракт платформы (api/openapi/download-manifest.md).
type Manifest struct {
	V          int         `json:"v"`
	Title      string      `json:"title"`
	Season     int         `json:"season"`
	Episode    int         `json:"episode"`
	Filename   string      `json:"filename"`
	Protocol   string      `json:"protocol"`
	URL        string      `json:"url"`
	ExpiresAt  time.Time   `json:"expiresAt"`
	Audios     []Track     `json:"audios"`
	Subtitles  []Track     `json:"subtitles"`
	Renditions []Rendition `json:"renditions"`
	Select     Selection   `json:"select"`
}

// ErrUnsupportedVersion возвращается, когда манифест новее утилиты.
var ErrUnsupportedVersion = errors.New("manifest version is newer than this build supports")

// maxManifestSize ограничивает чтение: манифест — это несколько килобайт, и
// принимать по этому адресу что угодно неразумно.
const maxManifestSize = 1 << 20

// Fetch забирает и проверяет манифест.
//
// Проверки сделаны здесь, а не в скрапере, чтобы ошибка всплывала при запуске,
// а не в середине скачивания: протухшую ссылку лучше назвать сразу.
func Fetch(ctx context.Context, client *http.Client, rawURL string) (*Manifest, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("--from: %q is not a valid URL", rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("--from: unsupported scheme %q", u.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errors.New("--from: link not found — it may have been revoked or mistyped")
	case http.StatusForbidden, http.StatusGone:
		return nil, errors.New("--from: link is no longer valid — it expired or was revoked")
	default:
		return nil, fmt.Errorf("--from: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}

	m, err := Parse(body)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	return m, nil
}

// Parse разбирает и проверяет тело манифеста. Вынесено отдельно от Fetch,
// потому что манифест приходит не только по ссылке-разрешению: платформа
// отдаёт его и по сессии, на каждую серию (см. platformscraper), а проверки
// у обоих путей одни.
func Parse(body []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("response is not a manifest: %w", err)
	}
	if m.V > SupportedVersion {
		return nil, fmt.Errorf("%w: manifest v%d, supported v%d — update kinopub",
			ErrUnsupportedVersion, m.V, SupportedVersion)
	}
	if m.URL == "" {
		return nil, errors.New("manifest has no media URL")
	}
	if p := strings.ToUpper(m.Protocol); p != "" && p != "HLS" {
		// Конвейер здесь один — HLS. Промолчать и запустить его на чём-то
		// другом значило бы упасть позже и невнятнее.
		return nil, fmt.Errorf("unsupported protocol %q (only HLS)", m.Protocol)
	}
	if !m.ExpiresAt.IsZero() && time.Now().After(m.ExpiresAt) {
		return nil, errors.New("link has expired — create a new one")
	}
	return &m, nil
}

// Scraper отдаёт конвейеру уже полученный манифест.
type Scraper struct {
	m      *Manifest
	logger domain.Logger
}

// New строит скрапер поверх разобранного манифеста.
func New(m *Manifest, logger domain.Logger) *Scraper {
	s := &Scraper{m: m}
	if logger != nil {
		s.logger = logger.Component("manifestscraper")
	}
	return s
}

// ExtractAllSeasons отдаёт единственную серию, описанную манифестом.
//
// baseURL игнорируется: манифест уже получен и разобран, а ходить по его
// адресу второй раз незачем — ссылка одноразовая по смыслу и живёт недолго.
func (s *Scraper) ExtractAllSeasons(_ context.Context, _ string) (*domain.PagePlaylist, error) {
	if s.m == nil {
		return nil, errors.New("manifestscraper: no manifest")
	}
	if s.logger != nil {
		s.logger.Debug("using platform manifest",
			domain.F("title", s.m.Title),
			domain.F("season", s.m.Season),
			domain.F("episode", s.m.Episode),
		)
	}
	return &domain.PagePlaylist{
		Title: s.m.Title,
		Episodes: []domain.PageEpisode{{
			ManifestURL: s.m.URL,
			Season:      s.m.Season,
			Episode:     s.m.Episode,
		}},
		Seasons: []domain.PageSeason{{Season: s.m.Season, Count: 1}},
	}, nil
}
