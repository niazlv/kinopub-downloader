// Copyright (C) 2026 niazlv <niazlv03@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package platformscraper feeds the HLS pipeline from a platform title page.
//
// Платформа — сайт поверх контракта манифеста скачивания (см. manifestscraper):
// резолв она делает сама и отдаёт медиа через свой шлюз. Её страница тайтла —
// приложение без содержимого в HTML, читать там нечего; зато залогиненному
// пользователю платформа отдаёт по API список серий и манифест на каждую.
// Из них собирается тот же PagePlaylist, что HTML- и API-скраперы добывают со
// страницы площадки, — и весь конвейер дальше не знает, откуда он пришёл.
//
// Авторизация — сессия самой платформы, не площадки: клиент приходит уже с её
// кукой, а запросы уходят только на хост из ссылки.
package platformscraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/niazlv/kinopub-downloader/internal/domain"
	"github.com/niazlv/kinopub-downloader/internal/services/manifestscraper"
)

// Compile-time interface satisfaction check.
var _ domain.PageScraper = (*Scraper)(nil)

// manifestConcurrency ограничивает одновременные запросы манифестов. Каждый
// манифест — резолв на площадке от имени платформы; сотня серий залпом
// выглядела бы для площадки как атака, а по две — как просмотр.
const manifestConcurrency = 2

// maxBodySize ограничивает чтение ответов API: список серий и манифест — это
// килобайты, и принимать по этим адресам что угодно неразумно.
const maxBodySize = 4 << 20

// requestTimeout — на один запрос к платформе. Манифест внутри себя ходит на
// площадку, поэтому запас больше, чем нужно локальному API.
const requestTimeout = 30 * time.Second

// Scraper implements domain.PageScraper for platform title links.
type Scraper struct {
	client   *http.Client
	log      domain.Logger
	seasons  domain.Selection
	episodes domain.Selection
}

// Option configures the Scraper.
type Option func(*Scraper)

// WithSelection limits the episodes a manifest is requested for to those the
// run will download anyway. Each manifest is a resolve on the platform's
// side, so asking for a whole series to then download one episode would be
// wasteful — and the selection here is the same one the engine applies, so
// the playlist and the run agree on what "the link names" means: a season and
// episode in the link, unless explicit --seasons/--episodes override them.
func WithSelection(seasons, episodes domain.Selection) Option {
	return func(s *Scraper) { s.seasons, s.episodes = seasons, episodes }
}

