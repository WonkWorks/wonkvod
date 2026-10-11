package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

func TestFetchMergesMultiCategoryMembership(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		categoryID := r.URL.Query().Get("category_id")
		switch r.URL.Query().Get("action") {
		case "get_vod_categories":
			_, _ = w.Write([]byte(`[{"category_id":"a","category_name":"Action"},{"category_id":"d","category_name":"Drama"}]`))
		case "get_series_categories":
			_, _ = w.Write([]byte(`[{"category_id":"r","category_name":"Reality"},{"category_id":"n","category_name":"News"}]`))
		case "get_vod_streams":
			_, _ = w.Write([]byte(`[{"stream_id":"movie-1","name":"Shared Movie","year":"2024","category_id":"` + categoryID + `","container_extension":"mkv","added":"1700000000"}]`))
		case "get_series":
			_, _ = w.Write([]byte(`[{"series_id":"show-1","name":"Shared Show","year":"2023","category_id":"` + categoryID + `","last_modified":"1700000001"}]`))
		case "get_vod_info":
			_, _ = w.Write([]byte(`{"info":{"name":"Shared Movie","year":"2024","genre":"Thriller"},"movie_data":{"stream_id":"movie-1","name":"Shared Movie","container_extension":"mkv"}}`))
		case "get_series_info":
			_, _ = w.Write([]byte(`{"info":{"name":"Shared Show","year":"2023","genre":"Documentary"},"seasons":[{"season_number":1}],"episodes":{"1":[{"id":"episode-1","episode_num":1,"season":1,"title":"Episode 1","container_extension":"mkv","info":{"duration_secs":60}}]}}`))
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: provider.URL, Username: "user", Password: "secret"}
	config.Libraries = librariesConfig{MovieCategoryIDs: []string{"a", "d"}, TVCategoryIDs: []string{"r", "n"}}
	runtime := &catalogRuntime{}
	snapshot, client, err := runtime.fetch(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Movies) != 1 || len(snapshot.Shows) != 1 {
		t.Fatalf("unique objects movies=%d shows=%d", len(snapshot.Movies), len(snapshot.Shows))
	}
	if got := snapshot.Movies[0].Categories; !reflect.DeepEqual(got, []canonical.Category{{ID: "a", Name: "Action"}, {ID: "d", Name: "Drama"}}) {
		t.Fatalf("movie memberships=%#v", got)
	}
	if got := snapshot.Shows[0].Categories; !reflect.DeepEqual(got, []canonical.Category{{ID: "r", Name: "Reality"}, {ID: "n", Name: "News"}}) {
		t.Fatalf("show memberships=%#v", got)
	}
	if snapshot.MovieCategories[0].Count+snapshot.MovieCategories[1].Count != 2 || len(snapshot.Movies) != 1 {
		t.Fatal("overlapping category counts were incorrectly forced to the unique total")
	}
	oldMovieID, oldMediaID, oldPartID := movieObjectID(snapshot.Movies[0]), mediaObjectID("movie", "movie-1"), partObjectID("movie", "movie-1")
	snapshot.Movies[0].MediaFacts = canonical.MediaFacts{State: "inspected", Container: "mkv"}
	previous := snapshot
	next := snapshot
	next.Movies = append([]canonicalMovie(nil), snapshot.Movies...)
	next.Movies[0].MediaFacts = canonical.MediaFacts{}
	mergeMediaFacts(&next, previous)
	if next.Movies[0].MediaFacts.State != "inspected" || movieObjectID(next.Movies[0]) != oldMovieID || mediaObjectID("movie", next.Movies[0].SourceID) != oldMediaID || partObjectID("movie", next.Movies[0].SourceID) != oldPartID {
		t.Fatal("overlap merge changed stable IDs or lost MediaFacts")
	}
	runtime = &catalogRuntime{snapshot: next, client: client, sources: map[string]*xtream.VODSource{}, capabilities: map[string]xtream.CapabilityProbe{}}
	handler := &realCatalogHandler{catalog: runtime}
	for _, name := range []string{"Action", "Drama"} {
		facets := movieGenreFacets(next.Movies)
		var id string
		for _, facet := range facets {
			if facet.Title == name {
				id = facet.ID
			}
		}
		if got := filterMoviesByGenre(next.Movies, id); len(got) != 1 || got[0].SourceID != "movie-1" {
			t.Fatalf("movie missing from %s filter: %#v", name, got)
		}
	}
	if got := filterMoviesByGenre(next.Movies, ""); len(got) != 1 {
		t.Fatalf("unfiltered movie count=%d", len(got))
	}
	status := runtime.catalogStatus()
	if status.OverlappingMovies != 1 || status.OverlappingShows != 1 || status.MaxCategoryMemberships != 2 {
		t.Fatalf("overlap status=%#v", status)
	}
	_ = handler
}

