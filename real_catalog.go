package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/mediaprobe"
	"wonkvod/internal/xtream"
)

const (
	realCatalogVersion      = 1
	realMovieCategoryName   = "Black Cinema"
	realTVCategoryName      = "BET"
	defaultCatalogStatePath = "/var/lib/wonkvod/catalog.json"
	defaultCatalogRefresh   = 6 * time.Hour
	providerRequestTimeout  = 90 * time.Second
	providerDetailWorkers   = 6
	// IDs occupy disjoint decimal ranges below JavaScript's exact-integer
	// ceiling: 1=movie, 2=show, 3=season, 4=episode, 5=Media, 6=Part,
	// 7=video Stream, and 8=audio Stream. The payload is a stable SHA-256
	// projection of the canonical Xtream identity, never catalog position.
	numericNamespaceWidth = uint64(1_000_000_000_000_000)
)

var realTVCategoryTargets = []catalogCategory{
	{ID: "887", Name: realTVCategoryName},
	{ID: "926", Name: "A&E"},
}

type catalogCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type canonicalMovie struct {
	SourceID   string                 `json:"source_id"`
	Category   catalogCategory        `json:"category"`
	Title      string                 `json:"title"`
	Year       string                 `json:"year,omitempty"`
	Extension  string                 `json:"extension"`
	Revision   string                 `json:"revision,omitempty"`
	Details    canonical.MovieDetails `json:"details"`
	MediaFacts canonical.MediaFacts   `json:"media_facts,omitzero"`
}

type canonicalEpisode struct {
	SourceID   string                   `json:"source_id"`
	Season     int                      `json:"season"`
	Number     int                      `json:"number"`
	Extension  string                   `json:"extension"`
	Details    canonical.EpisodeDetails `json:"details"`
	MediaFacts canonical.MediaFacts     `json:"media_facts,omitzero"`
}

type canonicalSeason struct {
	Number   int                `json:"number"`
	Poster   string             `json:"poster,omitempty"`
	Episodes []canonicalEpisode `json:"episodes"`
}

type canonicalShow struct {
	SourceID string                  `json:"source_id"`
	Category catalogCategory         `json:"category"`
	Title    string                  `json:"title"`
	Year     string                  `json:"year,omitempty"`
	Revision string                  `json:"revision,omitempty"`
	Details  canonical.SeriesDetails `json:"details"`
	Seasons  []canonicalSeason       `json:"seasons"`
}

type quarantineEntry struct {
	Kind     string `json:"kind"`
	SourceID string `json:"source_id,omitempty"`
	Reason   string `json:"reason"`
}

type canonicalSnapshot struct {
	Version       int               `json:"version"`
	GeneratedAt   time.Time         `json:"generated_at"`
	MovieCategory catalogCategory   `json:"movie_category"`
	TVCategory    catalogCategory   `json:"tv_category"`
	TVCategories  []catalogCategory `json:"tv_categories,omitempty"`
	Movies        []canonicalMovie  `json:"movies"`
	Shows         []canonicalShow   `json:"shows"`
	Quarantine    []quarantineEntry `json:"quarantine,omitempty"`
}

type catalogRuntime struct {
	client    *xtream.Client
	statePath string

	mu           sync.RWMutex
	snapshot     canonicalSnapshot
	sources      map[string]*xtream.VODSource
	capabilities map[string]xtream.CapabilityProbe
	inspections  sync.Map
	persistMu    sync.Mutex
}

func newCatalogRuntime() (*catalogRuntime, error) {
	baseURL := strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_URL"))
	username := strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_USER"))
	password := strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_PASS"))
	if baseURL == "" || username == "" || password == "" {
		return nil, errors.New("real catalog: Xtream configuration is incomplete")
	}
	client, err := xtream.NewClient(&http.Client{Timeout: providerRequestTimeout}, baseURL, username, password)
	if err != nil {
		return nil, err
	}
	statePath := strings.TrimSpace(os.Getenv("WONKVOD_CATALOG_STATE"))
	if statePath == "" {
		statePath = defaultCatalogStatePath
	}
	runtime := &catalogRuntime{client: client, statePath: statePath, sources: make(map[string]*xtream.VODSource), capabilities: make(map[string]xtream.CapabilityProbe)}
	previous, loadErr := loadCanonicalSnapshot(statePath)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	current, refreshErr := runtime.fetch(ctx)
	if refreshErr != nil {
		if loadErr != nil || previous.Version != realCatalogVersion {
			return nil, fmt.Errorf("real catalog: initial refresh failed and no usable snapshot exists: %w", refreshErr)
		}
		log.Printf("real catalog refresh failed; retaining snapshot generated=%s", previous.GeneratedAt.Format(time.RFC3339))
		runtime.snapshot = previous
	} else {
		mergeMediaFacts(&current, previous)
		runtime.snapshot = current
		if err := saveCanonicalSnapshot(statePath, current); err != nil {
			return nil, fmt.Errorf("real catalog: persist snapshot: %w", err)
		}
	}
	runtime.logSummary(runtime.snapshot)
	go runtime.refreshLoop()
	return runtime, nil
}

