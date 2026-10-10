package xtream

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"wonkvod/internal/catalog"
)

const maxAPIResponse = 64 << 20

type Client struct {
	httpClient *http.Client
	baseURL    string
	username   string
	password   string

	seriesMu    sync.Mutex
	seriesCache map[string]*list.Element
	seriesLRU   *list.List
}

type seriesCacheEntry struct {
	id       string
	episodes []Episode
}

type Category struct {
	ID   string
	Name string
}

type Movie struct {
	ID         string
	Name       string
	Year       string
	CategoryID string
	Extension  string
	Revision   string
}

type Series struct {
	ID         string
	Name       string
	Year       string
	CategoryID string
	Revision   string
}

type Episode struct {
	ID        string
	Season    int
	Number    int
	Extension string
}

func NewClient(httpClient *http.Client, baseURL, username, password string) (*Client, error) {
	if httpClient == nil || username == "" || password == "" {
		return nil, errors.New("xtream: HTTP client and credentials are required")
	}
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("xtream: invalid base URL")
	}
	return &Client{
		httpClient: httpClient, baseURL: base.String(), username: username, password: password,
		seriesCache: make(map[string]*list.Element), seriesLRU: list.New(),
	}, nil
}

func (c *Client) VODCategories(ctx context.Context) ([]Category, error) {
	var raw []categoryJSON
	if err := c.get(ctx, "get_vod_categories", "", &raw); err != nil {
		return nil, err
	}
	return normalizeCategories(raw), nil
}

func (c *Client) SeriesCategories(ctx context.Context) ([]Category, error) {
	var raw []categoryJSON
	if err := c.get(ctx, "get_series_categories", "", &raw); err != nil {
		return nil, err
	}
	return normalizeCategories(raw), nil
}

func (c *Client) Movies(ctx context.Context, categoryID string) ([]Movie, error) {
	if categoryID == "" {
		return nil, errors.New("xtream: movie category ID is required")
	}
	var raw []movieJSON
	if err := c.get(ctx, "get_vod_streams", categoryID, &raw); err != nil {
		return nil, err
	}
	movies := make([]Movie, 0, len(raw))
	for _, item := range raw {
		if (item.CategoryID.String() != "" && item.CategoryID.String() != categoryID) || item.StreamID.String() == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		ext := sanitizeExtension(item.Extension)
		if ext == "" {
			ext = "mkv"
		}
		revision := item.LastModified.String()
		if revision == "" {
			revision = item.Added.String()
		}
		movies = append(movies, Movie{ID: item.StreamID.String(), Name: item.Name, Year: item.Year.String(), CategoryID: categoryID, Extension: ext, Revision: revision})
	}
	return movies, nil
}

func (c *Client) Series(ctx context.Context, categoryID string) ([]Series, error) {
	if categoryID == "" {
		return nil, errors.New("xtream: TV category ID is required")
	}
	var raw []seriesJSON
	if err := c.get(ctx, "get_series", categoryID, &raw); err != nil {
		return nil, err
	}
	series := make([]Series, 0, len(raw))
	for _, item := range raw {
		if (item.CategoryID.String() != "" && item.CategoryID.String() != categoryID) || item.SeriesID.String() == "" || strings.TrimSpace(item.Name) == "" {
			continue
		}
		series = append(series, Series{ID: item.SeriesID.String(), Name: item.Name, Year: item.Year.String(), CategoryID: categoryID, Revision: item.LastModified.String()})
	}
	return series, nil
}

