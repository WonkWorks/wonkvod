package main

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	canonical "wonkvod/internal/catalog"
)

func firstCharacterTestHandler(t *testing.T) *realCatalogHandler {
	t.Helper()
	handler, snapshot, _ := realCatalogTestHandler(t)
	movieCategory := snapshot.MovieCategory
	tvCategory := snapshot.TVCategory
	handler.catalog.mu.Lock()
	handler.catalog.snapshot.Movies = []canonicalMovie{
		{SourceID: "movie-a1", Category: movieCategory, Categories: []canonical.Category{{ID: movieCategory.ID, Name: movieCategory.Name}, {ID: movieCategory.ID, Name: movieCategory.Name}}, Title: "Avengers"},
		{SourceID: "movie-a2", Category: movieCategory, Title: "apple"},
		{SourceID: "movie-b", Category: movieCategory, Title: "bravo"},
		{SourceID: "movie-z", Category: movieCategory, Title: "Zebra"},
		{SourceID: "movie-number", Category: movieCategory, Title: "2001: A Space Odyssey"},
		{SourceID: "movie-symbol", Category: movieCategory, Title: "#Alive"},
		{SourceID: "movie-empty", Category: movieCategory, Title: ""},
	}
	handler.catalog.snapshot.Shows = []canonicalShow{
		{SourceID: "show-a", Category: tvCategory, Title: "alpha"},
		{SourceID: "show-f", Category: tvCategory, Categories: []canonical.Category{{ID: tvCategory.ID, Name: tvCategory.Name}, {ID: tvCategory.ID, Name: tvCategory.Name}}, Title: "Fixture Show"},
		{SourceID: "show-t", Category: tvCategory, Title: "the Matrix"},
		{SourceID: "show-number", Category: tvCategory, Title: "2001"},
	}
	handler.catalog.indexes = nil
	handler.catalog.mu.Unlock()
	return handler
}

func decodeFirstCharacterDirectories(t *testing.T, handler http.Handler, target string) (map[string]any, []map[string]any) {
	t.Helper()
	container := decodeContainer(t, performRealRequest(handler, http.MethodGet, target, "application/json", nil))
	raw, _ := container["Directory"].([]any)
	directories := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		directories = append(directories, value.(map[string]any))
	}
	return container, directories
}

func TestMovieFirstCharacterBuckets(t *testing.T) {
	handler := firstCharacterTestHandler(t)
	container, directories := decodeFirstCharacterDirectories(t, handler, "/library/sections/1/firstCharacter?type=1&includeCollections=1&includeAdvanced=1")
	if container["allowSync"] != false || container["content"] != "secondary" || container["title2"] != "By First Letter" || container["viewGroup"] != "secondary" || container["viewMode"] != float64(65592) {
		t.Fatalf("movie firstCharacter navigation fields=%#v", container)
	}
	if container["librarySectionID"] != float64(1) || container["librarySectionTitle"] != libraryName || container["librarySectionUUID"] != libraryUUID || container["identifier"] != "com.plexapp.plugins.library" {
		t.Fatalf("movie firstCharacter section identity=%#v", container)
	}
	want := []map[string]any{
		{"size": float64(3), "key": "%23", "title": "#"},
		{"size": float64(2), "key": "A", "title": "A"},
		{"size": float64(1), "key": "B", "title": "B"},
		{"size": float64(1), "key": "Z", "title": "Z"},
	}
	if !reflect.DeepEqual(directories, want) || container["size"] != float64(len(want)) {
		t.Fatalf("movie firstCharacter directories=%#v want=%#v", directories, want)
	}
	if _, present := container["Metadata"]; present {
		t.Fatalf("movie firstCharacter unexpectedly emitted Metadata: %#v", container)
	}
}

func TestTVFirstCharacterBucketsAndGenreDeduplication(t *testing.T) {
	handler := firstCharacterTestHandler(t)
	genre := genreFacetID(normalizeGenre(realTVCategoryName))
	container, directories := decodeFirstCharacterDirectories(t, handler, "/library/sections/2/firstCharacter?type=2&genre="+genre+"&includeCollections=1&includeAdvanced=1")
	want := []map[string]any{
		{"size": float64(1), "key": "%23", "title": "#"},
		{"size": float64(1), "key": "A", "title": "A"},
		{"size": float64(1), "key": "F", "title": "F"},
		{"size": float64(1), "key": "T", "title": "T"},
	}
	if !reflect.DeepEqual(directories, want) || container["size"] != float64(len(want)) {
		t.Fatalf("TV firstCharacter directories=%#v want=%#v", directories, want)
	}
	if container["allowSync"] != false || container["content"] != "secondary" || container["title2"] != "By First Letter" || container["viewGroup"] != "secondary" || container["viewMode"] != float64(65592) || container["nocache"] != true {
		t.Fatalf("TV firstCharacter navigation fields=%#v", container)
	}
	if container["librarySectionID"] != float64(2) || container["librarySectionTitle"] != tvLibraryName || container["librarySectionUUID"] != tvLibraryUUID {
		t.Fatalf("TV firstCharacter section identity=%#v", container)
	}
}

func TestFirstCharacterTypeMismatchIsEmptyAndXMLIsNegotiated(t *testing.T) {
	handler := firstCharacterTestHandler(t)
	container, directories := decodeFirstCharacterDirectories(t, handler, "/library/sections/1/firstCharacter?type=2")
	if container["size"] != float64(0) || len(directories) != 0 {
		t.Fatalf("mismatched movie type returned buckets: %#v", container)
	}

	response := performRealRequest(handler, http.MethodGet, "/library/sections/1/firstCharacter?type=1", "text/xml", nil)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/xml;charset=utf-8" || !strings.Contains(response.Body.String(), `allowSync="0"`) || !strings.Contains(response.Body.String(), `<Directory size="3" key="%23" title="#"></Directory>`) {
		t.Fatalf("firstCharacter XML status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}
