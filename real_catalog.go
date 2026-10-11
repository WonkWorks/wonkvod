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
	// The deadline is a safety ceiling, not an expected duration. At the
	// conservative default concurrency, large catalogs can take several hours.
	catalogRefreshTimeout  = 6 * time.Hour
	providerRequestTimeout = 90 * time.Second
	// IDs occupy disjoint decimal ranges below JavaScript's exact-integer
	// ceiling: 1=movie, 2=show, 3=season, 4=episode, 5=Media, 6=Part,
	// 7=video Stream, and 8=audio Stream. The payload is a stable SHA-256
	// projection of the canonical Xtream identity, never catalog position.
	numericNamespaceWidth = uint64(1_000_000_000_000_000)
)

var realTVCategoryTargets = []catalogCategory{
	{ID: "887", Name: realTVCategoryName},
	{ID: "926", Name: "A&E"},
	{ID: "779", Name: "REALITY"},
}

type catalogCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type canonicalMovie struct {
	SourceID   string               `json:"source_id"`
	Category   catalogCategory      `json:"category"`
	Categories []canonical.Category `json:"categories,omitempty"`
	Title      string               `json:"title"`
	Year       string               `json:"year,omitempty"`
	Extension  string               `json:"extension"`
	Revision   string               `json:"revision,omitempty"`
	// ListFingerprint is adapter provenance derived only from inexpensive
	// Xtream list fields. It deliberately excludes category membership.
	ListFingerprint string                 `json:"list_fingerprint,omitempty"`
	Details         canonical.MovieDetails `json:"details"`
	MediaFacts      canonical.MediaFacts   `json:"media_facts,omitzero"`
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
	SourceID        string                  `json:"source_id"`
	Category        catalogCategory         `json:"category"`
	Categories      []canonical.Category    `json:"categories,omitempty"`
	Title           string                  `json:"title"`
	Year            string                  `json:"year,omitempty"`
	Revision        string                  `json:"revision,omitempty"`
	ListFingerprint string                  `json:"list_fingerprint,omitempty"`
	Details         canonical.SeriesDetails `json:"details"`
	Seasons         []canonicalSeason       `json:"seasons"`
}

type quarantineEntry struct {
	Kind            string               `json:"kind"`
	SourceID        string               `json:"source_id,omitempty"`
	ParentSourceID  string               `json:"parent_source_id,omitempty"`
	Title           string               `json:"title,omitempty"`
	Categories      []canonical.Category `json:"categories,omitempty"`
	Reason          string               `json:"reason"`
	ReasonClass     string               `json:"reason_class,omitempty"`
	HTTPStatus      int                  `json:"http_status,omitempty"`
	TransportClass  string               `json:"transport_error_class,omitempty"`
	LastFailure     *time.Time           `json:"last_failure_at,omitempty"`
	RetryCount      int                  `json:"retry_count,omitempty"`
	LastRetryResult string               `json:"last_retry_result,omitempty"`
}

type canonicalSnapshot struct {
	Version         int               `json:"version"`
	GeneratedAt     time.Time         `json:"generated_at"`
	MovieCategory   catalogCategory   `json:"movie_category"`
	MovieCategories []catalogCategory `json:"movie_categories,omitempty"`
	TVCategory      catalogCategory   `json:"tv_category"`
	TVCategories    []catalogCategory `json:"tv_categories,omitempty"`
	Movies          []canonicalMovie  `json:"movies"`
	Shows           []canonicalShow   `json:"shows"`
	Quarantine      []quarantineEntry `json:"quarantine,omitempty"`
}

type catalogRuntime struct {
	client    *xtream.Client
	statePath string
	config    *configStore
	startedAt time.Time

	mu           sync.RWMutex
	snapshot     canonicalSnapshot
	sources      map[string]*xtream.VODSource
	capabilities map[string]xtream.CapabilityProbe
	inspections  sync.Map
	persistMu    sync.Mutex
	refreshMu    sync.Mutex
	refreshing   bool
	status       catalogRefreshStatus
	scheduleWake chan struct{}
	fetcher      func(context.Context, appConfig) (canonicalSnapshot, *xtream.Client, error)
}

type catalogRefreshMetrics struct {
	Mode                  string `json:"mode,omitempty"`
	EffectiveConcurrency  int    `json:"effective_concurrency,omitempty"`
	MovieCandidates       int    `json:"movie_candidates"`
	ShowCandidates        int    `json:"show_candidates"`
	ReusedUnchanged       int    `json:"reused_unchanged"`
	New                   int    `json:"new"`
	Changed               int    `json:"changed"`
	MembershipOnly        int    `json:"membership_only"`
	QuarantineRetries     int    `json:"quarantine_retries"`
	QuarantineRecovered   int    `json:"quarantine_recovered"`
	Removed               int    `json:"removed"`
	DetailRequests        int    `json:"detail_requests"`
	DetailCompleted       int    `json:"detail_requests_completed"`
	DetailRequestsSkipped int    `json:"detail_requests_skipped"`
	Promoted              bool   `json:"promoted"`
}

type catalogRefreshProgress struct {
	Phase       string `json:"phase,omitempty"`
	Completed   int    `json:"completed"`
	Total       int    `json:"total"`
	Determinate bool   `json:"determinate"`
}