func (c *Client) SeriesInfo(ctx context.Context, seriesID string) ([]Episode, error) {
	if seriesID == "" {
		return nil, errors.New("xtream: series ID is required")
	}
	c.seriesMu.Lock()
	if elem := c.seriesCache[seriesID]; elem != nil {
		c.seriesLRU.MoveToFront(elem)
		episodes := append([]Episode(nil), elem.Value.(*seriesCacheEntry).episodes...)
		c.seriesMu.Unlock()
		return episodes, nil
	}
	c.seriesMu.Unlock()
	var raw seriesInfoJSON
	if err := c.getSeriesInfo(ctx, seriesID, &raw); err != nil {
		return nil, err
	}
	normalized, err := normalizeSeriesEpisodes(raw)
	if err != nil {
		return nil, err
	}
	episodes := make([]Episode, 0, len(normalized))
	for _, normalizedItem := range normalized {
		item := normalizedItem.Item
		ext := sanitizeExtension(item.Extension)
		if ext == "" {
			ext = "mkv"
		}
		episodes = append(episodes, Episode{ID: item.ID.String(), Season: normalizedItem.Season, Number: normalizedItem.Number, Extension: ext})
	}
	c.seriesMu.Lock()
	if existing := c.seriesCache[seriesID]; existing != nil {
		c.seriesLRU.MoveToFront(existing)
		episodes = append([]Episode(nil), existing.Value.(*seriesCacheEntry).episodes...)
		c.seriesMu.Unlock()
		return episodes, nil
	}
	elem := c.seriesLRU.PushFront(&seriesCacheEntry{id: seriesID, episodes: append([]Episode(nil), episodes...)})
	c.seriesCache[seriesID] = elem
	if c.seriesLRU.Len() > 32 {
		oldest := c.seriesLRU.Back()
		delete(c.seriesCache, oldest.Value.(*seriesCacheEntry).id)
		c.seriesLRU.Remove(oldest)
	}
	c.seriesMu.Unlock()
	return episodes, nil
}

// SeriesInfoFresh bypasses the bounded traversal cache and replaces its entry.
// Production catalog synchronization uses it so newly added/removed episodes
// are visible during every check cycle.
func (c *Client) SeriesInfoFresh(ctx context.Context, seriesID string) ([]Episode, error) {
	if seriesID == "" {
		return nil, errors.New("xtream: series ID is required")
	}
	c.seriesMu.Lock()
	if existing := c.seriesCache[seriesID]; existing != nil {
		delete(c.seriesCache, seriesID)
		c.seriesLRU.Remove(existing)
	}
	c.seriesMu.Unlock()
	return c.SeriesInfo(ctx, seriesID)
}

// MovieDetails fetches rich metadata used only by opt-in metadata proofs.
func (c *Client) MovieDetails(ctx context.Context, streamID string) (catalog.MovieDetails, error) {
	if streamID == "" {
		return catalog.MovieDetails{}, errors.New("xtream: movie stream ID is required")
	}
	var raw movieInfoJSON
	if err := c.getItemInfo(ctx, "get_vod_info", "vod_id", streamID, &raw); err != nil {
		return catalog.MovieDetails{}, err
	}
	info := raw.Info
	title := firstNonEmpty(info.Name, raw.MovieData.Name)
	year := info.Year.String()
	premiere := firstNonEmpty(info.ReleaseDate, info.ReleaseDateAlt)
	if year == "" && len(premiere) >= 4 {
		year = premiere[:4]
	}
	return catalog.MovieDetails{
		ID: firstNonEmpty(raw.MovieData.StreamID.String(), streamID), Title: strings.TrimSpace(title),
		OriginalTitle: strings.TrimSpace(firstNonEmpty(info.OriginalName, info.OriginalTitle)),
		Year:          year, Plot: strings.TrimSpace(firstNonEmpty(info.Plot, info.Description)),
		Premiere:       strings.TrimSpace(premiere),
		Runtime:        runtimeMinutes(info.DurationSecs.String(), info.EpisodeRunTime.String(), info.Duration),
		DurationMillis: runtimeMilliseconds(info.DurationSecs.String(), info.EpisodeRunTime.String(), info.Duration),
		Bitrate:        info.Bitrate.Int(),
		Rating:         info.Rating.String(), Country: strings.TrimSpace(info.Country),
		Genres: splitList(info.Genre), Directors: splitList(info.Director),
		Cast: splitList(firstNonEmpty(info.Cast, info.Actors)), TMDBID: info.TMDBID.String(),
		Poster: firstNonEmpty(info.CoverBig, info.MovieImage),
		Fanart: firstListValue(info.BackdropPath), Trailer: trailerURL(info.YouTubeTrailer),
		Extension: sanitizeExtension(raw.MovieData.Extension),
	}, nil
}