func (c *catalogRuntime) refreshLoop() {
	ticker := time.NewTicker(defaultCatalogRefresh)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		next, err := c.fetch(ctx)
		cancel()
		if err != nil {
			log.Printf("real catalog background refresh failed; previous snapshot retained")
			continue
		}
		c.persistMu.Lock()
		c.mu.Lock()
		mergeMediaFacts(&next, c.snapshot)
		c.mu.Unlock()
		if err := saveCanonicalSnapshot(c.statePath, next); err != nil {
			c.persistMu.Unlock()
			log.Printf("real catalog snapshot persist failed; previous snapshot retained")
			continue
		}
		c.mu.Lock()
		c.snapshot = next
		c.sources = make(map[string]*xtream.VODSource)
		c.capabilities = make(map[string]xtream.CapabilityProbe)
		c.mu.Unlock()
		c.persistMu.Unlock()
		c.logSummary(next)
	}
}

func (c *catalogRuntime) snapshotCopy() canonicalSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, _ := json.Marshal(c.snapshot)
	var result canonicalSnapshot
	_ = json.Unmarshal(data, &result)
	return result
}

func (c *catalogRuntime) fetch(ctx context.Context) (canonicalSnapshot, error) {
	movieCategories, err := c.client.VODCategories(ctx)
	if err != nil {
		return canonicalSnapshot{}, err
	}
	tvCategories, err := c.client.SeriesCategories(ctx)
	if err != nil {
		return canonicalSnapshot{}, err
	}
	movieCategory, err := selectCategory(movieCategories, realMovieCategoryName)
	if err != nil {
		return canonicalSnapshot{}, err
	}
	movieItems, err := c.client.Movies(ctx, movieCategory.ID)
	if err != nil {
		return canonicalSnapshot{}, err
	}
	result := canonicalSnapshot{
		Version: realCatalogVersion, GeneratedAt: time.Now().UTC(),
		MovieCategory: catalogCategory{ID: movieCategory.ID, Name: movieCategory.Name, Count: len(movieItems)},
	}
	type categorizedSeries struct {
		item     xtream.Series
		category catalogCategory
	}
	var showItems []categorizedSeries
	seenSeries := make(map[string]string)
	for _, target := range realTVCategoryTargets {
		category, categoryErr := selectCategoryByIDAndName(tvCategories, target.ID, target.Name)
		if categoryErr != nil {
			return canonicalSnapshot{}, categoryErr
		}
		items, seriesErr := c.client.Series(ctx, category.ID)
		if seriesErr != nil {
			return canonicalSnapshot{}, seriesErr
		}
		selected := catalogCategory{ID: category.ID, Name: category.Name, Count: len(items)}
		result.TVCategories = append(result.TVCategories, selected)
		for _, item := range items {
			if previous, exists := seenSeries[item.ID]; exists {
				return canonicalSnapshot{}, fmt.Errorf("real catalog: series %s appears in both TV categories %s and %s", item.ID, previous, selected.ID)
			}
			seenSeries[item.ID] = selected.ID
			showItems = append(showItems, categorizedSeries{item: item, category: selected})
		}
	}
	// Retain the original primary field so a newly written snapshot remains
	// readable by the previous single-category binary during rollback.
	result.TVCategory = result.TVCategories[0]

	type movieResult struct {
		movie canonicalMovie
		q     *quarantineEntry
	}
	movieJobs := make(chan xtream.Movie)
	movieResults := make(chan movieResult)
	var workers sync.WaitGroup
	workerCount := providerDetailWorkers
	if len(movieItems) < workerCount {
		workerCount = len(movieItems)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range movieJobs {
				details, detailErr := c.client.MovieDetails(ctx, item.ID)
				if detailErr != nil {
					movieResults <- movieResult{q: &quarantineEntry{Kind: "movie", SourceID: item.ID, Reason: "metadata request failed"}}
					continue
				}
				extension := firstNonEmptyString(details.Extension, item.Extension, "mkv")
				title := firstNonEmptyString(details.Title, item.Name)
				if title == "" {
					movieResults <- movieResult{q: &quarantineEntry{Kind: "movie", SourceID: item.ID, Reason: "title is empty"}}
					continue
				}
				movieResults <- movieResult{movie: canonicalMovie{
					SourceID: item.ID, Category: result.MovieCategory, Title: title,
					Year: firstNonEmptyString(details.Year, item.Year), Extension: extension,
					Revision: item.Revision, Details: details,
				}}
			}
		}()
	}
	go func() {
		defer close(movieJobs)
		for _, item := range movieItems {
			movieJobs <- item
		}
	}()
	go func() { workers.Wait(); close(movieResults) }()
	for item := range movieResults {
		if item.q != nil {
			result.Quarantine = append(result.Quarantine, *item.q)
		} else {
			result.Movies = append(result.Movies, item.movie)
		}
	}

	type showResult struct {
		show       canonicalShow
		q          *quarantineEntry
		quarantine []quarantineEntry
	}
	showJobs := make(chan categorizedSeries)
	showResults := make(chan showResult)
	workers = sync.WaitGroup{}
	workerCount = providerDetailWorkers
	if len(showItems) < workerCount {
		workerCount = len(showItems)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range showJobs {
				item := job.item
				details, detailErr := c.client.SeriesDetails(ctx, item.ID)
				if detailErr != nil {
					showResults <- showResult{q: &quarantineEntry{Kind: "show", SourceID: item.ID, Reason: "series traversal failed"}}
					continue
				}
				show := canonicalShow{
					SourceID: item.ID, Category: job.category,
					Title: firstNonEmptyString(details.Title, item.Name), Year: firstNonEmptyString(details.Year, item.Year),
					Revision: item.Revision, Details: details,
				}
				seasonPosters := make(map[int]string)
				var episodeQuarantine []quarantineEntry
				for _, season := range details.Seasons {
					seasonPosters[season.Number] = season.Poster
				}
				bySeason := make(map[int][]canonicalEpisode)
				for _, episode := range details.Episodes {
					if episode.ID == "" || episode.Season <= 0 || episode.Number <= 0 {
						episodeQuarantine = append(episodeQuarantine, quarantineEntry{Kind: "episode", SourceID: episode.ID, Reason: "invalid season or episode identity"})
						continue
					}
					extension := firstNonEmptyString(episode.Extension, "mkv")
					bySeason[episode.Season] = append(bySeason[episode.Season], canonicalEpisode{SourceID: episode.ID, Season: episode.Season, Number: episode.Number, Extension: extension, Details: episode})
				}
				for number, episodes := range bySeason {
					sort.Slice(episodes, func(i, j int) bool { return episodes[i].Number < episodes[j].Number })
					show.Seasons = append(show.Seasons, canonicalSeason{Number: number, Poster: seasonPosters[number], Episodes: episodes})
				}
				sort.Slice(show.Seasons, func(i, j int) bool { return show.Seasons[i].Number < show.Seasons[j].Number })
				if show.Title == "" || len(show.Seasons) == 0 {
					showResults <- showResult{q: &quarantineEntry{Kind: "show", SourceID: item.ID, Reason: "series has no usable episodes"}}
					continue
				}
				showResults <- showResult{show: show, quarantine: episodeQuarantine}
			}
		}()
	}
	go func() {
		defer close(showJobs)
		for _, item := range showItems {
			showJobs <- item
		}
	}()
	go func() { workers.Wait(); close(showResults) }()
	for item := range showResults {
		if item.q != nil {
			result.Quarantine = append(result.Quarantine, *item.q)
		} else {
			result.Shows = append(result.Shows, item.show)
			result.Quarantine = append(result.Quarantine, item.quarantine...)
		}
	}

	sort.Slice(result.Movies, func(i, j int) bool {
		if strings.EqualFold(result.Movies[i].Title, result.Movies[j].Title) {
			return result.Movies[i].SourceID < result.Movies[j].SourceID
		}
		return strings.ToLower(result.Movies[i].Title) < strings.ToLower(result.Movies[j].Title)
	})
	sort.Slice(result.Shows, func(i, j int) bool {
		if strings.EqualFold(result.Shows[i].Title, result.Shows[j].Title) {
			return result.Shows[i].SourceID < result.Shows[j].SourceID
		}
		return strings.ToLower(result.Shows[i].Title) < strings.ToLower(result.Shows[j].Title)
	})
	if err := validateStableIDs(result); err != nil {
		return canonicalSnapshot{}, err
	}
	return result, nil
}