type catalogRefreshStatus struct {
	CatalogAvailable     bool                   `json:"catalog_available"`
	RefreshRunning       bool                   `json:"refresh_running"`
	LastRefreshStarted   time.Time              `json:"last_refresh_started,omitempty"`
	LastRefreshCompleted time.Time              `json:"last_refresh_completed,omitempty"`
	LastSuccessful       time.Time              `json:"last_successful_refresh,omitempty"`
	LastRefreshDuration  int64                  `json:"last_refresh_duration_ms,omitempty"`
	LastRefreshResult    string                 `json:"last_refresh_result"`
	LastRefreshError     string                 `json:"last_refresh_error,omitempty"`
	NextScheduledRefresh time.Time              `json:"next_scheduled_refresh,omitempty"`
	Metrics              catalogRefreshMetrics  `json:"metrics"`
	Progress             catalogRefreshProgress `json:"progress"`
}

func newCatalogRuntime() (*catalogRuntime, error) {
	statePath := strings.TrimSpace(os.Getenv("WONKVOD_CATALOG_STATE"))
	if statePath == "" {
		statePath = defaultCatalogStatePath
	}
	config, err := loadConfigStore(configStatePath())
	if err != nil {
		return nil, err
	}
	runtime := &catalogRuntime{
		statePath: statePath, config: config, startedAt: time.Now(),
		sources: make(map[string]*xtream.VODSource), capabilities: make(map[string]xtream.CapabilityProbe),
		scheduleWake: make(chan struct{}, 1),
	}
	if client, clientErr := runtime.clientForConfig(config.snapshot()); clientErr == nil {
		runtime.client = client
	} else {
		log.Printf("provider configuration unavailable: %s", sanitizeStatusError(clientErr))
	}
	previous, loadErr := loadCanonicalSnapshot(statePath)
	if loadErr == nil && previous.Version == realCatalogVersion {
		runtime.snapshot = previous
		runtime.status.CatalogAvailable = true
		runtime.status.LastSuccessful = previous.GeneratedAt
		runtime.status.LastRefreshResult = "loaded_snapshot"
		runtime.logSummary(previous)
	} else {
		runtime.status.LastRefreshResult = "no_catalog"
		if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
			runtime.status.LastRefreshError = "stored catalog is unavailable: " + sanitizeStatusError(loadErr)
		}
	}
	go runtime.refreshLoop()
	go runtime.triggerRefresh("startup")
	return runtime, nil
}

func (c *catalogRuntime) refreshLoop() {
	for {
		interval := c.config.refreshInterval()
		c.refreshMu.Lock()
		c.status.NextScheduledRefresh = time.Now().Add(interval)
		next := c.status.NextScheduledRefresh
		c.refreshMu.Unlock()
		timer := time.NewTimer(time.Until(next))
		select {
		case <-timer.C:
			c.triggerRefresh("scheduled")
		case <-c.scheduleWake:
			if !timer.Stop() {
				<-timer.C
			}
		}
	}
}

func (c *catalogRuntime) triggerRefresh(reason string) bool {
	return c.triggerCatalogBuild(reason, false, 0)
}

func (c *catalogRuntime) triggerRebuild(reason string) bool {
	return c.triggerCatalogBuild(reason, true, 0)
}

func (c *catalogRuntime) triggerRefreshWithConcurrency(reason string, concurrency int) bool {
	return c.triggerCatalogBuild(reason, false, concurrency)
}

func (c *catalogRuntime) triggerRebuildWithConcurrency(reason string, concurrency int) bool {
	return c.triggerCatalogBuild(reason, true, concurrency)
}

func (c *catalogRuntime) triggerCatalogBuild(reason string, fullRebuild bool, concurrency int) bool {
	config := c.config.snapshot()
	if concurrency == 0 {
		concurrency = config.Catalog.RefreshConcurrency
	}
	_, mode := catalogBuildMode(c.snapshotCopy(), fullRebuild)
	if !fullRebuild && reason == "manual" && hasCategoryExpansion(c.snapshotCopy(), config.Libraries) {
		mode = "bootstrap"
	}
	c.refreshMu.Lock()
	if c.refreshing {
		c.refreshMu.Unlock()
		return false
	}
	c.refreshing = true
	c.status.RefreshRunning = true
	c.status.LastRefreshStarted = time.Now().UTC()
	c.status.LastRefreshResult = "running"
	c.status.LastRefreshError = ""
	c.status.Metrics = catalogRefreshMetrics{Mode: mode, EffectiveConcurrency: concurrency}
	c.status.Progress = catalogRefreshProgress{Phase: "category_indexes"}
	c.refreshMu.Unlock()
	log.Printf("real catalog refresh started reason=%q mode=%q process_age_ms=%d", reason, mode, time.Since(c.startedAt).Milliseconds())
	go c.runRefresh(reason, fullRebuild, mode, concurrency)
	return true
}

