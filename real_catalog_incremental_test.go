package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"

	canonical "wonkvod/internal/catalog"
)

type incrementalProvider struct {
	mu            sync.Mutex
	movies        map[string][]string
	shows         map[string][]string
	movieRevision map[string]string
	showRevision  map[string]string
	movieName     map[string]string
	showName      map[string]string
	failMovies    map[string]bool
	failShows     map[string]bool
	movieDetails  int
	showDetails   int
}

func newIncrementalProvider() *incrementalProvider {
	return &incrementalProvider{
		movies: map[string][]string{"a": {"m1"}, "b": nil}, shows: map[string][]string{"t": {"s1"}},
		movieRevision: map[string]string{"m1": "100"}, showRevision: map[string]string{"s1": "200"},
		movieName: map[string]string{"m1": "Movie One"}, showName: map[string]string{"s1": "Show One"},
		failMovies: map[string]bool{}, failShows: map[string]bool{},
	}
}

func (p *incrementalProvider) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	category := r.URL.Query().Get("category_id")
	switch r.URL.Query().Get("action") {
	case "get_vod_categories":
		_, _ = w.Write([]byte(`[{"category_id":"a","category_name":"A"},{"category_id":"b","category_name":"B"}]`))
	case "get_series_categories":
		_, _ = w.Write([]byte(`[{"category_id":"t","category_name":"TV"}]`))
	case "get_vod_streams":
		items := make([]map[string]any, 0, len(p.movies[category]))
		for _, id := range p.movies[category] {
			items = append(items, map[string]any{"stream_id": id, "name": p.movieName[id], "year": "2024", "category_id": category, "container_extension": "mkv", "added": p.movieRevision[id], "rating": "8"})
		}
		_ = json.NewEncoder(w).Encode(items)
	case "get_series":
		items := make([]map[string]any, 0, len(p.shows[category]))
		for _, id := range p.shows[category] {
			items = append(items, map[string]any{"series_id": id, "name": p.showName[id], "year": "2023", "category_id": category, "last_modified": p.showRevision[id], "rating": "7"})
		}
		_ = json.NewEncoder(w).Encode(items)
	case "get_vod_info":
		id := r.URL.Query().Get("vod_id")
		p.movieDetails++
		if p.failMovies[id] {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, `{"info":{"name":%q,"year":"2024"},"movie_data":{"stream_id":%q,"container_extension":"mkv"}}`, p.movieName[id], id)
	case "get_series_info":
		id := r.URL.Query().Get("series_id")
		p.showDetails++
		if p.failShows[id] {
			http.Error(w, "failed", http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, `{"info":{"name":%q,"year":"2023"},"seasons":[{"season_number":1}],"episodes":{"1":[{"id":%q,"episode_num":1,"season":1,"title":"Episode","container_extension":"mkv","info":{"duration_secs":60}}]}}`, p.showName[id], id+"e1")
	default:
		http.Error(w, "unexpected", http.StatusBadRequest)
	}
}

func (p *incrementalProvider) resetCounts() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.movieDetails, p.showDetails = 0, 0
}

func incrementalConfig(url string, movieCategories ...string) appConfig {
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: url, Username: "user", Password: "secret"}
	config.Libraries = librariesConfig{MovieCategoryIDs: movieCategories, TVCategoryIDs: []string{"t"}}
	return config
}