func TestFetchHonorsConfiguredDetailConcurrency(t *testing.T) {
	var active, maximum atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "get_vod_categories":
			_, _ = w.Write([]byte(`[{"category_id":"m","category_name":"Movies"}]`))
		case "get_series_categories":
			_, _ = w.Write([]byte(`[{"category_id":"t","category_name":"TV"}]`))
		case "get_vod_streams":
			_, _ = w.Write([]byte(`[{"stream_id":"1","name":"One","category_id":"m"},{"stream_id":"2","name":"Two","category_id":"m"},{"stream_id":"3","name":"Three","category_id":"m"},{"stream_id":"4","name":"Four","category_id":"m"},{"stream_id":"5","name":"Five","category_id":"m"},{"stream_id":"6","name":"Six","category_id":"m"}]`))
		case "get_series":
			_, _ = w.Write([]byte(`[]`))
		case "get_vod_info":
			current := active.Add(1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			id := r.URL.Query().Get("vod_id")
			_, _ = w.Write([]byte(`{"info":{"name":"Movie ` + id + `"},"movie_data":{"stream_id":"` + id + `","container_extension":"mkv"}}`))
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: provider.URL, Username: "user", Password: "secret"}
	config.Libraries = librariesConfig{MovieCategoryIDs: []string{"m"}, TVCategoryIDs: []string{"t"}}
	config.Catalog.RefreshConcurrency = 2
	if _, _, err := (&catalogRuntime{}).fetch(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent detail requests = %d, want 2", got)
	}
}

func TestLegacySnapshotLoadsSingleMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	legacy := canonicalSnapshot{Version: realCatalogVersion, MovieCategory: catalogCategory{ID: "917", Name: "Black Cinema"}, Movies: []canonicalMovie{{SourceID: "movie", Category: catalogCategory{ID: "917", Name: "Black Cinema"}, MediaFacts: canonical.MediaFacts{State: "inspected"}}}}
	if err := saveCanonicalSnapshot(path, legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCanonicalSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Movies[0].Categories; !reflect.DeepEqual(got, []canonical.Category{{ID: "917", Name: "Black Cinema"}}) || loaded.Movies[0].MediaFacts.State != "inspected" {
		t.Fatalf("legacy membership/media facts=%#v %#v", got, loaded.Movies[0].MediaFacts)
	}
}

