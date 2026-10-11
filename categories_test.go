package main

import (
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	canonical "wonkvod/internal/catalog"
)

func categoryTestHandler(t *testing.T) (*realCatalogHandler, *canonicalSnapshot) {
	t.Helper()
	handler, _, _ := realCatalogTestHandler(t)
	handler.catalog.mu.Lock()
	snapshot := handler.catalog.snapshot
	snapshot.Movies[0].Details.Genres = []string{"Drama", " comedy ", "DRAMA"}
	snapshot.Movies = append(snapshot.Movies, canonicalMovie{
		SourceID: "movie-2", Category: snapshot.MovieCategory, Title: "Action Fixture", Year: "2022", Extension: "mp4", Revision: "1700000003",
		Details:    canonical.MovieDetails{ID: "movie-2", Title: "Action Fixture", Genres: []string{"Action", "Comedy"}, DurationMillis: 90000, Extension: "mp4"},
		MediaFacts: fixtureMediaFacts("mp4", 90000, 1000),
	})
	snapshot.Shows[0].Details.Genres = []string{"Drama", " reality ", "DRAMA"}
	snapshot.Shows = append(snapshot.Shows, canonicalShow{
		SourceID: "show-2", Category: catalogCategory{ID: "926", Name: "A&E", Count: 1}, Title: "Comedy Fixture", Year: "2021", Revision: "1700000004",
		Details: canonical.SeriesDetails{ID: "show-2", Title: "Comedy Fixture", Genres: []string{"Comedy", "Reality"}},
	})
	handler.catalog.snapshot = snapshot
	handler.catalog.mu.Unlock()
	return handler, &snapshot
}

func categoryDirectoriesFromResponse(t *testing.T, handler http.Handler, target string) (map[string]any, []map[string]any) {
	t.Helper()
	container := decodeContainer(t, performRealRequest(handler, http.MethodGet, target, "application/json", nil))
	raw, _ := container["Directory"].([]any)
	directories := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		directories = append(directories, item.(map[string]any))
	}
	return container, directories
}

func categoryTitles(directories []map[string]any) []string {
	result := make([]string, 0, len(directories))
	for _, directory := range directories {
		result = append(result, directory["title"].(string))
	}
	return result
}

func metadataTitles(container map[string]any) []string {
	raw := container["Metadata"].([]any)
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		result = append(result, item.(map[string]any)["title"].(string))
	}
	sort.Strings(result)
	return result
}

func TestRealMovieCategoriesAndGenreFiltering(t *testing.T) {
	handler, snapshot := categoryTestHandler(t)
	states := []string{snapshot.Movies[0].MediaFacts.State, snapshot.Movies[1].MediaFacts.State}
	container, directories := categoryDirectoriesFromResponse(t, handler, "/library/sections/1/categories")

	if container["identifier"] != "com.plexapp.plugins.library" || container["content"] != "secondary" || container["title1"] != libraryName || container["title2"] != "" || container["viewGroup"] != "secondary" || container["size"] != float64(4) {
		t.Fatalf("movie category container=%#v", container)
	}
	meta := container["Meta"].(map[string]any)["Type"].([]any)[0].(map[string]any)
	if meta["key"] != "/library/sections/1/categories" || meta["type"] != "directory" || meta["title"] != "Categories" || meta["active"] != true {
		t.Fatalf("movie category Meta=%#v", meta)
	}
	if got, want := categoryTitles(directories), []string{"Action", "Black Cinema", "comedy", "Drama"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("movie category titles=%#v want=%#v", got, want)
	}

	wantTitles := map[string][]string{
		"Action":       {"Action Fixture"},
		"Black Cinema": {"Action Fixture", "Fixture Movie"},
		"comedy":       {"Action Fixture", "Fixture Movie"},
		"Drama":        {"Fixture Movie"},
	}
	for _, directory := range directories {
		key := directory["key"].(string)
		if directory["type"] != "directory" || !strings.HasPrefix(directory["thumb"].(string), "/photo/:/transcode?") {
			t.Fatalf("movie category directory=%#v", directory)
		}
		filtered := decodeContainer(t, performRealRequest(handler, http.MethodGet, key, "application/json", nil))
		if got, want := metadataTitles(filtered), wantTitles[directory["title"].(string)]; !reflect.DeepEqual(got, want) {
			t.Fatalf("filter %q titles=%#v want=%#v", key, got, want)
		}
	}

	unknown := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/sections/1/all?genre=not-a-real-category", "application/json", nil))
	if unknown["size"] != float64(0) || len(unknown["Metadata"].([]any)) != 0 {
		t.Fatalf("unknown movie category=%#v", unknown)
	}
	after := handler.catalog.snapshotCopy()
	if got := []string{after.Movies[0].MediaFacts.State, after.Movies[1].MediaFacts.State}; !reflect.DeepEqual(got, states) {
		t.Fatalf("category browse changed MediaFacts states from %#v to %#v", states, got)
	}
}

