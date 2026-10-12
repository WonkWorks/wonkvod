package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func pagingTestHandler(t *testing.T) *realCatalogHandler {
	t.Helper()
	handler, _, _ := realCatalogTestHandler(t)
	snapshot := indexedTestSnapshot(6, 5, 1)
	handler.catalog.mu.Lock()
	handler.catalog.snapshot = snapshot
	handler.catalog.indexes = buildCatalogIndexes(snapshot)
	handler.catalog.mu.Unlock()
	return handler
}

func performPagedRequest(handler http.Handler, target, start, size string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Accept", "application/json")
	if start != "" {
		request.Header.Set(plexContainerStart, start)
	}
	if size != "" {
		request.Header.Set(plexContainerSize, size)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertPage(t *testing.T, container map[string]any, size, total, offset, items int, itemField string) {
	t.Helper()
	if container["size"] != float64(size) || container["totalSize"] != float64(total) || container["offset"] != float64(offset) {
		t.Fatalf("page fields=%#v want size=%d totalSize=%d offset=%d", container, size, total, offset)
	}
	entries, ok := container[itemField].([]any)
	if !ok || len(entries) != items {
		t.Fatalf("%s=%#v want %d entries", itemField, container[itemField], items)
	}
}

func TestMovieListPagingFromQueryAndHeaders(t *testing.T) {
	handler := pagingTestHandler(t)

	query := decodeContainer(t, performRealRequest(handler, http.MethodGet,
		"/library/sections/1/all?type=1&X-Plex-Container-Start=1&X-Plex-Container-Size=2", "application/json", nil))
	assertPage(t, query, 2, 6, 1, 2, "Metadata")

	header := decodeContainer(t, performPagedRequest(handler, "/library/sections/1/all?type=1", "2", "3"))
	assertPage(t, header, 3, 6, 2, 3, "Metadata")

	outOfRange := decodeContainer(t, performRealRequest(handler, http.MethodGet,
		"/library/sections/1/all?type=1&X-Plex-Container-Start=99&X-Plex-Container-Size=2", "application/json", nil))
	assertPage(t, outOfRange, 0, 6, 99, 0, "Metadata")
}

func TestTVListPagingFromQueryAndHeaders(t *testing.T) {
	handler := pagingTestHandler(t)

	query := decodeContainer(t, performRealRequest(handler, http.MethodGet,
		"/library/sections/2/all?type=2&X-Plex-Container-Start=0&X-Plex-Container-Size=2", "application/json", nil))
	assertPage(t, query, 2, 5, 0, 2, "Metadata")

	header := decodeContainer(t, performPagedRequest(handler, "/library/sections/2/all?type=2", "3", "2"))
	assertPage(t, header, 2, 5, 3, 2, "Metadata")
}

func TestCategoryPagingFromQueryAndHeaders(t *testing.T) {
	handler := pagingTestHandler(t)

	movies := decodeContainer(t, performRealRequest(handler, http.MethodGet,
		"/library/sections/1/categories?X-Plex-Container-Start=1&X-Plex-Container-Size=1", "application/json", nil))
	assertPage(t, movies, 1, 3, 1, 1, "Directory")

	movieHeaders := decodeContainer(t, performPagedRequest(handler, "/library/sections/1/categories", "0", "2"))
	assertPage(t, movieHeaders, 2, 3, 0, 2, "Directory")

	tv := decodeContainer(t, performPagedRequest(handler, "/library/sections/2/categories", "1", "1"))
	assertPage(t, tv, 1, 2, 1, 1, "Directory")
}

func TestQueryPagingWinsOverHeaders(t *testing.T) {
	handler := pagingTestHandler(t)
	response := performPagedRequest(handler,
		"/library/sections/1/all?X-Plex-Container-Start=2&X-Plex-Container-Size=1", "0", "4")
	container := decodeContainer(t, response)
	assertPage(t, container, 1, 6, 2, 1, "Metadata")
}

func TestFilteredPagingUsesFilteredTotalAndStableSort(t *testing.T) {
	handler := pagingTestHandler(t)
	genre := genreFacetID("comedy")
	container := decodeContainer(t, performRealRequest(handler, http.MethodGet,
		"/library/sections/1/all?type=1&genre="+genre+"&sort=addedAt:desc&X-Plex-Container-Start=1&X-Plex-Container-Size=2",
		"application/json", nil))
	assertPage(t, container, 2, 3, 1, 2, "Metadata")
	metadata := container["Metadata"].([]any)
	if metadata[0].(map[string]any)["title"] != "Movie 0002" || metadata[1].(map[string]any)["title"] != "Movie 0000" {
		t.Fatalf("unexpected stable reverse page: %#v", metadata)
	}
}

func TestUnpagedAndMalformedPagingPreserveUnpagedShape(t *testing.T) {
	handler := pagingTestHandler(t)
	for _, response := range []*httptest.ResponseRecorder{
		performRealRequest(handler, http.MethodGet, "/library/sections/1/all?type=1", "application/json", nil),
		performPagedRequest(handler, "/library/sections/1/all?type=1", "bad", "2"),
		performRealRequest(handler, http.MethodGet, "/library/sections/1/categories", "application/json", nil),
	} {
		container := decodeContainer(t, response)
		if _, present := container["totalSize"]; present {
			t.Fatalf("unpaged response acquired totalSize: %#v", container)
		}
		if _, present := container["offset"]; present {
			t.Fatalf("unpaged response acquired offset: %#v", container)
		}
	}
}