// SeriesDetails fetches rich show and episode metadata. It remains separate
// from the persisted traversal catalog used by SeriesInfo.
func (c *Client) SeriesDetails(ctx context.Context, seriesID string) (catalog.SeriesDetails, error) {
	if seriesID == "" {
		return catalog.SeriesDetails{}, errors.New("xtream: series ID is required")
	}
	var raw seriesInfoJSON
	if err := c.getSeriesInfo(ctx, seriesID, &raw); err != nil {
		return catalog.SeriesDetails{}, err
	}
	premiere := firstNonEmpty(raw.Info.ReleaseDate, raw.Info.ReleaseDateAlt)
	result := catalog.SeriesDetails{
		ID: seriesID, Title: strings.TrimSpace(raw.Info.Name),
		OriginalTitle: strings.TrimSpace(firstNonEmpty(raw.Info.OriginalName, raw.Info.OriginalTitle)),
		Year:          raw.Info.Year.String(),
		Plot:          strings.TrimSpace(firstNonEmpty(raw.Info.Plot, raw.Info.Description)),
		Premiere:      strings.TrimSpace(premiere),
		Runtime:       runtimeMinutes("", raw.Info.EpisodeRunTime.String(), ""),
		Rating:        raw.Info.Rating.String(), Country: strings.TrimSpace(raw.Info.Country),
		Genres: splitList(raw.Info.Genre), Directors: splitList(raw.Info.Director),
		Cast: splitList(firstNonEmpty(raw.Info.Cast, raw.Info.Actors)), TMDBID: raw.Info.TMDBID.String(), Poster: firstNonEmpty(raw.Info.Cover, raw.Info.CoverBig),
		Fanart: firstListValue(raw.Info.BackdropPath), Trailer: trailerURL(raw.Info.YouTubeTrailer),
	}
	for _, season := range raw.Seasons {
		if number := season.Number.Int(); number > 0 {
			result.Seasons = append(result.Seasons, catalog.SeasonDetails{
				Number: number, Poster: firstNonEmpty(season.Cover, season.CoverBig),
			})
		}
	}
	if result.Year == "" && len(result.Premiere) >= 4 {
		result.Year = result.Premiere[:4]
	}
	normalized, err := normalizeSeriesEpisodes(raw)
	if err != nil {
		return catalog.SeriesDetails{}, err
	}
	for _, normalizedItem := range normalized {
		item := normalizedItem.Item
		result.Episodes = append(result.Episodes, catalog.EpisodeDetails{
			ID: item.ID.String(), Title: strings.TrimSpace(firstNonEmpty(item.Title, item.Info.Title, item.Info.Name)),
			Season: normalizedItem.Season, Number: normalizedItem.Number,
			Plot:           strings.TrimSpace(firstNonEmpty(item.Info.Plot, item.Info.Description)),
			Aired:          firstNonEmpty(item.Info.ReleaseDateISO, item.Info.ReleaseDate, item.Info.AirDate),
			Runtime:        runtimeMinutes(item.Info.DurationSecs.String(), item.Info.EpisodeRunTime.String(), item.Info.Duration),
			DurationMillis: runtimeMilliseconds(item.Info.DurationSecs.String(), item.Info.EpisodeRunTime.String(), item.Info.Duration),
			Bitrate:        item.Info.Bitrate.Int(),
			TMDBID:         item.Info.TMDBID.String(),
			Artwork:        firstNonEmpty(item.Info.MovieImage, item.Info.CoverBig),
			Rating:         item.Info.Rating.String(), Extension: sanitizeExtension(item.Extension),
		})
	}
	sort.Slice(result.Episodes, func(i, j int) bool {
		if result.Episodes[i].Season != result.Episodes[j].Season {
			return result.Episodes[i].Season < result.Episodes[j].Season
		}
		return result.Episodes[i].Number < result.Episodes[j].Number
	})
	return result, nil
}