func TestRealTVCategoriesAndGenreFiltering(t *testing.T) {
	handler, _ := categoryTestHandler(t)
	container, directories := categoryDirectoriesFromResponse(t, handler, "/library/sections/2/categories")

	if container["identifier"] != "com.plexapp.plugins.library" || container["content"] != "secondary" || container["title1"] != tvLibraryName || container["title2"] != "" || container["nocache"] != true || container["size"] != float64(5) {
		t.Fatalf("TV category container=%#v", container)
	}
	if got, want := categoryTitles(directories), []string{"A&E", "BET", "Comedy", "Drama", "reality"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TV category titles=%#v want=%#v", got, want)
	}
	wantTitles := map[string][]string{
		"A&E":     {"Comedy Fixture"},
		"BET":     {"Fixture Show"},
		"Comedy":  {"Comedy Fixture"},
		"Drama":   {"Fixture Show"},
		"reality": {"Comedy Fixture", "Fixture Show"},
	}
	for _, directory := range directories {
		key := directory["key"].(string)
		filtered := decodeContainer(t, performRealRequest(handler, http.MethodGet, key+"&type=4", "application/json", nil))
		if got, want := metadataTitles(filtered), wantTitles[directory["title"].(string)]; !reflect.DeepEqual(got, want) {
			t.Fatalf("TV filter %q titles=%#v want=%#v", key, got, want)
		}
		for _, item := range filtered["Metadata"].([]any) {
			if item.(map[string]any)["type"] != "show" {
				t.Fatalf("TV category returned non-show: %#v", item)
			}
		}
	}
	unknown := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/sections/2/all?genre=not-a-real-category", "application/json", nil))
	if unknown["size"] != float64(0) || len(unknown["Metadata"].([]any)) != 0 {
		t.Fatalf("unknown TV category=%#v", unknown)
	}
}

func TestRealCategoryXMLLegacyGenreAndEmptyLibrary(t *testing.T) {
	handler, _ := categoryTestHandler(t)
	for _, target := range []string{"/library/sections/1/categories", "/library/sections/2/categories"} {
		response := performRealRequest(handler, http.MethodGet, target, "text/xml", nil)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/xml;charset=utf-8" || !strings.Contains(response.Body.String(), `content="secondary"`) || !strings.Contains(response.Body.String(), `<Directory`) {
			t.Fatalf("category XML %q status=%d type=%q body=%q", target, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}

	legacy, directories := categoryDirectoriesFromResponse(t, handler, "/library/sections/1/genre")
	if legacy["title2"] != "By Genre" || legacy["Meta"] != nil || len(directories) != 4 {
		t.Fatalf("legacy genre container=%#v", legacy)
	}
	for _, directory := range directories {
		if directory["type"] != "genre" || directory["key"] == "" || !strings.HasPrefix(directory["fastKey"].(string), "/library/sections/1/all?genre=") {
			t.Fatalf("legacy genre directory=%#v", directory)
		}
	}

	handler.catalog.mu.Lock()
	handler.catalog.snapshot.Movies = nil
	handler.catalog.mu.Unlock()
	empty := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/sections/1/categories", "application/json", nil))
	if empty["size"] != float64(0) {
		t.Fatalf("empty category size=%#v", empty["size"])
	}
	if _, present := empty["Directory"]; present {
		t.Fatalf("empty real-PMS-shaped category response unexpectedly has Directory: %#v", empty)
	}
}

func TestRealTVCategoriesFallBackToCanonicalSourceMembership(t *testing.T) {
	handler, _ := categoryTestHandler(t)
	handler.catalog.mu.Lock()
	for index := range handler.catalog.snapshot.Shows {
		handler.catalog.snapshot.Shows[index].Details.Genres = nil
	}
	handler.catalog.mu.Unlock()

	_, directories := categoryDirectoriesFromResponse(t, handler, "/library/sections/2/categories")
	if got, want := categoryTitles(directories), []string{"A&E", "BET"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("TV category-membership fallback titles=%#v want=%#v", got, want)
	}
	for _, directory := range directories {
		filtered := decodeContainer(t, performRealRequest(handler, http.MethodGet, directory["key"].(string), "application/json", nil))
		if filtered["size"] != float64(1) {
			t.Fatalf("TV category-membership filter %q=%#v", directory["title"], filtered)
		}
	}
}

func TestGenreFacetIDsAreStableAndOpaque(t *testing.T) {
	first := genreFacetID("science fiction")
	if first == "" || first != genreFacetID("science fiction") {
		t.Fatalf("unstable genre ID %q", first)
	}
	if _, err := url.QueryUnescape(first); err != nil {
		t.Fatalf("genre ID is not safe in a query: %v", err)
	}
}