func (c *catalogRuntime) runRefresh(reason string, fullRebuild bool, mode string, concurrency int) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), catalogRefreshTimeout)
	config := c.config.snapshot()
	config.Catalog.RefreshConcurrency = concurrency
	fetcher := c.fetcher
	var metrics catalogRefreshMetrics
	previous := c.snapshotCopy()
	effectiveFull, _ := catalogBuildMode(previous, fullRebuild)
	metrics.Mode = mode
	metrics.EffectiveConcurrency = concurrency
	var next canonicalSnapshot
	var client *xtream.Client
	var err error
	if fetcher == nil {
		next, client, metrics, err = c.fetchCatalogWithMode(ctx, config, previous, effectiveFull, mode)
	} else {
		next, client, err = fetcher(ctx, config)
	}
	cancel()
	if err == nil {
		c.updateRefreshProgress("promoting", 0, 0, false)
		c.persistMu.Lock()
		c.mu.RLock()
		previous := c.snapshot
		c.mu.RUnlock()
		mergeMediaFacts(&next, previous)
		if saveErr := saveCanonicalSnapshot(c.statePath, next); saveErr != nil {
			err = saveErr
		} else {
			c.mu.Lock()
			c.snapshot = next
			c.client = client
			c.sources = make(map[string]*xtream.VODSource)
			c.capabilities = make(map[string]xtream.CapabilityProbe)
			c.mu.Unlock()
			metrics.Promoted = true
		}
		c.persistMu.Unlock()
	}
	completed := time.Now().UTC()
	c.refreshMu.Lock()
	c.refreshing = false
	c.status.RefreshRunning = false
	c.status.LastRefreshCompleted = completed
	c.status.LastRefreshDuration = time.Since(started).Milliseconds()
	c.status.Metrics = metrics
	if err != nil {
		c.status.LastRefreshResult = "failed"
		c.status.Progress = catalogRefreshProgress{Phase: "failed"}
		c.status.LastRefreshError = sanitizeStatusError(err)
		log.Printf("real catalog refresh failed reason=%q; previous snapshot retained: %s", reason, sanitizeStatusError(err))
	} else {
		c.status.CatalogAvailable = true
		c.status.LastSuccessful = completed
		c.status.LastRefreshResult = "success"
		c.status.LastRefreshError = ""
		c.status.Progress = catalogRefreshProgress{Phase: "complete"}
		c.logSummary(next)
		log.Printf("real catalog refresh metrics mode=%q movie_candidates=%d show_candidates=%d reused=%d new=%d changed=%d membership_only=%d quarantine_retries=%d quarantine_recovered=%d removed=%d detail_requests=%d detail_skipped=%d promoted=%t", metrics.Mode, metrics.MovieCandidates, metrics.ShowCandidates, metrics.ReusedUnchanged, metrics.New, metrics.Changed, metrics.MembershipOnly, metrics.QuarantineRetries, metrics.QuarantineRecovered, metrics.Removed, metrics.DetailRequests, metrics.DetailRequestsSkipped, metrics.Promoted)
	}
	c.status.NextScheduledRefresh = completed.Add(c.config.refreshInterval())
	c.refreshMu.Unlock()
}

func catalogBuildMode(previous canonicalSnapshot, explicitFull bool) (bool, string) {
	if explicitFull {
		return true, "full_rebuild"
	}
	if previous.Version != realCatalogVersion {
		return true, "bootstrap"
	}
	return false, "incremental"
}

func hasCategoryExpansion(previous canonicalSnapshot, libraries librariesConfig) bool {
	movies, shows := map[string]bool{}, map[string]bool{}
	for _, category := range snapshotMovieCategories(previous) {
		movies[category.ID] = true
	}
	for _, category := range snapshotTVCategories(previous) {
		shows[category.ID] = true
	}
	for _, id := range libraries.MovieCategoryIDs {
		if !movies[id] {
			return true
		}
	}
	for _, id := range libraries.TVCategoryIDs {
		if !shows[id] {
			return true
		}
	}
	return false
}

func (c *catalogRuntime) updateRefreshMetrics(metrics catalogRefreshMetrics) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if c.refreshing {
		c.status.Metrics = metrics
	}
}

func (c *catalogRuntime) updateRefreshProgress(phase string, completed, total int, determinate bool) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if c.refreshing {
		c.status.Progress = catalogRefreshProgress{Phase: phase, Completed: completed, Total: total, Determinate: determinate && total > 0}
	}
}

func (c *catalogRuntime) clientForConfig(config appConfig) (*xtream.Client, error) {
	if err := validateAppConfig(config, false); err != nil {
		return nil, err
	}
	return xtream.NewClient(&http.Client{Timeout: providerRequestTimeout}, config.Provider.BaseURL, config.Provider.Username, config.Provider.Password)
}

func (c *catalogRuntime) snapshotCopy() canonicalSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, _ := json.Marshal(c.snapshot)
	var result canonicalSnapshot
	_ = json.Unmarshal(data, &result)
	return result
}

type catalogStatusResponse struct {
	CatalogAvailable       bool                   `json:"catalog_available"`
	RefreshRunning         bool                   `json:"refresh_running"`
	LastRefreshStarted     time.Time              `json:"last_refresh_started,omitempty"`
	LastRefreshCompleted   time.Time              `json:"last_refresh_completed,omitempty"`
	LastSuccessful         time.Time              `json:"last_successful_refresh,omitempty"`
	LastRefreshDuration    int64                  `json:"last_refresh_duration_ms,omitempty"`
	LastRefreshResult      string                 `json:"last_refresh_result"`
	LastRefreshError       string                 `json:"last_refresh_error,omitempty"`
	NextScheduledRefresh   time.Time              `json:"next_scheduled_refresh,omitempty"`
	SnapshotGeneratedAt    time.Time              `json:"snapshot_generated_at,omitempty"`
	SnapshotAgeSeconds     int64                  `json:"snapshot_age_seconds,omitempty"`
	SnapshotPath           string                 `json:"snapshot_path"`
	SnapshotSize           int64                  `json:"snapshot_size_bytes,omitempty"`
	Movies                 int                    `json:"movies"`
	Shows                  int                    `json:"shows"`
	Seasons                int                    `json:"seasons"`
	Episodes               int                    `json:"episodes"`
	Quarantine             int                    `json:"quarantine"`
	MediaFactsInspected    int                    `json:"media_facts_inspected"`
	OverlappingMovies      int                    `json:"overlapping_movies"`
	OverlappingShows       int                    `json:"overlapping_shows"`
	MaxCategoryMemberships int                    `json:"max_category_memberships"`
	MovieCategories        []catalogCategory      `json:"movie_categories,omitempty"`
	TVCategories           []catalogCategory      `json:"tv_categories,omitempty"`
	Metrics                catalogRefreshMetrics  `json:"metrics"`
	Progress               catalogRefreshProgress `json:"progress"`
}