func (c *Client) MovieSource(streamID, extension string) (*VODSource, error) {
	return newMediaSource(c.httpClient, c.baseURL, c.username, c.password, "movie", streamID, extension)
}

func (c *Client) EpisodeSource(episodeID, extension string) (*VODSource, error) {
	return newMediaSource(c.httpClient, c.baseURL, c.username, c.password, "series", episodeID, extension)
}

func (c *Client) get(ctx context.Context, action, categoryID string, target any) error {
	values := url.Values{"username": {c.username}, "password": {c.password}, "action": {action}}
	if categoryID != "" {
		values.Set("category_id", categoryID)
	}
	return c.getURL(ctx, c.baseURL+"/player_api.php?"+values.Encode(), target)
}

func (c *Client) getSeriesInfo(ctx context.Context, seriesID string, target any) error {
	values := url.Values{"username": {c.username}, "password": {c.password}, "action": {"get_series_info"}, "series_id": {seriesID}}
	return c.getURL(ctx, c.baseURL+"/player_api.php?"+values.Encode(), target)
}

func (c *Client) getItemInfo(ctx context.Context, action, idName, id string, target any) error {
	values := url.Values{"username": {c.username}, "password": {c.password}, "action": {action}, idName: {id}}
	return c.getURL(ctx, c.baseURL+"/player_api.php?"+values.Encode(), target)
}

func (c *Client) getURL(ctx context.Context, targetURL string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return errors.New("xtream: build API request")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("xtream: API request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xtream: API returned HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxAPIResponse))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("xtream: decode API response: %w", err)
	}
	return nil
}

type flexibleString string

func (v *flexibleString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*v = flexibleString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return errors.New("expected string or number")
	}
	*v = flexibleString(number.String())
	return nil
}

func (v flexibleString) String() string { return strings.TrimSpace(string(v)) }
func (v flexibleString) Int() int {
	n, _ := strconv.Atoi(v.String())
	return n
}

type categoryJSON struct {
	ID   flexibleString `json:"category_id"`
	Name string         `json:"category_name"`
}

type movieJSON struct {
	StreamID     flexibleString `json:"stream_id"`
	Name         string         `json:"name"`
	Year         flexibleString `json:"year"`
	CategoryID   flexibleString `json:"category_id"`
	Extension    string         `json:"container_extension"`
	Added        flexibleString `json:"added"`
	LastModified flexibleString `json:"last_modified"`
}

type seriesJSON struct {
	SeriesID     flexibleString `json:"series_id"`
	Name         string         `json:"name"`
	Year         flexibleString `json:"year"`
	CategoryID   flexibleString `json:"category_id"`
	LastModified flexibleString `json:"last_modified"`
}

type mediaInfoJSON struct {
	Name           string         `json:"name"`
	Title          string         `json:"title"`
	OriginalName   string         `json:"o_name"`
	OriginalTitle  string         `json:"original_title"`
	Year           flexibleString `json:"year"`
	Plot           string         `json:"plot"`
	Description    string         `json:"description"`
	ReleaseDate    string         `json:"releasedate"`
	ReleaseDateISO string         `json:"release_date"`
	ReleaseDateAlt string         `json:"releaseDate"`
	AirDate        string         `json:"air_date"`
	Duration       string         `json:"duration"`
	DurationSecs   flexibleString `json:"duration_secs"`
	EpisodeRunTime flexibleString `json:"episode_run_time"`
	Rating         flexibleString `json:"rating"`
	Bitrate        flexibleString `json:"bitrate"`
	Country        string         `json:"country"`
	Genre          string         `json:"genre"`
	Director       string         `json:"director"`
	Cast           string         `json:"cast"`
	Actors         string         `json:"actors"`
	TMDBID         flexibleString `json:"tmdb_id"`
	Cover          string         `json:"cover"`
	CoverBig       string         `json:"cover_big"`
	MovieImage     string         `json:"movie_image"`
	BackdropPath   []string       `json:"backdrop_path"`
	YouTubeTrailer string         `json:"youtube_trailer"`
}

