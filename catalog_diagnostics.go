package main

import (
	"context"
	"fmt"
	"sort"
	"time"

	canonical "wonkvod/internal/catalog"
)

const categoryExpansionWarningThreshold = 100

type categoryImpactPreview struct {
	CurrentMovies        int  `json:"current_movies"`
	CurrentShows         int  `json:"current_shows"`
	ResultMovies         int  `json:"result_movies"`
	ResultShows          int  `json:"result_shows"`
	NewMovies            int  `json:"new_movies"`
	NewShows             int  `json:"new_shows"`
	RemovedMovies        int  `json:"removed_movies"`
	RemovedShows         int  `json:"removed_shows"`
	MembershipOnly       int  `json:"membership_only"`
	MovieOverlaps        int  `json:"movie_overlaps"`
	ShowOverlaps         int  `json:"show_overlaps"`
	NewTopLevel          int  `json:"new_top_level"`
	Warning              bool `json:"large_expansion"`
	SuggestedConcurrency int  `json:"suggested_concurrency,omitempty"`
	WarningThreshold     int  `json:"warning_threshold"`
}

func (c *catalogRuntime) previewCategoryImpact(ctx context.Context, libraries librariesConfig) (categoryImpactPreview, error) {
	config := c.config.snapshot()
	config.Libraries = libraries
	if err := validateAppConfig(config, false); err != nil {
		return categoryImpactPreview{}, err
	}
	client, err := c.clientForConfig(config)
	if err != nil {
		return categoryImpactPreview{}, err
	}
	movieCategories, err := client.VODCategories(ctx)
	if err != nil {
		return categoryImpactPreview{}, err
	}
	showCategories, err := client.SeriesCategories(ctx)
	if err != nil {
		return categoryImpactPreview{}, err
	}
	movieNames, showNames := map[string]string{}, map[string]string{}
	for _, item := range movieCategories {
		movieNames[item.ID] = item.Name
	}
	for _, item := range showCategories {
		showNames[item.ID] = item.Name
	}
	movieMemberships, showMemberships := map[string][]canonical.Category{}, map[string][]canonical.Category{}
	for _, id := range libraries.MovieCategoryIDs {
		name, ok := movieNames[id]
		if !ok {
			return categoryImpactPreview{}, fmt.Errorf("unknown movie category %q", id)
		}
		items, listErr := client.Movies(ctx, id)
		if listErr != nil {
			return categoryImpactPreview{}, listErr
		}
		for _, item := range items {
			movieMemberships[item.ID] = appendUniqueCategory(movieMemberships[item.ID], canonical.Category{ID: id, Name: name})
		}
	}
	for _, id := range libraries.TVCategoryIDs {
		name, ok := showNames[id]
		if !ok {
			return categoryImpactPreview{}, fmt.Errorf("unknown TV category %q", id)
		}
		items, listErr := client.Series(ctx, id)
		if listErr != nil {
			return categoryImpactPreview{}, listErr
		}
		for _, item := range items {
			showMemberships[item.ID] = appendUniqueCategory(showMemberships[item.ID], canonical.Category{ID: id, Name: name})
		}
	}
	snapshot := c.snapshotCopy()
	preview := categoryImpactPreview{CurrentMovies: len(snapshot.Movies), CurrentShows: len(snapshot.Shows), ResultMovies: len(movieMemberships), ResultShows: len(showMemberships), WarningThreshold: categoryExpansionWarningThreshold}
	oldMovies, oldShows := map[string][]canonical.Category{}, map[string][]canonical.Category{}
	for _, item := range snapshot.Movies {
		oldMovies[item.SourceID] = itemCategories(item.Category, item.Categories)
	}
	for _, item := range snapshot.Shows {
		oldShows[item.SourceID] = itemCategories(item.Category, item.Categories)
	}
	for id, memberships := range movieMemberships {
		if len(memberships) > 1 {
			preview.MovieOverlaps++
		}
		if old, ok := oldMovies[id]; !ok {
			preview.NewMovies++
		} else if !sameCategories(old, memberships) {
			preview.MembershipOnly++
		}
	}
	for id, memberships := range showMemberships {
		if len(memberships) > 1 {
			preview.ShowOverlaps++
		}
		if old, ok := oldShows[id]; !ok {
			preview.NewShows++
		} else if !sameCategories(old, memberships) {
			preview.MembershipOnly++
		}
	}
	for id := range oldMovies {
		if _, ok := movieMemberships[id]; !ok {
			preview.RemovedMovies++
		}
	}
	for id := range oldShows {
		if _, ok := showMemberships[id]; !ok {
			preview.RemovedShows++
		}
	}
	preview.NewTopLevel = preview.NewMovies + preview.NewShows
	applyExpansionGuidance(&preview)
	return preview, nil
}

func applyExpansionGuidance(preview *categoryImpactPreview) {
	preview.Warning = preview.NewTopLevel > categoryExpansionWarningThreshold
	if preview.Warning {
		switch {
		case preview.NewTopLevel >= 3000:
			preview.SuggestedConcurrency = 10
		case preview.NewTopLevel >= 1000:
			preview.SuggestedConcurrency = 8
		default:
			preview.SuggestedConcurrency = 4
		}
	}
}

type quarantineItemResponse struct {
	quarantineEntry
	Retryable bool `json:"retryable"`
}

type quarantineResponse struct {
	Total         int                      `json:"total"`
	Retryable     int                      `json:"retryable"`
	Movies        int                      `json:"movies"`
	Shows         int                      `json:"shows"`
	Episodes      int                      `json:"episodes"`
	ReasonBuckets map[string]int           `json:"reason_buckets"`
	Items         []quarantineItemResponse `json:"items"`
}

func quarantineRetryable(item quarantineEntry) bool {
	if item.Kind == "episode" && item.ParentSourceID == "" {
		return false
	}
	switch item.ReasonClass {
	case "normalization":
		return false
	case "http":
		return item.HTTPStatus == 408 || item.HTTPStatus == 429 || item.HTTPStatus >= 500
	default:
		return true
	}
}

func (c *catalogRuntime) quarantineStatus() quarantineResponse {
	snapshot := c.snapshotCopy()
	result := quarantineResponse{Total: len(snapshot.Quarantine), ReasonBuckets: map[string]int{}, Items: []quarantineItemResponse{}}
	for _, item := range snapshot.Quarantine {
		class := item.ReasonClass
		if class == "" {
			class = "legacy"
		}
		result.ReasonBuckets[class]++
		switch item.Kind {
		case "movie":
			result.Movies++
		case "show":
			result.Shows++
		case "episode":
			result.Episodes++
		}
		result.Items = append(result.Items, quarantineItemResponse{quarantineEntry: item, Retryable: quarantineRetryable(item)})
		if quarantineRetryable(item) {
			result.Retryable++
		}
	}
	sort.Slice(result.Items, func(i, j int) bool {
		if result.Items[i].Kind != result.Items[j].Kind {
			return result.Items[i].Kind < result.Items[j].Kind
		}
		return result.Items[i].SourceID < result.Items[j].SourceID
	})
	return result
}

func (c *catalogRuntime) hasQuarantineItem(kind, sourceID string) (quarantineEntry, bool) {
	for _, item := range c.snapshotCopy().Quarantine {
		if item.Kind == kind && item.SourceID == sourceID {
			return item, true
		}
	}
	return quarantineEntry{}, false
}

func previewTimeoutContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 2*time.Minute)
}