func (c *catalogRuntime) catalogStatus() catalogStatusResponse {
	c.refreshMu.Lock()
	status := c.status
	c.refreshMu.Unlock()
	snapshot := c.snapshotCopy()
	result := catalogStatusResponse{
		CatalogAvailable: status.CatalogAvailable, RefreshRunning: status.RefreshRunning,
		LastRefreshStarted: status.LastRefreshStarted, LastRefreshCompleted: status.LastRefreshCompleted,
		LastSuccessful: status.LastSuccessful, LastRefreshDuration: status.LastRefreshDuration,
		LastRefreshResult: status.LastRefreshResult, LastRefreshError: status.LastRefreshError,
		NextScheduledRefresh: status.NextScheduledRefresh, SnapshotGeneratedAt: snapshot.GeneratedAt,
		SnapshotPath: c.statePath, Movies: len(snapshot.Movies), Shows: len(snapshot.Shows), Quarantine: len(snapshot.Quarantine),
		MovieCategories: snapshotMovieCategories(snapshot), TVCategories: snapshotTVCategories(snapshot),
		Metrics:  status.Metrics,
		Progress: status.Progress,
	}
	if !snapshot.GeneratedAt.IsZero() {
		result.SnapshotAgeSeconds = int64(time.Since(snapshot.GeneratedAt).Seconds())
	}
	if info, err := os.Stat(c.statePath); err == nil {
		result.SnapshotSize = info.Size()
	}
	for _, movie := range snapshot.Movies {
		memberships := len(itemCategories(movie.Category, movie.Categories))
		if memberships > 1 {
			result.OverlappingMovies++
		}
		if memberships > result.MaxCategoryMemberships {
			result.MaxCategoryMemberships = memberships
		}
		if movie.MediaFacts.State == "inspected" {
			result.MediaFactsInspected++
		}
	}
	for _, show := range snapshot.Shows {
		memberships := len(itemCategories(show.Category, show.Categories))
		if memberships > 1 {
			result.OverlappingShows++
		}
		if memberships > result.MaxCategoryMemberships {
			result.MaxCategoryMemberships = memberships
		}
		result.Seasons += len(show.Seasons)
		for _, season := range show.Seasons {
			result.Episodes += len(season.Episodes)
			for _, episode := range season.Episodes {
				if episode.MediaFacts.State == "inspected" {
					result.MediaFactsInspected++
				}
			}
		}
	}
	return result
}

func (c *catalogRuntime) fetch(ctx context.Context, config appConfig) (canonicalSnapshot, *xtream.Client, error) {
	snapshot, client, _, err := c.fetchCatalog(ctx, config, canonicalSnapshot{}, true)
	return snapshot, client, err
}

func (c *catalogRuntime) fetchCatalog(ctx context.Context, config appConfig, previous canonicalSnapshot, fullRebuild bool) (canonicalSnapshot, *xtream.Client, catalogRefreshMetrics, error) {
	mode := "incremental"
	if fullRebuild {
		mode = "full_rebuild"
	}
	return c.fetchCatalogWithMode(ctx, config, previous, fullRebuild, mode)
}