type movieDataJSON struct {
	StreamID  flexibleString `json:"stream_id"`
	Name      string         `json:"name"`
	Extension string         `json:"container_extension"`
}

type movieInfoJSON struct {
	Info      mediaInfoJSON `json:"info"`
	MovieData movieDataJSON `json:"movie_data"`
}

type episodeJSON struct {
	ID            flexibleString  `json:"id"`
	Title         string          `json:"title"`
	Info          episodeInfoJSON `json:"info"`
	Season        flexibleString  `json:"season"`
	EpisodeNumber flexibleString  `json:"episode_num"`
	Extension     string          `json:"container_extension"`
}

// episodeInfoJSON accepts the provider's narrowly observed empty-string
// sentinel for missing episode metadata. Non-empty strings and other shapes
// remain unsupported instead of being silently coerced.
type episodeInfoJSON mediaInfoJSON

func (v *episodeInfoJSON) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value) != "" {
			return errors.New("episode info string must be empty")
		}
		*v = episodeInfoJSON{}
		return nil
	}
	type plain episodeInfoJSON
	return json.Unmarshal(data, (*plain)(v))
}

type episodesJSONForm int

const (
	episodesObject episodesJSONForm = iota
	episodesFlatArray
	episodesNestedArray
)

type episodeGroupJSON struct {
	SeasonKey string
	Items     []episodeJSON
}

type episodesJSON struct {
	Form   episodesJSONForm
	Groups []episodeGroupJSON
}

func (e *episodesJSON) UnmarshalJSON(data []byte) error {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 || string(data) == "null" {
		return newUnsupportedSeriesResponse("episodes must be an object or array")
	}
	switch data[0] {
	case '{':
		var object map[string][]episodeJSON
		if err := json.Unmarshal(data, &object); err != nil {
			return newUnsupportedSeriesResponse("invalid episodes object: %v", err)
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		e.Form = episodesObject
		e.Groups = make([]episodeGroupJSON, 0, len(keys))
		for _, key := range keys {
			e.Groups = append(e.Groups, episodeGroupJSON{SeasonKey: key, Items: object[key]})
		}
		return nil
	case '[':
		var entries []json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			return newUnsupportedSeriesResponse("invalid episodes array: %v", err)
		}
		if len(entries) == 0 {
			e.Form = episodesFlatArray
			e.Groups = nil
			return nil
		}
		first := bytes.TrimSpace(entries[0])
		if len(first) == 0 {
			return newUnsupportedSeriesResponse("episodes array entry 0 is empty")
		}
		switch first[0] {
		case '{':
			var items []episodeJSON
			if err := json.Unmarshal(data, &items); err != nil {
				return newUnsupportedSeriesResponse("invalid flat episodes array: %v", err)
			}
			e.Form = episodesFlatArray
			e.Groups = []episodeGroupJSON{{Items: items}}
			return nil
		case '[':
			e.Form = episodesNestedArray
			e.Groups = make([]episodeGroupJSON, 0, len(entries))
			for index, entry := range entries {
				var items []episodeJSON
				if err := json.Unmarshal(entry, &items); err != nil {
					return newUnsupportedSeriesResponse("invalid nested episodes array group %d: %v", index, err)
				}
				e.Groups = append(e.Groups, episodeGroupJSON{Items: items})
			}
			return nil
		default:
			return newUnsupportedSeriesResponse("unsupported episodes array entry 0")
		}
	default:
		return newUnsupportedSeriesResponse("episodes must be an object or array")
	}
}

