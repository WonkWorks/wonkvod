package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type searchHubSpec struct {
	Title string
	Type  string
}

var plexSearchHubs = []searchHubSpec{
	{Title: "Shows", Type: "show"},
	{Title: "Artists", Type: "artist"},
	{Title: "Albums", Type: "album"},
	{Title: "Episodes", Type: "episode"},
	{Title: "Movies", Type: "movie"},
	{Title: "Tracks", Type: "track"},
	{Title: "Photo Albums", Type: "photoalbum"},
	{Title: "Automatic Tags", Type: "autotag"},
	{Title: "Photos", Type: "photo"},
	{Title: "Tags", Type: "tag"},
	{Title: "Actors", Type: "actor"},
	{Title: "Directors", Type: "director"},
	{Title: "Genres", Type: "genre"},
	{Title: "Collections", Type: "collection"},
	{Title: "Playlists", Type: "playlist"},
	{Title: "Shared", Type: "shared"},
	{Title: "Places", Type: "place"},
}

func searchScore(rank int) float64 {
	return []float64{0.99, 0.89, 0.79, 0.69}[rank]
}

func searchScoreString(rank int) string {
	return fmt.Sprintf("%.5f", searchScore(rank))
}

func searchLimit(r *http.Request) int {
	const defaultLimit = 10
	value := strings.TrimSpace(r.URL.Query().Get("limit"))
	if value == "" {
		return defaultLimit
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 0 {
		return defaultLimit
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func windowSearchResults(results []catalogSearchResult, page containerPaging) ([]catalogSearchResult, int, int) {
	total := len(results)
	if !page.Requested {
		return results, total, 0
	}
	startValue, sizeValue := page.windowValues()
	start, end := catalogWindow(startValue, sizeValue, total)
	return results[start:end], total, start
}

func limitSearchResults(results []catalogSearchResult, limit int) []catalogSearchResult {
	if limit < len(results) {
		return results[:limit]
	}
	return results
}

func (h *realCatalogHandler) searchMovieModel(item canonicalMovie, scope string, rank int) movie {
	result := h.movieModel(item, false, false, 0)
	switch scope {
	case "hub":
		result.LibrarySectionID = 1
		result.LibrarySectionKey = "/library/sections/1"
		result.LibrarySectionTitle = libraryName
		result.Score = searchScoreString(rank)
	case "root":
		result.LibrarySectionID = 1
		result.LibrarySectionTitle = libraryName
		result.LibrarySectionUUID = libraryUUID
		result.SourceTitle = "WonkVOD"
		result.AllowSync = truePointer()
		result.Personal = falsePointer()
	case "universal":
		result.LibrarySectionTitle = libraryName
	}
	return result
}

func (h *realCatalogHandler) searchShowModel(item canonicalShow, scope string, rank int) tvItem {
	result := h.showModel(item, false)
	switch scope {
	case "hub":
		result.LibrarySectionID = 2
		result.LibrarySectionKey = "/library/sections/2"
		result.LibrarySectionTitle = tvLibraryName
		result.Score = searchScoreString(rank)
	case "root":
		result.LibrarySectionID = 2
		result.LibrarySectionTitle = tvLibraryName
		result.LibrarySectionUUID = tvLibraryUUID
		result.SourceTitle = "WonkVOD"
		result.AllowSync = truePointer()
		result.Personal = falsePointer()
	case "universal":
		result.LibrarySectionID = 2
		result.LibrarySectionKey = "/library/sections/2"
		result.LibrarySectionTitle = tvLibraryName
	}
	return result
}

func (h *realCatalogHandler) writeSearchHubs(w http.ResponseWriter, r *http.Request) {
	limit := searchLimit(r)
	movies := h.catalog.search(r.URL.Query().Get("query"), catalogMovie)
	shows := h.catalog.search(r.URL.Query().Get("query"), catalogShow)

	type populatedHub struct {
		spec searchHubSpec
		rank int
		hub  map[string]any
	}
	populated := make([]populatedHub, 0, 2)
	addHub := func(spec searchHubSpec, results []catalogSearchResult) {
		if len(results) == 0 {
			return
		}
		limited := limitSearchResults(results, limit)
		metadata := make([]any, 0, len(limited))
		for _, result := range limited {
			if result.Kind == catalogMovie {
				metadata = append(metadata, h.searchMovieModel(result.Movie, "hub", result.Rank))
			} else {
				metadata = append(metadata, h.searchShowModel(result.Show, "hub", result.Rank))
			}
		}
		populated = append(populated, populatedHub{spec: spec, rank: results[0].Rank, hub: map[string]any{
			"title": spec.Title, "type": spec.Type, "hubIdentifier": spec.Type, "context": "",
			"size": len(metadata), "more": len(results) > len(metadata), "style": "shelf", "Metadata": metadata,
		}})
	}
	addHub(searchHubSpec{Title: "Movies", Type: "movie"}, movies)
	addHub(searchHubSpec{Title: "Shows", Type: "show"}, shows)
	sort.SliceStable(populated, func(left, right int) bool {
		if populated[left].rank != populated[right].rank {
			return populated[left].rank < populated[right].rank
		}
		return populated[left].spec.Type == "movie"
	})

	hubs := make([]any, 0, len(plexSearchHubs))
	emitted := make(map[string]bool, len(populated))
	for _, value := range populated {
		hubs = append(hubs, value.hub)
		emitted[value.spec.Type] = true
	}
	for _, spec := range plexSearchHubs {
		if emitted[spec.Type] {
			continue
		}
		hubs = append(hubs, map[string]any{
			"title": spec.Title, "type": spec.Type, "hubIdentifier": spec.Type, "context": "",
			"size": 0, "more": false, "style": "shelf",
		})
	}
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": len(hubs), "Hub": hubs}})
}

func (h *realCatalogHandler) writeSectionSearch(w http.ResponseWriter, r *http.Request, section int) {
	page := requestContainerPaging(r)
	kind := catalogMovie
	if section == 2 {
		kind = catalogShow
	}
	results, total, offset := windowSearchResults(h.catalog.search(r.URL.Query().Get("query"), kind), page)
	query := r.URL.Query().Get("query")
	if section == 1 {
		metadata := make([]movie, 0, len(results))
		for _, result := range results {
			metadata = append(metadata, h.movieModel(result.Movie, false, false, 0))
		}
		container := mediaContainer{Size: len(metadata), AllowSync: false, Identifier: "com.plexapp.plugins.library", MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Art: "/:/resources/movie-fanart.jpg", Thumb: "/:/resources/movie.png", Title1: libraryName, Title2: "Search for '" + query + "'", ViewGroup: "movie", ViewMode: 65592, Metadata: metadata}
		if page.Requested {
			container.TotalSize = intPointer(total)
			container.Offset = intPointer(offset)
		}
		writeNegotiatedMediaContainer(w, r, container)
		return
	}
	metadata := make([]tvItem, 0, len(results))
	for _, result := range results {
		metadata = append(metadata, h.showModel(result.Show, false))
	}
	container := tvMediaContainer{Size: len(metadata), AllowSync: false, Identifier: "com.plexapp.plugins.library", MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Art: "/:/resources/show-fanart.jpg", Thumb: "/:/resources/show.png", Title1: tvLibraryName, Title2: "Search for '" + query + "'", ViewGroup: "show", ViewMode: 65592, Nocache: truePointer(), Metadata: metadata}
	if page.Requested {
		container.TotalSize = intPointer(total)
		container.Offset = intPointer(offset)
	}
	writeNegotiatedTVContainer(w, r, container)
}

func (h *realCatalogHandler) writeUniversalSearch(w http.ResponseWriter, r *http.Request) {
	page := requestContainerPaging(r)
	results, total, offset := windowSearchResults(h.catalog.search(r.URL.Query().Get("query"), 0), page)
	values := make([]any, 0, len(results))
	for _, result := range results {
		var metadata any
		if result.Kind == catalogMovie {
			metadata = h.searchMovieModel(result.Movie, "universal", result.Rank)
		} else {
			metadata = h.searchShowModel(result.Show, "universal", result.Rank)
		}
		values = append(values, map[string]any{"score": searchScore(result.Rank), "Metadata": metadata})
	}
	container := map[string]any{"size": len(values), "SearchResult": values}
	if page.Requested {
		container["totalSize"] = total
		container["offset"] = offset
	}
	writeJSON(w, map[string]any{"MediaContainer": container})
}

func (h *realCatalogHandler) writeLegacySearch(w http.ResponseWriter, r *http.Request) {
	kind := catalogObjectKind(0)
	switch r.URL.Query().Get("type") {
	case "1":
		kind = catalogMovie
	case "2":
		kind = catalogShow
	case "":
	default:
		kind = catalogSeason // unsupported by the v1 title-search scope
	}
	results := limitSearchResults(h.catalog.search(r.URL.Query().Get("query"), kind), searchLimit(r))
	metadata := make([]any, 0, len(results))
	for _, result := range results {
		if result.Kind == catalogMovie {
			metadata = append(metadata, h.searchMovieModel(result.Movie, "root", result.Rank))
		} else {
			metadata = append(metadata, h.searchShowModel(result.Show, "root", result.Rank))
		}
	}
	providers := []any{
		map[string]any{"key": "/system/search", "m_title": "Local Network", "m_type": "mixed"},
		map[string]any{"key": "/search?type=10", "m_title": "Tracks", "m_type": "tracks"},
		map[string]any{"key": "/search/actor", "m_title": "Actors", "m_type": "actors"},
		map[string]any{"key": "/search/director", "m_title": "Directors", "m_type": "directors"},
	}
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": len(metadata) + len(providers), "identifier": "com.plexapp.plugins.library",
		"mediaTagPrefix": "/system/bundle/media/flags/", "mediaTagVersion": 1,
		"Metadata": metadata, "Provider": providers,
	}})
}
