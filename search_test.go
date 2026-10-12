package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

func searchTestSnapshot() canonicalSnapshot {
	snapshot := indexedTestSnapshot(0, 0, 0)
	for index, title := range []string{"Matrix", "Matrix Reloaded", "The Matrix", "Cinematrix"} {
		snapshot.Movies = append(snapshot.Movies, canonicalMovie{
			SourceID: "search-movie-" + strconv.Itoa(index), Title: title, Year: "2024", Extension: "mkv",
			Details: canonical.MovieDetails{Title: title}, MediaFacts: canonical.MediaFacts{State: "unknown"},
		})
	}
	snapshot.Shows = []canonicalShow{{SourceID: "search-show-1", Title: "Matrix", Year: "2024", Details: canonical.SeriesDetails{Title: "Matrix"}}}
	return snapshot
}

func TestCatalogTitleSearchRankingAndCase(t *testing.T) {
	snapshot := searchTestSnapshot()
	runtime := &catalogRuntime{snapshot: snapshot, indexes: buildCatalogIndexes(snapshot)}
	results := runtime.search("mAtRiX", 0)
	if len(results) != 5 {
		t.Fatalf("results=%d, want 5", len(results))
	}
	wantRanks := []int{0, 0, 1, 2, 3}
	wantTitles := []string{"Matrix", "Matrix", "Matrix Reloaded", "The Matrix", "Cinematrix"}
	for index, result := range results {
		title := result.Movie.Title
		if result.Kind == catalogShow {
			title = result.Show.Title
		}
		if result.Rank != wantRanks[index] || title != wantTitles[index] {
			t.Fatalf("result[%d]=kind %d rank %d title %q, want rank %d title %q", index, result.Kind, result.Rank, title, wantRanks[index], wantTitles[index])
		}
	}
	if got := runtime.search("RELOADED", catalogMovie); len(got) != 1 || got[0].Movie.Title != "Matrix Reloaded" || got[0].Rank != 2 {
		t.Fatalf("word-prefix result=%#v", got)
	}
	if got := runtime.search("inema", catalogMovie); len(got) != 1 || got[0].Movie.Title != "Cinematrix" || got[0].Rank != 3 {
		t.Fatalf("substring result=%#v", got)
	}
	if got := runtime.search("no such title", 0); len(got) != 0 {
		t.Fatalf("no-result query returned %#v", got)
	}
}

func BenchmarkCatalogTitleSearchCurrentScale(b *testing.B) {
	snapshot := indexedTestSnapshot(20_000, 2_500, 0)
	runtime := &catalogRuntime{snapshot: snapshot, indexes: buildCatalogIndexes(snapshot)}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if results := runtime.search("Movie 19999", 0); len(results) != 1 {
			b.Fatalf("results=%d, want 1", len(results))
		}
	}
}