type unsupportedSeriesResponseError struct {
	reason string
}

func newUnsupportedSeriesResponse(format string, args ...any) error {
	return &unsupportedSeriesResponseError{reason: fmt.Sprintf(format, args...)}
}

func (e *unsupportedSeriesResponseError) Error() string {
	return "xtream: unsupported series response: " + e.reason
}

func (*unsupportedSeriesResponseError) CatalogItemUnsupported() bool { return true }

type normalizedEpisodeJSON struct {
	Item   episodeJSON
	Season int
	Number int
}

func normalizeSeriesEpisodes(raw seriesInfoJSON) ([]normalizedEpisodeJSON, error) {
	result := make([]normalizedEpisodeJSON, 0)
	for groupIndex, group := range raw.Episodes.Groups {
		fallbackSeason := 0
		switch raw.Episodes.Form {
		case episodesObject:
			fallbackSeason, _ = strconv.Atoi(group.SeasonKey)
		case episodesNestedArray:
			if groupIndex < len(raw.Seasons) {
				fallbackSeason = raw.Seasons[groupIndex].Number.Int()
			}
		}
		for itemIndex, item := range group.Items {
			season := item.Season.Int()
			if season <= 0 {
				season = fallbackSeason
			}
			number := item.EpisodeNumber.Int()
			if item.ID.String() == "" || season <= 0 || number <= 0 {
				if raw.Episodes.Form == episodesObject {
					continue
				}
				return nil, newUnsupportedSeriesResponse(
					"episodes array group %d entry %d has invalid id/season/episode coordinates",
					groupIndex, itemIndex,
				)
			}
			result = append(result, normalizedEpisodeJSON{Item: item, Season: season, Number: number})
		}
	}
	return result, nil
}

type seriesInfoJSON struct {
	Info     mediaInfoJSON `json:"info"`
	Seasons  []seasonJSON  `json:"seasons"`
	Episodes episodesJSON  `json:"episodes"`
}

type seasonJSON struct {
	Number   flexibleString `json:"season_number"`
	Cover    string         `json:"cover"`
	CoverBig string         `json:"cover_big"`
}

func normalizeCategories(raw []categoryJSON) []Category {
	result := make([]Category, 0, len(raw))
	for _, item := range raw {
		if item.ID.String() != "" {
			result = append(result, Category{ID: item.ID.String(), Name: strings.TrimSpace(item.Name)})
		}
	}
	return result
}

func sanitizeExtension(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstListValue(values []string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' })
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			result = append(result, field)
		}
	}
	return result
}

func runtimeMilliseconds(durationSeconds, episodeRuntime, duration string) int64 {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(durationSeconds), 10, 64); err == nil && seconds > 0 {
		return seconds * 1000
	}
	return int64(runtimeMinutes("", episodeRuntime, duration)) * 60 * 1000
}

func runtimeMinutes(durationSeconds, episodeRuntime, duration string) int {
	if seconds, err := strconv.Atoi(strings.TrimSpace(durationSeconds)); err == nil && seconds > 0 {
		return (seconds + 59) / 60
	}
	if minutes, err := strconv.Atoi(strings.TrimSpace(episodeRuntime)); err == nil && minutes > 0 {
		return minutes
	}
	parts := strings.Split(strings.TrimSpace(duration), ":")
	if len(parts) == 3 {
		hours, errHours := strconv.Atoi(parts[0])
		minutes, errMinutes := strconv.Atoi(parts[1])
		seconds, errSeconds := strconv.Atoi(parts[2])
		if errHours == nil && errMinutes == nil && errSeconds == nil {
			return hours*60 + minutes + (seconds+59)/60
		}
	}
	return 0
}

func trailerURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "://") {
		return value
	}
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(value)
}