func (c *catalogRuntime) fetchCatalogWithMode(ctx context.Context, config appConfig, previous canonicalSnapshot, fullRebuild bool, mode string) (canonicalSnapshot, *xtream.Client, catalogRefreshMetrics, error) {
	metrics := catalogRefreshMetrics{Mode: mode, EffectiveConcurrency: config.Catalog.RefreshConcurrency}
	c.updateRefreshProgress("category_indexes", 0, 0, false)
	client, err := c.clientForConfig(config)
	if err != nil {
		return canonicalSnapshot{}, nil, metrics, err
	}
	refreshStarted := time.Now()
	topLevelStarted := refreshStarted
	movieCategories, err := client.VODCategories(ctx)
	if err != nil {
		return canonicalSnapshot{}, nil, metrics, err
	}
	tvCategories, err := client.SeriesCategories(ctx)
	if err != nil {
		return canonicalSnapshot{}, nil, metrics, err
	}
	result := canonicalSnapshot{Version: realCatalogVersion, GeneratedAt: time.Now().UTC()}
	type categorizedMovie struct {
		item       xtream.Movie
		category   catalogCategory
		categories []canonical.Category
	}
	var movieItems []categorizedMovie
	seenMovies := make(map[string]int)
	for _, categoryID := range config.Libraries.MovieCategoryIDs {
		category, categoryErr := selectCategoryByID(movieCategories, categoryID)
		if categoryErr != nil {
			return canonicalSnapshot{}, nil, metrics, categoryErr
		}
		items, movieErr := client.Movies(ctx, category.ID)
		if movieErr != nil {
			return canonicalSnapshot{}, nil, metrics, movieErr
		}
		selected := catalogCategory{ID: category.ID, Name: category.Name, Count: len(items)}
		result.MovieCategories = append(result.MovieCategories, selected)
		for _, item := range items {
			membership := canonical.Category{ID: selected.ID, Name: selected.Name}
			if index, exists := seenMovies[item.ID]; exists {
				if movieListingDiffers(movieItems[index].item, item) {
					log.Printf("real catalog duplicate movie metadata differs source_id=%q first_category=%q additional_category=%q", item.ID, movieItems[index].category.ID, selected.ID)
				}
				movieItems[index].categories = appendUniqueCategory(movieItems[index].categories, membership)
				continue
			}
			seenMovies[item.ID] = len(movieItems)
			movieItems = append(movieItems, categorizedMovie{item: item, category: selected, categories: []canonical.Category{membership}})
		}
	}
	result.MovieCategory = result.MovieCategories[0]
	type categorizedSeries struct {
		item       xtream.Series
		category   catalogCategory
		categories []canonical.Category
	}
	var showItems []categorizedSeries
	seenSeries := make(map[string]int)
	for _, categoryID := range config.Libraries.TVCategoryIDs {
		category, categoryErr := selectCategoryByID(tvCategories, categoryID)
		if categoryErr != nil {
			return canonicalSnapshot{}, nil, metrics, categoryErr
		}
		items, seriesErr := client.Series(ctx, category.ID)
		if seriesErr != nil {
			return canonicalSnapshot{}, nil, metrics, seriesErr
		}
		selected := catalogCategory{ID: category.ID, Name: category.Name, Count: len(items)}
		result.TVCategories = append(result.TVCategories, selected)
		for _, item := range items {
			membership := canonical.Category{ID: selected.ID, Name: selected.Name}
			if index, exists := seenSeries[item.ID]; exists {
				if seriesListingDiffers(showItems[index].item, item) {
					log.Printf("real catalog duplicate show metadata differs source_id=%q first_category=%q additional_category=%q", item.ID, showItems[index].category.ID, selected.ID)
				}
				showItems[index].categories = appendUniqueCategory(showItems[index].categories, membership)
				continue
			}
			seenSeries[item.ID] = len(showItems)
			showItems = append(showItems, categorizedSeries{item: item, category: selected, categories: []canonical.Category{membership}})
		}
	}
	// Retain the original primary field so a newly written snapshot remains
	// readable by the previous single-category binary during rollback.
	result.TVCategory = result.TVCategories[0]
	metrics.MovieCandidates = len(movieItems)
	metrics.ShowCandidates = len(showItems)
	c.updateRefreshProgress("comparing", 0, 0, false)
	log.Printf("real catalog refresh timing phase=top_level duration_ms=%d movies=%d shows=%d tv_categories=%d", time.Since(topLevelStarted).Milliseconds(), len(movieItems), len(showItems), len(result.TVCategories))

	movieDetailsStarted := time.Now()
	previousMovies := make(map[string]canonicalMovie, len(previous.Movies))
	for _, movie := range previous.Movies {
		previousMovies[movie.SourceID] = movie
	}
	previousShows := make(map[string]canonicalShow, len(previous.Shows))
	for _, show := range previous.Shows {
		previousShows[show.SourceID] = show
	}
	quarantinedMovies := make(map[string]quarantineEntry)
	quarantinedShows := make(map[string]quarantineEntry)
	for _, item := range previous.Quarantine {
		switch item.Kind {
		case "movie":
			quarantinedMovies[item.SourceID] = item
		case "show":
			quarantinedShows[item.SourceID] = item
		case "episode":
			if item.ParentSourceID != "" {
				quarantinedShows[item.ParentSourceID] = item
				continue
			}
			for _, show := range previous.Shows {
				if showContainsEpisode(show, item.SourceID) {
					quarantinedShows[show.SourceID] = item
				}
			}
		}
	}
	for sourceID := range previousMovies {
		if _, exists := seenMovies[sourceID]; !exists {
			metrics.Removed++
		}
	}
	for sourceID := range previousShows {
		if _, exists := seenSeries[sourceID]; !exists {
			metrics.Removed++
		}
	}

	type movieWork struct {
		item     categorizedMovie
		retry    bool
		previous quarantineEntry
		fallback *canonicalMovie
	}
	var movieWorkItems []movieWork
	for _, job := range movieItems {
		fingerprint := movieListFingerprint(job.item)
		old, exists := previousMovies[job.item.ID]
		var fallback *canonicalMovie
		if exists {
			preserved := old
			preserved.Category, preserved.Categories = job.category, job.categories
			fallback = &preserved
		}
		previousQuarantine, retry := quarantinedMovies[job.item.ID]
		switch {
		case retry:
			metrics.QuarantineRetries++
			movieWorkItems = append(movieWorkItems, movieWork{item: job, retry: true, previous: previousQuarantine, fallback: fallback})
		case !exists:
			metrics.New++
			movieWorkItems = append(movieWorkItems, movieWork{item: job, fallback: fallback})
		case fullRebuild:
			movieWorkItems = append(movieWorkItems, movieWork{item: job, fallback: fallback})
		case movieListingMatches(old, job.item, fingerprint):
			old.Category = job.category
			old.Categories = job.categories
			old.Revision = job.item.Revision
			old.ListFingerprint = fingerprint
			if sameCategories(itemCategories(previousMovies[job.item.ID].Category, previousMovies[job.item.ID].Categories), job.categories) {
				metrics.ReusedUnchanged++
			} else {
				metrics.MembershipOnly++
			}
			metrics.DetailRequestsSkipped++
			result.Movies = append(result.Movies, old)
		default:
			metrics.Changed++
			movieWorkItems = append(movieWorkItems, movieWork{item: job, fallback: fallback})
		}
	}
	metrics.DetailRequests += len(movieWorkItems)
	c.updateRefreshMetrics(metrics)
	if len(movieWorkItems) > 0 {
		c.updateRefreshProgress("enriching_movies", 0, len(movieWorkItems), true)
	}
	type movieResult struct {
		movie    canonicalMovie
		q        *quarantineEntry
		retry    bool
		fallback *canonicalMovie
	}
	movieJobs := make(chan movieWork)
	movieResults := make(chan movieResult)
	movieFailures := 0
	var workers sync.WaitGroup
	workerCount := config.Catalog.RefreshConcurrency
	if len(movieWorkItems) < workerCount {
		workerCount = len(movieWorkItems)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for work := range movieJobs {
				job, item := work.item, work.item.item
				details, detailErr := client.MovieDetails(ctx, item.ID)
				if detailErr != nil {
					entry := newQuarantineEntry("movie", item.ID, item.Name, job.categories, "metadata request failed", detailErr, work.previous)
					movieResults <- movieResult{q: &entry, retry: work.retry, fallback: work.fallback}
					continue
				}
				extension := firstNonEmptyString(details.Extension, item.Extension, "mkv")
				title := firstNonEmptyString(details.Title, item.Name)
				if title == "" {
					entry := newQuarantineEntry("movie", item.ID, item.Name, job.categories, "title is empty", errCatalogNormalization, work.previous)
					movieResults <- movieResult{q: &entry, retry: work.retry, fallback: work.fallback}
					continue
				}
				movieResults <- movieResult{movie: canonicalMovie{
					SourceID: item.ID, Category: job.category, Categories: job.categories, Title: title,
					Year: firstNonEmptyString(details.Year, item.Year), Extension: extension,
					Revision: item.Revision, ListFingerprint: movieListFingerprint(item), Details: details,
				}, retry: work.retry}
			}
		}()
	}
	go func() {
		defer close(movieJobs)
		for _, item := range movieWorkItems {
			movieJobs <- item
		}
	}()
	go func() { workers.Wait(); close(movieResults) }()
	movieCompleted := 0
	for item := range movieResults {
		movieCompleted++
		metrics.DetailCompleted++
		if item.q != nil {
			movieFailures++
			result.Quarantine = append(result.Quarantine, *item.q)
			if item.fallback != nil {
				result.Movies = append(result.Movies, *item.fallback)
			}
		} else {
			result.Movies = append(result.Movies, item.movie)
			if item.retry {
				metrics.QuarantineRecovered++
			}
		}
		c.updateRefreshMetrics(metrics)
		if movieCompleted < len(movieWorkItems) {
			c.updateRefreshProgress("enriching_movies", movieCompleted, len(movieWorkItems), true)
		} else {
			c.updateRefreshProgress("comparing", 0, 0, false)
		}
	}
	if err := ctx.Err(); err != nil {
		return canonicalSnapshot{}, nil, metrics, fmt.Errorf("real catalog: movie detail traversal did not complete: %w", err)
	}
	if excessiveDetailFailures(movieFailures, len(movieWorkItems)) {
		return canonicalSnapshot{}, nil, metrics, fmt.Errorf("real catalog: movie detail traversal failed for %d of %d requested items", movieFailures, len(movieWorkItems))
	}
	c.updateRefreshMetrics(metrics)
	log.Printf("real catalog refresh timing phase=movie_details duration_ms=%d accepted=%d quarantine=%d", time.Since(movieDetailsStarted).Milliseconds(), len(result.Movies), len(result.Quarantine))

	tvDetailsStarted := time.Now()
	type showWork struct {
		item     categorizedSeries
		retry    bool
		previous quarantineEntry
		fallback *canonicalShow
	}
	var showWorkItems []showWork
	for _, job := range showItems {
		fingerprint := seriesListFingerprint(job.item)
		old, exists := previousShows[job.item.ID]
		var fallback *canonicalShow
		if exists {
			preserved := old
			preserved.Category, preserved.Categories = job.category, job.categories
			fallback = &preserved
		}
		previousQuarantine, retry := quarantinedShows[job.item.ID]
		switch {
		case retry:
			metrics.QuarantineRetries++
			showWorkItems = append(showWorkItems, showWork{item: job, retry: true, previous: previousQuarantine, fallback: fallback})
		case !exists:
			metrics.New++
			showWorkItems = append(showWorkItems, showWork{item: job, fallback: fallback})
		case fullRebuild:
			showWorkItems = append(showWorkItems, showWork{item: job, fallback: fallback})
		case seriesListingMatches(old, job.item, fingerprint):
			old.Category = job.category
			old.Categories = job.categories
			old.Revision = job.item.Revision
			old.ListFingerprint = fingerprint
			if sameCategories(itemCategories(previousShows[job.item.ID].Category, previousShows[job.item.ID].Categories), job.categories) {
				metrics.ReusedUnchanged++
			} else {
				metrics.MembershipOnly++
			}
			metrics.DetailRequestsSkipped++
			result.Shows = append(result.Shows, old)
		default:
			metrics.Changed++
			showWorkItems = append(showWorkItems, showWork{item: job, fallback: fallback})
		}
	}
	metrics.DetailRequests += len(showWorkItems)
	c.updateRefreshMetrics(metrics)
	if len(showWorkItems) > 0 {
		c.updateRefreshProgress("enriching_shows", 0, len(showWorkItems), true)
	}
	type showResult struct {
		show       canonicalShow
		q          *quarantineEntry
		quarantine []quarantineEntry
		retry      bool
		fallback   *canonicalShow
	}
	showJobs := make(chan showWork)
	showResults := make(chan showResult)
	showFailures := 0
	workers = sync.WaitGroup{}
	workerCount = config.Catalog.RefreshConcurrency
	if len(showWorkItems) < workerCount {
		workerCount = len(showWorkItems)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for work := range showJobs {
				job, item := work.item, work.item.item
				details, detailErr := client.SeriesDetails(ctx, item.ID)
				if detailErr != nil {
					entry := newQuarantineEntry("show", item.ID, item.Name, job.categories, "series traversal failed", detailErr, work.previous)
					showResults <- showResult{q: &entry, retry: work.retry, fallback: work.fallback}
					continue
				}
				show := canonicalShow{
					SourceID: item.ID, Category: job.category, Categories: job.categories,
					Title: firstNonEmptyString(details.Title, item.Name), Year: firstNonEmptyString(details.Year, item.Year),
					Revision: item.Revision, ListFingerprint: seriesListFingerprint(item), Details: details,
				}
				seasonPosters := make(map[int]string)
				var episodeQuarantine []quarantineEntry
				for _, season := range details.Seasons {
					seasonPosters[season.Number] = season.Poster
				}
				bySeason := make(map[int][]canonicalEpisode)
				for _, episode := range details.Episodes {
					if episode.ID == "" || episode.Season <= 0 || episode.Number <= 0 {
						entry := newQuarantineEntry("episode", episode.ID, episode.Title, job.categories, "invalid season or episode identity", errCatalogNormalization, quarantineEntry{})
						entry.ParentSourceID = item.ID
						episodeQuarantine = append(episodeQuarantine, entry)
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
					entry := newQuarantineEntry("show", item.ID, item.Name, job.categories, "series has no usable episodes", errCatalogNormalization, work.previous)
					showResults <- showResult{q: &entry, retry: work.retry, fallback: work.fallback}
					continue
				}
				showResults <- showResult{show: show, quarantine: episodeQuarantine, retry: work.retry}
			}
		}()
	}
	go func() {
		defer close(showJobs)
		for _, item := range showWorkItems {
			showJobs <- item
		}
	}()
	go func() { workers.Wait(); close(showResults) }()
	showCompleted := 0
	for item := range showResults {
		showCompleted++
		metrics.DetailCompleted++
		if item.q != nil {
			showFailures++
			result.Quarantine = append(result.Quarantine, *item.q)
			if item.fallback != nil {
				result.Shows = append(result.Shows, *item.fallback)
			}
		} else {
			result.Shows = append(result.Shows, item.show)
			result.Quarantine = append(result.Quarantine, item.quarantine...)
			if item.retry {
				metrics.QuarantineRecovered++
			}
		}
		c.updateRefreshMetrics(metrics)
		if showCompleted < len(showWorkItems) {
			c.updateRefreshProgress("enriching_shows", showCompleted, len(showWorkItems), true)
		} else {
			c.updateRefreshProgress("validating", 0, 0, false)
		}
	}
	if err := ctx.Err(); err != nil {
		return canonicalSnapshot{}, nil, metrics, fmt.Errorf("real catalog: TV detail traversal did not complete: %w", err)
	}
	if excessiveDetailFailures(showFailures, len(showWorkItems)) {
		return canonicalSnapshot{}, nil, metrics, fmt.Errorf("real catalog: TV detail traversal failed for %d of %d requested items", showFailures, len(showWorkItems))
	}
	c.updateRefreshMetrics(metrics)

	c.updateRefreshProgress("validating", 0, 0, false)
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
		return canonicalSnapshot{}, nil, metrics, err
	}
	log.Printf("real catalog refresh timing phase=tv_details duration_ms=%d accepted=%d quarantine_total=%d", time.Since(tvDetailsStarted).Milliseconds(), len(result.Shows), len(result.Quarantine))
	log.Printf("real catalog refresh timing phase=total duration_ms=%d", time.Since(refreshStarted).Milliseconds())
	return result, client, metrics, nil
}