func TestFetchDeadlineDoesNotReturnPartialSnapshot(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "get_vod_categories":
			_, _ = w.Write([]byte(`[{"category_id":"m","category_name":"Movies"}]`))
		case "get_series_categories":
			_, _ = w.Write([]byte(`[{"category_id":"t","category_name":"TV"}]`))
		case "get_vod_streams":
			_, _ = w.Write([]byte(`[{"stream_id":"movie-1","name":"Movie","category_id":"m","container_extension":"mkv"}]`))
		case "get_series":
			_, _ = w.Write([]byte(`[]`))
		case "get_vod_info":
			<-r.Context().Done()
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: provider.URL, Username: "user", Password: "secret"}
	config.Libraries = librariesConfig{MovieCategoryIDs: []string{"m"}, TVCategoryIDs: []string{"t"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	snapshot, _, err := (&catalogRuntime{}).fetch(ctx, config)
	if err == nil || len(snapshot.Movies) != 0 {
		t.Fatalf("deadline returned promotable partial snapshot movies=%d err=%v", len(snapshot.Movies), err)
	}
}

func TestExcessiveDetailFailuresRejectOnlySystemicFailures(t *testing.T) {
	for _, test := range []struct {
		failures int
		total    int
		want     bool
	}{{1, 5, false}, {10, 20, false}, {11, 100, false}, {21, 100, true}, {100, 100, true}} {
		if got := excessiveDetailFailures(test.failures, test.total); got != test.want {
			t.Fatalf("excessiveDetailFailures(%d,%d)=%t want %t", test.failures, test.total, got, test.want)
		}
	}
}

func TestRealTVCategoryTargets(t *testing.T) {
	want := []catalogCategory{{ID: "887", Name: "BET"}, {ID: "926", Name: "A&E"}, {ID: "779", Name: "REALITY"}}
	if !reflect.DeepEqual(realTVCategoryTargets, want) {
		t.Fatalf("TV category targets=%#v want=%#v", realTVCategoryTargets, want)
	}
}

func TestSelectCategoryByIDAndName(t *testing.T) {
	categories := []xtream.Category{{ID: "887", Name: "BET"}, {ID: "926", Name: "A&E"}, {ID: "779", Name: "REALITY"}}
	selected, err := selectCategoryByIDAndName(categories, "926", "A&E")
	if err != nil || selected.ID != "926" || selected.Name != "A&E" {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
	if _, err := selectCategoryByIDAndName(categories, "779", "A&E"); err == nil {
		t.Fatal("mismatched category ID/name was accepted")
	}
}

func TestSnapshotTVCategoriesSupportsLegacyAndExpandedSnapshots(t *testing.T) {
	legacy := canonicalSnapshot{TVCategory: catalogCategory{ID: "887", Name: "BET", Count: 34}}
	if got := snapshotTVCategories(legacy); !reflect.DeepEqual(got, []catalogCategory{legacy.TVCategory}) {
		t.Fatalf("legacy categories=%#v", got)
	}
	expanded := canonicalSnapshot{TVCategory: legacy.TVCategory, TVCategories: []catalogCategory{{ID: "887", Name: "BET", Count: 34}, {ID: "926", Name: "A&E", Count: 10}, {ID: "779", Name: "REALITY", Count: 5}}}
	if got := snapshotTVCategories(expanded); !reflect.DeepEqual(got, expanded.TVCategories) {
		t.Fatalf("expanded categories=%#v", got)
	}
}

func TestSnapshotMovieCategoriesSupportsLegacyAndExpandedSnapshots(t *testing.T) {
	legacy := canonicalSnapshot{MovieCategory: catalogCategory{ID: "917", Name: "Black Cinema", Count: 55}}
	if got := snapshotMovieCategories(legacy); !reflect.DeepEqual(got, []catalogCategory{legacy.MovieCategory}) {
		t.Fatalf("legacy movie categories=%#v", got)
	}
	expanded := canonicalSnapshot{MovieCategory: legacy.MovieCategory, MovieCategories: []catalogCategory{{ID: "917", Name: "Black Cinema", Count: 55}, {ID: "913", Name: "Drama", Count: 80}}}
	if got := snapshotMovieCategories(expanded); !reflect.DeepEqual(got, expanded.MovieCategories) {
		t.Fatalf("expanded movie categories=%#v", got)
	}
}

func TestMergeMediaFactsAcrossExpandedTVCategories(t *testing.T) {
	movieFacts := canonical.MediaFacts{State: "retryable"}
	episodeFacts := canonical.MediaFacts{State: "inspected", Container: "mkv"}
	previous := canonicalSnapshot{
		Movies: []canonicalMovie{{SourceID: "movie-1", MediaFacts: movieFacts}},
		Shows:  []canonicalShow{{SourceID: "bet-show", Category: catalogCategory{ID: "887", Name: "BET"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-1", MediaFacts: episodeFacts}}}}}},
	}
	next := canonicalSnapshot{
		Movies: []canonicalMovie{{SourceID: "movie-1"}},
		Shows: []canonicalShow{
			{SourceID: "bet-show", Category: catalogCategory{ID: "887", Name: "BET"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-1"}}}}},
			{SourceID: "ae-show", Category: catalogCategory{ID: "926", Name: "A&E"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-new"}}}}},
			{SourceID: "reality-show", Category: catalogCategory{ID: "779", Name: "REALITY"}, Seasons: []canonicalSeason{{Number: 1, Episodes: []canonicalEpisode{{SourceID: "episode-reality"}}}}},
		},
	}
	mergeMediaFacts(&next, previous)
	if !reflect.DeepEqual(next.Movies[0].MediaFacts, movieFacts) || !reflect.DeepEqual(next.Shows[0].Seasons[0].Episodes[0].MediaFacts, episodeFacts) {
		t.Fatal("existing MediaFacts were not preserved")
	}
	if next.Shows[1].Seasons[0].Episodes[0].MediaFacts.State != "" {
		t.Fatal("new A&E episode unexpectedly acquired MediaFacts")
	}
	if next.Shows[2].Seasons[0].Episodes[0].MediaFacts.State != "" {
		t.Fatal("new REALITY episode unexpectedly acquired MediaFacts")
	}
}