func selectCategory(categories []xtream.Category, name string) (xtream.Category, error) {
	var matches []xtream.Category
	for _, category := range categories {
		if strings.EqualFold(strings.TrimSpace(category.Name), name) {
			matches = append(matches, category)
		}
	}
	if len(matches) != 1 {
		return xtream.Category{}, fmt.Errorf("real catalog: category %q matched %d provider categories", name, len(matches))
	}
	return matches[0], nil
}

func selectCategoryByIDAndName(categories []xtream.Category, id, name string) (xtream.Category, error) {
	var matches []xtream.Category
	for _, category := range categories {
		if category.ID == id && strings.EqualFold(strings.TrimSpace(category.Name), name) {
			matches = append(matches, category)
		}
	}
	if len(matches) != 1 {
		return xtream.Category{}, fmt.Errorf("real catalog: category %s/%q matched %d provider categories", id, name, len(matches))
	}
	return matches[0], nil
}

func snapshotTVCategories(snapshot canonicalSnapshot) []catalogCategory {
	if len(snapshot.TVCategories) > 0 {
		return snapshot.TVCategories
	}
	if snapshot.TVCategory.ID != "" {
		return []catalogCategory{snapshot.TVCategory}
	}
	return nil
}

func (c *catalogRuntime) logSummary(snapshot canonicalSnapshot) {
	seasons, episodes := 0, 0
	for _, show := range snapshot.Shows {
		seasons += len(show.Seasons)
		for _, season := range show.Seasons {
			episodes += len(season.Episodes)
		}
	}
	movieRejected, showRejected, episodeRejected := 0, 0, 0
	for _, item := range snapshot.Quarantine {
		switch item.Kind {
		case "movie":
			movieRejected++
		case "show":
			showRejected++
		case "episode":
			episodeRejected++
		}
	}
	log.Printf("real catalog selected category=%q provider_id=%s top_level=%d accepted=%d quarantined=%d", snapshot.MovieCategory.Name, snapshot.MovieCategory.ID, snapshot.MovieCategory.Count, len(snapshot.Movies), movieRejected)
	for _, category := range snapshotTVCategories(snapshot) {
		accepted, categorySeasons, categoryEpisodes := 0, 0, 0
		for _, show := range snapshot.Shows {
			if show.Category.ID != category.ID {
				continue
			}
			accepted++
			categorySeasons += len(show.Seasons)
			for _, season := range show.Seasons {
				categoryEpisodes += len(season.Episodes)
			}
		}
		log.Printf("real catalog selected category=%q provider_id=%s top_level=%d accepted=%d seasons=%d episodes=%d", category.Name, category.ID, category.Count, accepted, categorySeasons, categoryEpisodes)
	}
	log.Printf("real catalog TV totals accepted=%d quarantined=%d seasons=%d episodes=%d malformed_episodes=%d", len(snapshot.Shows), showRejected, seasons, episodes, episodeRejected)
}