func TestIncrementalRefreshReusesUnchangedMoviesAndShows(t *testing.T) {
	provider := newIncrementalProvider()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	runtime := &catalogRuntime{}
	previous, _, _, err := runtime.fetchCatalog(t.Context(), incrementalConfig(server.URL, "a"), canonicalSnapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	previous.Movies[0].MediaFacts = canonical.MediaFacts{State: "inspected", Container: "mkv"}
	previous.Shows[0].Seasons[0].Episodes[0].MediaFacts = canonical.MediaFacts{State: "inspected", Container: "mkv"}
	provider.resetCounts()
	next, _, metrics, err := runtime.fetchCatalog(t.Context(), incrementalConfig(server.URL, "a"), previous, false)
	if err != nil {
		t.Fatal(err)
	}
	if provider.movieDetails != 0 || provider.showDetails != 0 || metrics.ReusedUnchanged != 2 || metrics.DetailRequestsSkipped != 2 {
		t.Fatalf("calls movie/show=%d/%d metrics=%#v", provider.movieDetails, provider.showDetails, metrics)
	}
	if !reflect.DeepEqual(next.Movies[0].MediaFacts, previous.Movies[0].MediaFacts) || !reflect.DeepEqual(next.Shows[0].Seasons[0].Episodes[0].MediaFacts, previous.Shows[0].Seasons[0].Episodes[0].MediaFacts) {
		t.Fatal("reused object lost MediaFacts")
	}
	if movieObjectID(next.Movies[0]) != movieObjectID(previous.Movies[0]) || partObjectID("movie", "m1") != partObjectID("movie", previous.Movies[0].SourceID) {
		t.Fatal("reused object changed stable IDs")
	}
}

func TestIncrementalRefreshClassifiesNewChangedMembershipAndRemoval(t *testing.T) {
	provider := newIncrementalProvider()
	provider.movies["a"] = []string{"m1", "m2", "gone"}
	provider.movies["b"] = []string{"m1"}
	provider.movieRevision["m2"], provider.movieRevision["gone"] = "101", "102"
	provider.movieName["m2"], provider.movieName["gone"] = "Movie Two", "Gone"
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	runtime := &catalogRuntime{}
	config := incrementalConfig(server.URL, "a", "b")
	previous, _, _, err := runtime.fetchCatalog(t.Context(), config, canonicalSnapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	provider.resetCounts()
	provider.movies["a"] = []string{"m1", "m2", "m3"}
	provider.movies["b"] = []string{"m2"}
	provider.movieRevision["m2"], provider.movieRevision["m3"] = "changed", "103"
	provider.movieName["m3"] = "Movie Three"
	next, _, metrics, err := runtime.fetchCatalog(t.Context(), config, previous, false)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.New != 1 || metrics.Changed != 1 || metrics.MembershipOnly != 1 || metrics.Removed != 1 || metrics.DetailRequests != 2 || metrics.DetailRequestsSkipped != 2 {
		t.Fatalf("metrics=%#v", metrics)
	}
	if provider.movieDetails != 2 || provider.showDetails != 0 {
		t.Fatalf("detail calls movie/show=%d/%d", provider.movieDetails, provider.showDetails)
	}
	for _, movie := range next.Movies {
		if movie.SourceID == "gone" {
			t.Fatal("removed movie remained")
		}
		if movie.SourceID == "m1" && len(movie.Categories) != 1 {
			t.Fatalf("membership-only update failed: %#v", movie.Categories)
		}
		if movie.SourceID == "m2" && len(movie.Categories) != 2 {
			t.Fatalf("movie remaining in another category was dropped: %#v", movie.Categories)
		}
	}
}

func TestIncrementalRefreshRetriesQuarantineAndGuardsSystemicFailure(t *testing.T) {
	provider := newIncrementalProvider()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	runtime := &catalogRuntime{}
	config := incrementalConfig(server.URL, "a")
	previous := canonicalSnapshot{Version: realCatalogVersion, Quarantine: []quarantineEntry{{Kind: "movie", SourceID: "m1", Reason: "metadata request failed"}}}
	next, _, metrics, err := runtime.fetchCatalog(t.Context(), config, previous, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Movies) != 1 || len(next.Quarantine) != 0 || metrics.QuarantineRetries != 1 || metrics.QuarantineRecovered != 1 || provider.movieDetails != 1 {
		t.Fatalf("retry result movies=%d quarantine=%d metrics=%#v calls=%d", len(next.Movies), len(next.Quarantine), metrics, provider.movieDetails)
	}

	provider.resetCounts()
	provider.movies["a"] = nil
	for index := 0; index < 11; index++ {
		id := fmt.Sprintf("bad-%02d", index)
		provider.movies["a"] = append(provider.movies["a"], id)
		provider.movieName[id], provider.movieRevision[id], provider.failMovies[id] = id, "1", true
	}
	if _, _, _, err := runtime.fetchCatalog(t.Context(), config, next, false); err == nil {
		t.Fatal("systemic detail failure was promotable")
	}
}

func TestFullRebuildFetchesEveryDetailAndLegacySnapshotMigrates(t *testing.T) {
	provider := newIncrementalProvider()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	runtime := &catalogRuntime{}
	config := incrementalConfig(server.URL, "a")
	previous, _, _, err := runtime.fetchCatalog(t.Context(), config, canonicalSnapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	provider.resetCounts()
	legacy := previous
	legacy.Movies[0].ListFingerprint = ""
	legacy.Movies[0].Title = "Detail-formatted Movie One (2024)"
	legacy.Movies[0].Year = ""
	legacy.Shows[0].ListFingerprint = ""
	if _, _, metrics, err := runtime.fetchCatalog(t.Context(), config, legacy, false); err != nil || metrics.DetailRequests != 0 {
		t.Fatalf("legacy migration fetched details: metrics=%#v err=%v", metrics, err)
	}
	provider.resetCounts()
	if _, _, metrics, err := runtime.fetchCatalog(t.Context(), config, previous, true); err != nil || provider.movieDetails != 1 || provider.showDetails != 1 || metrics.DetailRequests != 2 || metrics.DetailRequestsSkipped != 0 {
		t.Fatalf("full rebuild calls=%d/%d metrics=%#v err=%v", provider.movieDetails, provider.showDetails, metrics, err)
	}
}

func TestFingerprintsExcludeCategoryMembership(t *testing.T) {
	provider := newIncrementalProvider()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	runtime := &catalogRuntime{}
	config := incrementalConfig(server.URL, "a", "b")
	provider.movies["b"] = []string{"m1"}
	first, _, _, err := runtime.fetchCatalog(t.Context(), config, canonicalSnapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	provider.movies["a"] = nil
	provider.resetCounts()
	second, _, metrics, err := runtime.fetchCatalog(t.Context(), config, first, false)
	if err != nil {
		t.Fatal(err)
	}
	if provider.movieDetails != 0 || metrics.MembershipOnly != 1 || len(second.Movies) != 1 || second.Movies[0].ListFingerprint != first.Movies[0].ListFingerprint {
		t.Fatalf("membership changed fingerprint/detail behavior: metrics=%#v", metrics)
	}
}

func TestCatalogOrderingRemainsDeterministic(t *testing.T) {
	provider := newIncrementalProvider()
	provider.movies["a"] = []string{"m2", "m1"}
	provider.movieName["m2"], provider.movieRevision["m2"] = "A Movie", "2"
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	snapshot, _, _, err := (&catalogRuntime{}).fetchCatalog(t.Context(), incrementalConfig(server.URL, "a"), canonicalSnapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	titles := []string{snapshot.Movies[0].Title, snapshot.Movies[1].Title}
	if !sort.StringsAreSorted(titles) {
		t.Fatalf("titles=%v", titles)
	}
}

func TestCatalogBuildModesDistinguishBootstrapIncrementalAndRebuild(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous canonicalSnapshot
		explicit bool
		wantFull bool
		wantMode string
	}{
		{name: "cold bootstrap", wantFull: true, wantMode: "bootstrap"},
		{name: "valid snapshot startup", previous: canonicalSnapshot{Version: realCatalogVersion}, wantMode: "incremental"},
		{name: "explicit rebuild", previous: canonicalSnapshot{Version: realCatalogVersion}, explicit: true, wantFull: true, wantMode: "full_rebuild"},
	} {
		t.Run(test.name, func(t *testing.T) {
			full, mode := catalogBuildMode(test.previous, test.explicit)
			if full != test.wantFull || mode != test.wantMode {
				t.Fatalf("full/mode=%t/%q want %t/%q", full, mode, test.wantFull, test.wantMode)
			}
		})
	}
}