func movieListFingerprint(item xtream.Movie) string {
	if item.Fingerprint != "" {
		return item.Fingerprint
	}
	return listFingerprint("movie", item.ID, item.Name, item.Year, item.Extension, item.Revision)
}

func seriesListFingerprint(item xtream.Series) string {
	if item.Fingerprint != "" {
		return item.Fingerprint
	}
	return listFingerprint("show", item.ID, item.Name, item.Year, item.Revision)
}

func listFingerprint(fields ...string) string {
	hash := sha256.New()
	for _, field := range fields {
		_, _ = hash.Write([]byte(strings.TrimSpace(field)))
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func movieListingMatches(old canonicalMovie, item xtream.Movie, fingerprint string) bool {
	if old.ListFingerprint != "" {
		return old.ListFingerprint == fingerprint
	}
	// Version-1 snapshots persisted the movie list's source ID, extension, and
	// revision (`last_modified`, falling back to `added`). Title and year came
	// from detail responses and cannot safely be compared with list formatting.
	// This one-time bridge trusts only fields known to share provenance; the
	// newly persisted strong fingerprint is used on every later refresh.
	return old.SourceID == item.ID && old.Extension == item.Extension && old.Revision == item.Revision
}

func seriesListingMatches(old canonicalShow, item xtream.Series, fingerprint string) bool {
	if old.ListFingerprint != "" {
		return old.ListFingerprint == fingerprint
	}
	return old.SourceID == item.ID && old.Title == item.Name && old.Year == item.Year && old.Revision == item.Revision
}

func sameCategories(first, second []canonical.Category) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func showContainsEpisode(show canonicalShow, sourceID string) bool {
	if sourceID == "" {
		return false
	}
	for _, season := range show.Seasons {
		for _, episode := range season.Episodes {
			if episode.SourceID == sourceID {
				return true
			}
		}
	}
	return false
}

func selectCategoryByID(categories []xtream.Category, id string) (xtream.Category, error) {
	for _, category := range categories {
		if category.ID == id {
			return category, nil
		}
	}
	return xtream.Category{}, fmt.Errorf("real catalog: category ID %q was not returned by provider", id)
}

func appendUniqueCategory(categories []canonical.Category, category canonical.Category) []canonical.Category {
	for _, existing := range categories {
		if existing.ID == category.ID {
			return categories
		}
	}
	return append(categories, category)
}

var errCatalogNormalization = errors.New("catalog normalization failed")

func newQuarantineEntry(kind, sourceID, title string, categories []canonical.Category, reason string, failure error, previous quarantineEntry) quarantineEntry {
	class, status := xtream.ErrorDetails(failure)
	if errors.Is(failure, errCatalogNormalization) {
		class = "normalization"
	}
	failureTime := time.Now().UTC()
	entry := quarantineEntry{
		Kind: kind, SourceID: sourceID, Title: strings.TrimSpace(title),
		Categories: append([]canonical.Category(nil), categories...), Reason: reason,
		ReasonClass: class, HTTPStatus: status, LastFailure: &failureTime,
	}
	if class == "transport" || class == "timeout" || class == "canceled" {
		entry.TransportClass = class
	}
	if previous.SourceID != "" || previous.Reason != "" {
		entry.RetryCount = previous.RetryCount + 1
		entry.LastRetryResult = "failed"
	}
	return entry
}

func excessiveDetailFailures(failures, total int) bool {
	return total > 0 && failures > 10 && failures*5 > total
}

func movieListingDiffers(first, next xtream.Movie) bool {
	return first.Name != next.Name || first.Year != next.Year || first.Extension != next.Extension || first.Revision != next.Revision
}

func seriesListingDiffers(first, next xtream.Series) bool {
	return first.Name != next.Name || first.Year != next.Year || first.Revision != next.Revision
}

func itemCategories(primary catalogCategory, categories []canonical.Category) []canonical.Category {
	if len(categories) > 0 {
		return categories
	}
	if primary.ID != "" || primary.Name != "" {
		return []canonical.Category{{ID: primary.ID, Name: primary.Name}}
	}
	return nil
}

func normalizeSnapshotMemberships(snapshot *canonicalSnapshot) {
	for index := range snapshot.Movies {
		item := &snapshot.Movies[index]
		item.Categories = itemCategories(item.Category, item.Categories)
		if item.Category.ID == "" && len(item.Categories) > 0 {
			item.Category = catalogCategory{ID: item.Categories[0].ID, Name: item.Categories[0].Name}
		}
	}
	for index := range snapshot.Shows {
		item := &snapshot.Shows[index]
		item.Categories = itemCategories(item.Category, item.Categories)
		if item.Category.ID == "" && len(item.Categories) > 0 {
			item.Category = catalogCategory{ID: item.Categories[0].ID, Name: item.Categories[0].Name}
		}
	}
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

func snapshotMovieCategories(snapshot canonicalSnapshot) []catalogCategory {
	if len(snapshot.MovieCategories) > 0 {
		return snapshot.MovieCategories
	}
	if snapshot.MovieCategory.ID != "" {
		return []catalogCategory{snapshot.MovieCategory}
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
	for _, category := range snapshotMovieCategories(snapshot) {
		accepted := 0
		for _, movie := range snapshot.Movies {
			if hasSourceCategory(movie.Category, movie.Categories, category.ID, "") {
				accepted++
			}
		}
		log.Printf("real catalog selected category=%q provider_id=%s top_level=%d accepted=%d quarantined=%d", category.Name, category.ID, category.Count, accepted, movieRejected)
	}
	for _, category := range snapshotTVCategories(snapshot) {
		accepted, categorySeasons, categoryEpisodes := 0, 0, 0
		for _, show := range snapshot.Shows {
			if !hasSourceCategory(show.Category, show.Categories, category.ID, "") {
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
	normalizeSnapshotMemberships(&snapshot)
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
	client := c.client
	c.mu.RUnlock()
	if existing != nil {
		return existing, nil
	}
	if client == nil {
		return nil, errors.New("provider is not configured")
	}
	var source *xtream.VODSource
	var err error
	if item.Kind == "movie" {
		source, err = client.MovieSource(item.SourceID, item.Extension)
	} else {
		source, err = client.EpisodeSource(item.SourceID, item.Extension)
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