func loadCanonicalSnapshot(path string) (canonicalSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return canonicalSnapshot{}, err
	}
	var snapshot canonicalSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return canonicalSnapshot{}, err
	}
	if snapshot.Version != realCatalogVersion {
		return canonicalSnapshot{}, errors.New("unsupported catalog snapshot version")
	}
	return snapshot, validateStableIDs(snapshot)
}

func saveCanonicalSnapshot(path string, snapshot canonicalSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".catalog-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func stableNumericID(namespace byte, identity string) int64 {
	sum := sha256.Sum256([]byte(string(namespace) + "\x00" + identity))
	value := binary.BigEndian.Uint64(sum[:8]) % numericNamespaceWidth
	return int64(uint64(namespace-'0')*numericNamespaceWidth + value)
}

func validateStableIDs(snapshot canonicalSnapshot) error {
	seen := make(map[int64]string)
	claim := func(id int64, identity string) error {
		if previous, exists := seen[id]; exists && previous != identity {
			return fmt.Errorf("stable ID collision between %s and %s", previous, identity)
		}
		seen[id] = identity
		return nil
	}
	for _, movie := range snapshot.Movies {
		identity := "xtream:movie:" + movie.SourceID
		for _, spec := range []struct {
			namespace byte
			suffix    string
		}{{'1', "object"}, {'5', "media"}, {'6', "part"}, {'7', "video"}, {'8', "audio"}} {
			if err := claim(stableNumericID(spec.namespace, identity+":"+spec.suffix), identity+":"+spec.suffix); err != nil {
				return err
			}
		}
		for _, subtitle := range movie.MediaFacts.Subtitles {
			value := "xtream:movie:" + movie.SourceID + ":subtitle:" + strconv.Itoa(subtitle.Index)
			if err := claim(stableNumericID('9', value), value); err != nil {
				return err
			}
		}
	}
	for _, show := range snapshot.Shows {
		showIdentity := "xtream:show:" + show.SourceID
		if err := claim(stableNumericID('2', showIdentity), showIdentity); err != nil {
			return err
		}
		for _, season := range show.Seasons {
			seasonIdentity := showIdentity + ":season:" + strconv.Itoa(season.Number)
			if err := claim(stableNumericID('3', seasonIdentity), seasonIdentity); err != nil {
				return err
			}
			for _, episode := range season.Episodes {
				identity := "xtream:episode:" + episode.SourceID
				for _, spec := range []struct {
					namespace byte
					suffix    string
				}{{'4', "object"}, {'5', "media"}, {'6', "part"}, {'7', "video"}, {'8', "audio"}} {
					if err := claim(stableNumericID(spec.namespace, identity+":"+spec.suffix), identity+":"+spec.suffix); err != nil {
						return err
					}
				}
				for _, subtitle := range episode.MediaFacts.Subtitles {
					value := "xtream:episode:" + episode.SourceID + ":subtitle:" + strconv.Itoa(subtitle.Index)
					if err := claim(stableNumericID('9', value), value); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

type catalogPlayable struct {
	Kind       string
	RatingKey  string
	SourceID   string
	Extension  string
	Title      string
	Duration   int
	Bitrate    int
	MediaFacts canonical.MediaFacts
	Movie      *canonicalMovie
	Show       *canonicalShow
	Season     *canonicalSeason
	Episode    *canonicalEpisode
}

func movieObjectID(movie canonicalMovie) string {
	return strconv.FormatInt(stableNumericID('1', "xtream:movie:"+movie.SourceID+":object"), 10)
}

func showObjectID(show canonicalShow) string {
	return strconv.FormatInt(stableNumericID('2', "xtream:show:"+show.SourceID), 10)
}

func seasonObjectID(show canonicalShow, season canonicalSeason) string {
	return strconv.FormatInt(stableNumericID('3', "xtream:show:"+show.SourceID+":season:"+strconv.Itoa(season.Number)), 10)
}

func episodeObjectID(episode canonicalEpisode) string {
	return strconv.FormatInt(stableNumericID('4', "xtream:episode:"+episode.SourceID+":object"), 10)
}

func mediaObjectID(kind, sourceID string) int64 {
	return stableNumericID('5', "xtream:"+kind+":"+sourceID+":media")
}

func partObjectID(kind, sourceID string) int64 {
	return stableNumericID('6', "xtream:"+kind+":"+sourceID+":part")
}

func videoStreamObjectID(kind, sourceID string) int64 {
	return stableNumericID('7', "xtream:"+kind+":"+sourceID+":video")
}

func audioStreamObjectID(kind, sourceID string) int64 {
	return stableNumericID('8', "xtream:"+kind+":"+sourceID+":audio")
}

func subtitleStreamObjectID(kind, sourceID string, index int) int64 {
	return stableNumericID('9', "xtream:"+kind+":"+sourceID+":subtitle:"+strconv.Itoa(index))
}

func (c *catalogRuntime) playable(ratingKey string) (catalogPlayable, bool) {
	snapshot := c.snapshotCopy()
	for index := range snapshot.Movies {
		item := &snapshot.Movies[index]
		if movieObjectID(*item) == ratingKey {
			return catalogPlayable{Kind: "movie", RatingKey: ratingKey, SourceID: item.SourceID, Extension: item.Extension, Title: item.Title, Duration: int(item.Details.DurationMillis), Bitrate: item.Details.Bitrate, MediaFacts: item.MediaFacts, Movie: item}, true
		}
	}
	for showIndex := range snapshot.Shows {
		show := &snapshot.Shows[showIndex]
		for seasonIndex := range show.Seasons {
			season := &show.Seasons[seasonIndex]
			for episodeIndex := range season.Episodes {
				episode := &season.Episodes[episodeIndex]
				if episodeObjectID(*episode) == ratingKey {
					return catalogPlayable{Kind: "episode", RatingKey: ratingKey, SourceID: episode.SourceID, Extension: episode.Extension, Title: episode.Details.Title, Duration: int(episode.Details.DurationMillis), Bitrate: episode.Details.Bitrate, MediaFacts: episode.MediaFacts, Show: show, Season: season, Episode: episode}, true
				}
			}
		}
	}
	return catalogPlayable{}, false
}

func (c *catalogRuntime) metadataKind(ratingKey string) (string, bool) {
	if ratingKey == "" {
		return "", false
	}
	snapshot := c.snapshotCopy()
	for _, movie := range snapshot.Movies {
		if movieObjectID(movie) == ratingKey {
			return "movie", true
		}
	}
	for _, show := range snapshot.Shows {
		if showObjectID(show) == ratingKey {
			return "show", true
		}
		for _, season := range show.Seasons {
			if seasonObjectID(show, season) == ratingKey {
				return "season", true
			}
			for _, episode := range season.Episodes {
				if episodeObjectID(episode) == ratingKey {
					return "episode", true
				}
			}
		}
	}
	return "", false
}

func (c *catalogRuntime) firstPlayableEpisode(showRatingKey string) (catalogPlayable, bool) {
	snapshot := c.snapshotCopy()
	for showIndex := range snapshot.Shows {
		show := &snapshot.Shows[showIndex]
		if showObjectID(*show) != showRatingKey {
			continue
		}
		for seasonIndex := range show.Seasons {
			season := &show.Seasons[seasonIndex]
			for episodeIndex := range season.Episodes {
				episode := &season.Episodes[episodeIndex]
				if strings.TrimSpace(episode.SourceID) == "" || strings.TrimSpace(episode.Extension) == "" {
					continue
				}
				return catalogPlayable{Kind: "episode", RatingKey: episodeObjectID(*episode), SourceID: episode.SourceID, Extension: episode.Extension, Title: episode.Details.Title, Duration: int(episode.Details.DurationMillis), Bitrate: episode.Details.Bitrate, MediaFacts: episode.MediaFacts, Show: show, Season: season, Episode: episode}, true
			}
		}
		return catalogPlayable{}, false
	}
	return catalogPlayable{}, false
}

func (c *catalogRuntime) source(item catalogPlayable) (*xtream.VODSource, error) {
	key := item.Kind + ":" + item.SourceID
	c.mu.RLock()
	existing := c.sources[key]
	c.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}
	var source *xtream.VODSource
	var err error
	if item.Kind == "movie" {
		source, err = c.client.MovieSource(item.SourceID, item.Extension)
	} else {
		source, err = c.client.EpisodeSource(item.SourceID, item.Extension)
	}
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if previous := c.sources[key]; previous != nil {
		source = previous
	} else {
		c.sources[key] = source
	}
	c.mu.Unlock()
	return source, nil
}

func (c *catalogRuntime) capability(ctx context.Context, item catalogPlayable) (xtream.CapabilityProbe, error) {
	key := item.Kind + ":" + item.SourceID
	c.mu.RLock()
	capability, ok := c.capabilities[key]
	c.mu.RUnlock()
	if ok {
		return capability, nil
	}
	source, err := c.source(item)
	if err != nil {
		return xtream.CapabilityProbe{}, err
	}
	capability, err = source.ProbeCapability(ctx)
	if err != nil {
		return capability, err
	}
	c.mu.Lock()
	c.capabilities[key] = capability
	c.mu.Unlock()
	return capability, nil
}

func mergeMediaFacts(next *canonicalSnapshot, previous canonicalSnapshot) {
	movies := make(map[string]canonical.MediaFacts, len(previous.Movies))
	for _, item := range previous.Movies {
		movies[item.SourceID] = item.MediaFacts
	}
	for index := range next.Movies {
		if facts := movies[next.Movies[index].SourceID]; facts.State != "" {
			next.Movies[index].MediaFacts = facts
		}
	}
	episodes := make(map[string]canonical.MediaFacts)
	for _, show := range previous.Shows {
		for _, season := range show.Seasons {
			for _, episode := range season.Episodes {
				episodes[episode.SourceID] = episode.MediaFacts
			}
		}
	}
	for showIndex := range next.Shows {
		for seasonIndex := range next.Shows[showIndex].Seasons {
			for episodeIndex := range next.Shows[showIndex].Seasons[seasonIndex].Episodes {
				episode := &next.Shows[showIndex].Seasons[seasonIndex].Episodes[episodeIndex]
				if facts := episodes[episode.SourceID]; facts.State != "" {
					episode.MediaFacts = facts
				}
			}
		}
	}
}

func (c *catalogRuntime) inspectMedia(ctx context.Context, item catalogPlayable, knownSize int64) (canonical.MediaFacts, error) {
	if item.MediaFacts.State == "inspected" {
		return item.MediaFacts, nil
	}
	key := item.Kind + ":" + item.SourceID
	lockValue, _ := c.inspections.LoadOrStore(key, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if refreshed, ok := c.playable(item.RatingKey); ok && refreshed.MediaFacts.State == "inspected" {
		return refreshed.MediaFacts, nil
	}
	source, err := c.source(item)
	if err != nil {
		return canonical.MediaFacts{State: "retryable"}, err
	}
	resp, err := source.Open(ctx, http.MethodGet, fmt.Sprintf("bytes=0-%d", mediaprobe.MaxProbeBytes-1))
	if err != nil {
		facts := canonical.MediaFacts{State: "retryable"}
		c.storeMediaFacts(item, facts)
		return facts, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		state := "retryable"
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			state = "unavailable"
		}
		facts := canonical.MediaFacts{State: state}
		c.storeMediaFacts(item, facts)
		return facts, fmt.Errorf("media inspection returned HTTP %d", resp.StatusCode)
	}
	if knownSize <= 0 {
		knownSize = mediaResponseSize(resp)
	}
	facts, probeErr := mediaprobe.Probe(ctx, resp.Body, knownSize)
	c.storeMediaFacts(item, facts)
	return facts, probeErr
}

func mediaResponseSize(resp *http.Response) int64 {
	if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		return resp.ContentLength
	}
	value := resp.Header.Get("Content-Range")
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		size, _ := strconv.ParseInt(value[slash+1:], 10, 64)
		return size
	}
	return 0
}

func (c *catalogRuntime) storeMediaFacts(item catalogPlayable, facts canonical.MediaFacts) {
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	c.mu.Lock()
	for index := range c.snapshot.Movies {
		if c.snapshot.Movies[index].SourceID == item.SourceID && item.Kind == "movie" {
			c.snapshot.Movies[index].MediaFacts = facts
		}
	}
	for showIndex := range c.snapshot.Shows {
		for seasonIndex := range c.snapshot.Shows[showIndex].Seasons {
			for episodeIndex := range c.snapshot.Shows[showIndex].Seasons[seasonIndex].Episodes {
				episode := &c.snapshot.Shows[showIndex].Seasons[seasonIndex].Episodes[episodeIndex]
				if episode.SourceID == item.SourceID && item.Kind == "episode" {
					episode.MediaFacts = facts
				}
			}
		}
	}
	data, _ := json.Marshal(c.snapshot)
	statePath := c.statePath
	c.mu.Unlock()
	if statePath != "" {
		var snapshot canonicalSnapshot
		if json.Unmarshal(data, &snapshot) == nil {
			if err := saveCanonicalSnapshot(statePath, snapshot); err != nil {
				log.Printf("media facts persist failed source_id=%q", item.SourceID)
			}
		}
	}
}

func parsePositiveInt(value string) int {
	parsed, _ := strconv.Atoi(strings.TrimSpace(value))
	if parsed < 0 {
		return 0
	}
	return parsed
}