// New builds a Scraper on an HTTP client that already carries the platform
// session (httpx.WithAuth scopes the cookie to the site of the link).
func New(client *http.Client, log domain.Logger, opts ...Option) *Scraper {
	if client == nil {
		client = http.DefaultClient
	}
	s := &Scraper{
		client:   client,
		log:      nopLogger{},
		seasons:  domain.Selection{All: true},
		episodes: domain.Selection{All: true},
	}
	if log != nil {
		s.log = log.Component("platformscraper")
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// titleResponse — ответ GET /api/v1/titles/{id}: тайтл и его серии.
type titleResponse struct {
	Title struct {
		ID     int64  `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		HasArt bool   `json:"hasArt"`
	} `json:"title"`
	Episodes []titleEpisode `json:"episodes"`
}

type titleEpisode struct {
	ID          int64  `json:"id"`
	SeasonNo    int    `json:"seasonNo"`
	EpisodeNo   int    `json:"episodeNo"`
	Title       string `json:"title"`
	DurationSec int    `json:"durationSec"`
}

// numbering is the season and episode the pipeline files an episode under.
// A movie has no season on the platform; the pipeline numbers everything as
// a series and puts a movie in S01E01, as it does for a kino.pub page.
func (e titleEpisode) numbering() (season, episode int) {
	season, episode = e.SeasonNo, e.EpisodeNo
	if season == 0 {
		season = 1
	}
	if episode == 0 {
		episode = 1
	}
	return season, episode
}

// ExtractAllSeasons resolves a platform title link into the playlist of its
// episodes — all of them, or the one the link names.
func (s *Scraper) ExtractAllSeasons(ctx context.Context, pageURL string) (*domain.PagePlaylist, error) {
	link, ok := domain.ParsePlatformLink(pageURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a platform title page", domain.ErrInvalidInputURL, pageURL)
	}
	api := link.Origin + "/api/v1"
	titleID := strconv.FormatInt(link.TitleID, 10)

	var tr titleResponse
	if err := s.getJSON(ctx, api+"/titles/"+titleID, &tr); err != nil {
		return nil, fmt.Errorf("title %s: %w", titleID, err)
	}

	if len(tr.Episodes) == 0 {
		return nil, fmt.Errorf("title %s (%s) has no episodes", titleID, tr.Title.Title)
	}
	eps := s.wanted(link, tr.Episodes)
	if len(eps) == 0 {
		switch {
		case link.EpisodeID != 0:
			return nil, fmt.Errorf("episode %d is not part of title %s (%s)",
				link.EpisodeID, titleID, tr.Title.Title)
		case !link.Ref.IsZero():
			return nil, fmt.Errorf("title %s (%s) has no %s", titleID, tr.Title.Title, refLabel(link.Ref))
		default:
			return nil, fmt.Errorf("no episode of title %s (%s) matches the selection", titleID, tr.Title.Title)
		}
	}

	s.log.Info("platform title",
		domain.F("title", tr.Title.Title),
		domain.F("kind", tr.Title.Kind),
		domain.F("episodes", len(eps)),
	)

	episodes, err := s.manifests(ctx, api, eps)
	if err != nil {
		return nil, err
	}

	pl := &domain.PagePlaylist{
		ItemID:   int(link.TitleID),
		Title:    tr.Title.Title,
		Episodes: episodes,
		Seasons:  seasonsOf(episodes),
	}
	if tr.Title.HasArt {
		pl.Poster = api + "/titles/" + titleID + "/art"
	}
	return pl, nil
}

// wanted keeps the episodes the run is going to download: the one an episode
// id names, or those the selection admits — which already carries the season
// and episode of the link, see WithSelection.
func (s *Scraper) wanted(link domain.PlatformLink, all []titleEpisode) []titleEpisode {
	var out []titleEpisode
	for _, e := range all {
		if link.EpisodeID != 0 {
			if e.ID == link.EpisodeID {
				out = append(out, e)
			}
			continue
		}
		season, episode := e.numbering()
		if s.seasons.Matches(season) && s.episodes.Matches(episode) {
			out = append(out, e)
		}
	}
	return out
}

// refLabel names a season or an episode the way the link does.
func refLabel(r domain.EpisodeRef) string {
	if r.Episode > 0 {
		return fmt.Sprintf("s%02de%02d", r.Season, r.Episode)
	}
	return fmt.Sprintf("season %d", r.Season)
}

// manifests fetches a manifest per episode, a few at a time, in the order of
// the episodes. The first failure cancels the rest: a session that expired on
// episode three will not improve by episode thirty.
func (s *Scraper) manifests(ctx context.Context, api string, eps []titleEpisode) ([]domain.PageEpisode, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make([]domain.PageEpisode, len(eps))
	errs := make([]error, len(eps))
	sem := make(chan struct{}, manifestConcurrency)
	var wg sync.WaitGroup
	for i, e := range eps {
		wg.Add(1)
		go func(i int, e titleEpisode) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()

			pe, err := s.manifest(ctx, api, e)
			if err != nil {
				errs[i] = err
				cancel()
				return
			}
			out[i] = pe
		}(i, e)
	}
	wg.Wait()

	// Report the failure that started it, not the cancellations it caused.
	var first error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !errors.Is(err, context.Canceled) {
			return nil, err
		}
		if first == nil {
			first = err
		}
	}
	if first != nil {
		return nil, first
	}
	return out, nil
}

// manifest asks the platform for one episode's manifest and turns it into a
// playlist entry.
func (s *Scraper) manifest(ctx context.Context, api string, e titleEpisode) (domain.PageEpisode, error) {
	body, err := s.get(ctx, api+"/episodes/"+strconv.FormatInt(e.ID, 10)+"/manifest")
	if err != nil {
		return domain.PageEpisode{}, fmt.Errorf("episode %d (s%02de%02d): %w", e.ID, e.SeasonNo, e.EpisodeNo, err)
	}
	m, err := manifestscraper.Parse(body)
	if err != nil {
		return domain.PageEpisode{}, fmt.Errorf("episode %d (s%02de%02d): %w", e.ID, e.SeasonNo, e.EpisodeNo, err)
	}

	season, episode := e.numbering()
	s.log.Debug("platform manifest",
		domain.F("episode_id", e.ID),
		domain.F("season", season),
		domain.F("episode", episode),
		domain.F("expires_at", m.ExpiresAt.Format(time.RFC3339)),
	)
	return domain.PageEpisode{
		ManifestURL:  m.URL,
		MediaID:      int(e.ID),
		EpisodeTitle: e.Title,
		Season:       season,
		Episode:      episode,
		Duration:     e.DurationSec,
	}, nil
}

// get performs one API request. Accept asks for JSON explicitly: to a browser
// the platform serves its app shell, and a wrong answer should fail as a
// status, not as an HTML body that does not decode.
func (s *Scraper) get(ctx context.Context, rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	host := req.URL.Host
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("%w (%s answered %s)", domain.ErrPlatformSessionRequired, host, resp.Status)
	case http.StatusNotFound:
		return nil, fmt.Errorf("not found on %s", host)
	default:
		return nil, fmt.Errorf("%s answered %s", host, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", host, err)
	}
	return body, nil
}

func (s *Scraper) getJSON(ctx context.Context, rawURL string, v any) error {
	body, err := s.get(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("response is not the platform API: %w", err)
	}
	return nil
}

// seasonsOf counts episodes per season, seasons in ascending order.
func seasonsOf(eps []domain.PageEpisode) []domain.PageSeason {
	counts := make(map[int]int)
	for _, e := range eps {
		counts[e.Season]++
	}
	out := make([]domain.PageSeason, 0, len(counts))
	for n, c := range counts {
		out = append(out, domain.PageSeason{Season: n, Count: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Season < out[j].Season })
	return out
}

// nopLogger stands in when no logger is given.
type nopLogger struct{}

func (nopLogger) Debug(string, ...domain.Field)        {}
func (nopLogger) Info(string, ...domain.Field)         {}
func (nopLogger) Warn(string, ...domain.Field)         {}
func (nopLogger) Error(string, ...domain.Field)        {}
func (l nopLogger) With(...domain.Field) domain.Logger { return l }
func (l nopLogger) Component(string) domain.Logger     { return l }