func TestSearchUsesOnlyImmutableCatalogState(t *testing.T) {
	var providerCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "unexpected provider request", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := xtream.NewClient(server.Client(), server.URL, "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := searchTestSnapshot()
	before := snapshot.Movies[0].MediaFacts
	runtime := &catalogRuntime{client: client, snapshot: snapshot, indexes: buildCatalogIndexes(snapshot)}
	if got := runtime.search("Matrix", 0); len(got) == 0 {
		t.Fatal("catalog search returned no results")
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls=%d, want zero", providerCalls.Load())
	}
	if !reflect.DeepEqual(before, runtime.snapshot.Movies[0].MediaFacts) || runtime.inspectionsHasEntries() {
		t.Fatal("search changed MediaFacts or triggered inspection work")
	}
}

func (c *catalogRuntime) inspectionsHasEntries() bool {
	found := false
	c.inspections.Range(func(_, _ any) bool { found = true; return false })
	c.backgroundInspections.Range(func(_, _ any) bool { found = true; return false })
	return found
}

func TestSearchIndexSwapsWithSnapshotGeneration(t *testing.T) {
	first := canonicalSnapshot{Movies: []canonicalMovie{{SourceID: "old", Title: "Old Title"}}}
	next := canonicalSnapshot{Movies: []canonicalMovie{{SourceID: "new", Title: "New Title"}}}
	runtime := &catalogRuntime{snapshot: first, indexes: buildCatalogIndexes(first)}
	if len(runtime.search("Old Title", 0)) != 1 {
		t.Fatal("old index was not searchable")
	}
	runtime.mu.Lock()
	runtime.snapshot = next
	runtime.indexes = buildCatalogIndexes(next)
	runtime.mu.Unlock()
	if len(runtime.search("Old Title", 0)) != 0 || len(runtime.search("New Title", 0)) != 1 {
		t.Fatal("search index did not swap with snapshot")
	}
}

func TestRealSearchRoutesAndStableProjections(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	movieID := movieObjectID(snapshot.Movies[0])
	showID := showObjectID(snapshot.Shows[0])
	secondMovie := snapshot.Movies[0]
	secondMovie.SourceID = "movie-2"
	secondMovie.Title = "Fixture Movie Two"
	handler.catalog.mu.Lock()
	handler.catalog.snapshot.Movies = append(handler.catalog.snapshot.Movies, secondMovie)
	handler.catalog.indexes = buildCatalogIndexes(handler.catalog.snapshot)
	handler.catalog.mu.Unlock()

	hubs := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/hubs/search?query=fixture&limit=1", "application/json", nil))
	if hubs["size"] != float64(17) || len(hubs["Hub"].([]any)) != 17 {
		t.Fatalf("search hubs=%#v", hubs)
	}
	populated := 0
	for _, raw := range hubs["Hub"].([]any) {
		hub := raw.(map[string]any)
		if hub["size"] == float64(0) {
			continue
		}
		populated++
		if hub["type"] == "movie" && hub["more"] != true {
			t.Fatalf("limited movie hub did not advertise more results: %#v", hub)
		}
		item := hub["Metadata"].([]any)[0].(map[string]any)
		if item["ratingKey"] != movieID && item["ratingKey"] != showID {
			t.Fatalf("search returned unstable key %#v", item)
		}
	}
	if populated != 2 {
		t.Fatalf("populated hubs=%d, want movie and show", populated)
	}

	sectionRequest := httptest.NewRequest(http.MethodGet, "/library/sections/1/search?query=fixture&X-Plex-Container-Start=0&X-Plex-Container-Size=1", nil)
	sectionRequest.Header.Set("Accept", "application/json")
	sectionRequest.Header.Set(plexContainerStart, "99")
	sectionRequest.Header.Set(plexContainerSize, "0")
	sectionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sectionRecorder, sectionRequest)
	section := decodeContainer(t, sectionRecorder)
	if section["size"] != float64(1) || section["totalSize"] != float64(2) || section["offset"] != float64(0) {
		t.Fatalf("section search paging=%#v", section)
	}
	if got := section["Metadata"].([]any)[0].(map[string]any)["ratingKey"]; got != movieID {
		t.Fatalf("section search ratingKey=%v, want %s", got, movieID)
	}

	universal := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/search?query=fixture&X-Plex-Container-Start=2&X-Plex-Container-Size=1", "application/json", nil))
	if universal["size"] != float64(1) || universal["totalSize"] != float64(3) || universal["offset"] != float64(2) {
		t.Fatalf("universal search paging=%#v", universal)
	}
	wrapped := universal["SearchResult"].([]any)[0].(map[string]any)
	if wrapped["Metadata"].(map[string]any)["ratingKey"] != showID {
		t.Fatalf("universal stable show key=%#v", wrapped)
	}

	legacy := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/search?query=fixture&limit=1", "application/json", nil))
	if legacy["size"] != float64(5) || len(legacy["Metadata"].([]any)) != 1 || len(legacy["Provider"].([]any)) != 4 {
		t.Fatalf("legacy search=%#v", legacy)
	}

	none := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/search?query=missing", "application/json", nil))
	if none["size"] != float64(0) || len(none["SearchResult"].([]any)) != 0 {
		t.Fatalf("no-result search=%#v", none)
	}
}

func TestSectionSearchNegotiatesXML(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	response := performRealRequest(handler, http.MethodGet, "/library/sections/2/search?query=fixture", "text/xml", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "xml") || !strings.Contains(response.Body.String(), `title="Fixture Show"`) {
		t.Fatalf("XML section search status=%d type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestSearchResultJSONShape(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	response := performRealRequest(handler, http.MethodGet, "/library/search?query=fixture", "application/json", nil)
	var document struct {
		MediaContainer struct {
			SearchResult []struct {
				Score    float64         `json:"score"`
				Metadata json.RawMessage `json:"Metadata"`
			} `json:"SearchResult"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.MediaContainer.SearchResult) != 2 || document.MediaContainer.SearchResult[0].Score <= 0 || len(document.MediaContainer.SearchResult[0].Metadata) == 0 {
		t.Fatalf("bad SearchResult wrapper: %#v", document)
	}
}
