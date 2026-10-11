package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

type realCatalogHandler struct {
	catalog     *catalogRuntime
	passthrough http.Handler
	upstreamURL string
	httpClient  *http.Client
	queueID     atomic.Int64
	mediaID     atomic.Uint64
	queuesMu    sync.RWMutex
	queues      map[int64]virtualPlayQueue
	mediaCtx    sync.Map
}

type virtualPlayQueue struct {
	SelectedKey string
	ContextKey  string
}

func newRealCatalogHandler(upstream string) (http.Handler, error) {
	passthrough, err := pmsPassthroughHandler(upstream)
	if err != nil {
		return nil, err
	}
	catalog, err := newCatalogRuntime()
	if err != nil {
		return nil, err
	}
	handler := &realCatalogHandler{
		catalog: catalog, passthrough: passthrough, upstreamURL: upstream,
		httpClient: &http.Client{Timeout: 45 * time.Second}, queues: make(map[int64]virtualPlayQueue),
	}
	handler.queueID.Store(1000)
	return handler, nil
}

func (h *realCatalogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r, profile := startCatalogRequestProfile(r)
	defer profile.finish()
	if r.Method == http.MethodOptions {
		addCommonHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	requestPath := r.URL.Path
	switch {
	case requestPath == "/media/providers" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeProviders(w, r)
	case requestPath == "/library/sections" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeSections(w)
	case requestPath == "/library/sections/1" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeSection(w, 1)
	case requestPath == "/library/sections/2" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeSection(w, 2)
	case (requestPath == "/library/sections/1/prefs" || requestPath == "/library/sections/2/prefs") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if strings.Contains(requestPath, "/2/") {
			writeTVLibraryPrefs(w)
		} else {
			writeLibrarySectionPrefs(w)
		}
	case requestPath == "/library/sections/1/all" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeMovieList(w, r)
	case requestPath == "/library/sections/2/all" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeTVList(w, r)
	case requestPath == "/library/sections/1/categories" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeCategories(w, r, 1, false)
	case requestPath == "/library/sections/2/categories" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeCategories(w, r, 2, false)
	case requestPath == "/library/sections/1/genre" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeCategories(w, r, 1, true)
	case requestPath == "/library/sections/2/genre" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeCategories(w, r, 2, true)
	case strings.HasPrefix(requestPath, "/library/sections/1/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeEmptyNavigation(w, 1)
	case strings.HasPrefix(requestPath, "/library/sections/2/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeEmptyNavigation(w, 2)
	case requestPath == "/hubs" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeHomeHubs(w, r)
	case requestPath == "/hubs/promoted" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writePromotedHubs(w, r)
	case requestPath == "/hubs/sections/1" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeSectionHubs(w, r, 1)
	case requestPath == "/hubs/sections/2" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.writeSectionHubs(w, r, 2)
	case strings.HasPrefix(requestPath, "/library/metadata/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if h.serveMetadata(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case strings.HasPrefix(requestPath, "/library/parts/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if h.servePart(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case strings.HasPrefix(requestPath, "/library/parts/") && r.Method == http.MethodPut:
		if h.acknowledgePart(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case requestPath == "/photo/:/transcode" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if h.servePhoto(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case requestPath == "/playQueues" && r.Method == http.MethodPost:
		if h.createPlayQueue(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case strings.HasPrefix(requestPath, "/playQueues/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if h.readPlayQueue(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case requestPath == "/video/:/transcode/universal/decision" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if h.writeDecision(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	case requestPath == "/:/timeline" || requestPath == "/:/scrobble" || requestPath == "/:/unscrobble":
		if h.acknowledgeVirtualState(w, r) {
			return
		}
		h.passthrough.ServeHTTP(w, r)
	default:
		h.passthrough.ServeHTTP(w, r)
	}
}

func (h *realCatalogHandler) writeProviders(w http.ResponseWriter, r *http.Request) {
	includePreferences := r.URL.Query().Get("includePreferences") == "1"
	document := map[string]any{"MediaContainer": map[string]any{
		"size": 1, "apiVersion": apiVersion, "friendlyName": "WonkVOD", "machineIdentifier": machineID,
		"version": version, "platform": "Linux", "myPlex": true, "myPlexSigninState": "ok",
		"MediaProvider": []any{map[string]any{
			"identifier": "com.plexapp.plugins.library", "title": "Library", "types": "video", "protocols": "stream",
			"Feature": []any{
				map[string]any{"key": "/library/sections", "type": "content", "Directory": []any{
					map[string]any{"hubKey": "/hubs", "title": "Home"},
					h.realLibraryDirectory(1, includePreferences), h.realLibraryDirectory(2, includePreferences),
				}},
				map[string]any{"key": "/library/metadata", "type": "metadata"},
				map[string]any{"key": "/hubs/promoted", "type": "promoted"},
				map[string]any{"flavor": "universal", "key": "/playQueues", "type": "playqueue"},
				map[string]any{"key": "/:/timeline", "type": "timeline", "scrobbleKey": "/:/scrobble", "unscrobbleKey": "/:/unscrobble"},
			},
		}},
	}}
	writeJSON(w, document)
}

func (h *realCatalogHandler) realLibraryDirectory(section int, includePreferences bool) map[string]any {
	isTV := section == 2
	name, uuid, libraryType, agent, scanner, typeNumber := libraryName, libraryUUID, "movie", "tv.plex.agents.movie", "Plex Movie", 1
	settings := libraryPreferences()
	if isTV {
		name, uuid, libraryType, agent, scanner, typeNumber = tvLibraryName, tvLibraryUUID, "show", "tv.plex.agents.series", "Plex TV Series", 2
		settings = tvLibraryPreferences()
	}
	id := strconv.Itoa(section)
	directory := map[string]any{
		"agent": agent, "scanner": scanner, "language": "en-US", "refreshing": false,
		"uuid": uuid, "id": id, "key": "/library/sections/" + id, "hubKey": "/hubs/sections/" + id,
		"type": libraryType, "title": name, "updatedAt": libraryUpdatedAt, "scannedAt": libraryUpdatedAt,
		"Pivot": []any{
			map[string]any{"id": "recommended", "key": "/hubs/sections/" + id, "type": "hub", "title": "Recommended", "context": "content.discover", "symbol": "star"},
			map[string]any{"id": "library", "key": fmt.Sprintf("/library/sections/%s/all?type=%d", id, typeNumber), "type": "list", "title": "Library", "context": "content.library", "symbol": "library"},
			map[string]any{"id": "categories", "key": "/library/sections/" + id + "/categories", "type": "list", "title": "Categories", "context": "content.categories", "symbol": "stack"},
		},
	}
	if includePreferences {
		directory["Preferences"] = map[string]any{"Setting": settings}
	}
	return directory
}

func (h *realCatalogHandler) writeSections(w http.ResponseWriter) {
	snapshot := h.catalog.snapshotCopy()
	now := snapshot.GeneratedAt.Unix()
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": 2, "allowSync": false, "title1": "Plex Library", "Directory": []any{
			map[string]any{"agent": "tv.plex.agents.movie", "allowSync": true, "content": true, "createdAt": now, "directory": true, "filters": true, "hidden": 0, "key": "1", "language": "en-US", "metadataAgentProviderGroupId": 1, "refreshing": false, "scannedAt": now, "scanner": "Plex Movie", "title": libraryName, "type": "movie", "updatedAt": now, "uuid": libraryUUID},
			map[string]any{"agent": "tv.plex.agents.series", "allowSync": true, "content": true, "createdAt": now, "directory": true, "filters": true, "hidden": 0, "key": "2", "language": "en-US", "metadataAgentProviderGroupId": 2, "refreshing": false, "scannedAt": now, "scanner": "Plex TV Series", "title": tvLibraryName, "type": "show", "updatedAt": now, "uuid": tvLibraryUUID},
		},
	}})
}

func (h *realCatalogHandler) writeSection(w http.ResponseWriter, section int) {
	if section == 1 {
		writeLibrarySection(w)
		return
	}
	writeTVLibrarySection(w)
}

func reverseRequested(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.URL.Query().Get("sort")), ":desc")
}

func itemTimestamp(revision string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(revision), 10, 64)
	if err == nil && value > 0 {
		return value
	}
	return libraryUpdatedAt
}

func (h *realCatalogHandler) writeMovieList(w http.ResponseWriter, r *http.Request) {
	items, _ := h.catalog.movieWindow(r.URL.Query().Get("genre"), reverseRequested(r), r.URL.Query().Get("X-Plex-Container-Start"), r.URL.Query().Get("X-Plex-Container-Size"))
	catalogProfileMark(r, "indexed_selection")
	metadata := make([]movie, 0, len(items))
	for _, item := range items {
		metadata = append(metadata, h.movieModel(item, false, false, 0))
	}
	catalogProfileMark(r, "metadata_assembly")
	container := mediaContainer{Size: len(metadata), AllowSync: true, Identifier: "com.plexapp.plugins.library", LibrarySectionID: 1, LibrarySectionTitle: libraryName, LibrarySectionUUID: libraryUUID, MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Art: "/:/resources/movie-fanart.jpg", Thumb: "/:/resources/movie.png", Title1: libraryName, Title2: "All " + libraryName, ViewGroup: "movie", ViewMode: 65592, Metadata: metadata}
	writeNegotiatedMediaContainer(w, r, container)
	catalogProfileMark(r, "serialization")
}

func (h *realCatalogHandler) writeTVList(w http.ResponseWriter, r *http.Request) {
	genre := r.URL.Query().Get("genre")
	if genre == "" && r.URL.Query().Get("type") == "4" {
		playables, _ := h.catalog.episodeWindow(r.URL.Query().Get("X-Plex-Container-Start"), r.URL.Query().Get("X-Plex-Container-Size"))
		catalogProfileMark(r, "indexed_selection")
		episodes := make([]tvItem, 0, len(playables))
		for _, item := range playables {
			episodes = append(episodes, h.episodeModel(*item.Show, *item.Season, *item.Episode, false, false, false, 0))
		}
		container := h.realTVContainer(episodes)
		container.Title1 = tvLibraryName
		container.Title2 = "All Episodes"
		container.ViewGroup = "episode"
		container.ViewMode = 65592
		writeNegotiatedTVContainer(w, r, container)
		catalogProfileMark(r, "serialization")
		return
	}
	items, _ := h.catalog.showWindow(genre, reverseRequested(r), r.URL.Query().Get("X-Plex-Container-Start"), r.URL.Query().Get("X-Plex-Container-Size"))
	catalogProfileMark(r, "indexed_selection")
	metadata := make([]tvItem, 0, len(items))
	for _, item := range items {
		metadata = append(metadata, h.showModel(item, false))
	}
	container := h.realTVContainer(metadata)
	container.Art = "/:/resources/show-fanart.jpg"
	container.Thumb = "/:/resources/show.png"
	container.Title1 = tvLibraryName
	container.Title2 = "All Shows"
	container.ViewGroup = "show"
	container.ViewMode = 65592
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
	catalogProfileMark(r, "serialization")
}

func (h *realCatalogHandler) realTVContainer(metadata []tvItem) tvMediaContainer {
	return tvMediaContainer{Size: len(metadata), AllowSync: true, Identifier: "com.plexapp.plugins.library", LibrarySectionID: 2, LibrarySectionTitle: tvLibraryName, LibrarySectionUUID: tvLibraryUUID, MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Metadata: metadata}
}

func (h *realCatalogHandler) serveMetadata(w http.ResponseWriter, r *http.Request) bool {
	trimmed := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 {
		return false
	}
	key := parts[0]
	locator, item, show, season, episode, found := h.catalog.metadataAt(key)
	catalogProfileMark(r, "rating_key_lookup")
	if !found {
		return false
	}
	switch locator.kind {
	case catalogMovie:
		if len(parts) > 1 {
			if parts[1] == "thumb" {
				return h.proxyImage(w, r, item.Details.Poster)
			}
			if parts[1] == "art" {
				return h.proxyImage(w, r, item.Details.Fanart)
			}
			if parts[1] == "related" || parts[1] == "userState" {
				writeEmptyContainer(w)
				return true
			}
			return false
		}
		playable := catalogPlayable{Kind: "movie", RatingKey: key, SourceID: item.SourceID, Extension: item.Extension, Title: item.Title, Duration: int(item.Details.DurationMillis), Bitrate: item.Details.Bitrate, MediaFacts: item.MediaFacts, Movie: &item}
		size, available := h.detailCapability(r, playable)
		h.catalog.scheduleMediaInspection(playable, int64(size))
		catalogProfileMark(r, "availability_and_inspection_schedule")
		container := mediaContainer{Size: 1, AllowSync: true, Identifier: "com.plexapp.plugins.library", LibrarySectionID: 1, LibrarySectionTitle: libraryName, LibrarySectionUUID: libraryUUID, MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, Metadata: []movie{h.movieModel(item, true, available, size)}}
		writeNegotiatedMediaContainer(w, r, container)
		catalogProfileMark(r, "serialization")
		return true
	case catalogShow:
		if len(parts) > 1 {
			if parts[1] == "children" {
				h.writeSeasons(w, r, show)
				return true
			}
			if parts[1] == "thumb" {
				return h.proxyImage(w, r, show.Details.Poster)
			}
			if parts[1] == "art" {
				return h.proxyImage(w, r, show.Details.Fanart)
			}
			if parts[1] == "related" || parts[1] == "userState" {
				writeEmptyContainer(w)
				return true
			}
			return false
		}
		container := h.realTVContainer([]tvItem{h.showModel(show, true)})
		writeNegotiatedTVContainer(w, r, container)
		catalogProfileMark(r, "serialization")
		return true
	case catalogSeason:
		if len(parts) > 1 {
			if parts[1] == "children" {
				h.writeEpisodes(w, r, show, season)
				return true
			}
			if parts[1] == "thumb" {
				return h.proxyImage(w, r, firstNonEmptyString(season.Poster, show.Details.Poster))
			}
			if parts[1] == "art" {
				return h.proxyImage(w, r, show.Details.Fanart)
			}
			if parts[1] == "related" || parts[1] == "userState" {
				writeEmptyContainer(w)
				return true
			}
			return false
		}
		container := h.realTVContainer([]tvItem{h.seasonModel(show, season, true)})
		writeNegotiatedTVContainer(w, r, container)
		catalogProfileMark(r, "serialization")
		return true
	case catalogEpisode:
		if len(parts) > 1 {
			if parts[1] == "thumb" || parts[1] == "art" {
				return h.proxyImage(w, r, firstNonEmptyString(episode.Details.Artwork, show.Details.Fanart, show.Details.Poster))
			}
			if parts[1] == "related" || parts[1] == "userState" {
				writeEmptyContainer(w)
				return true
			}
			return false
		}
		playable := catalogPlayable{Kind: "episode", RatingKey: key, SourceID: episode.SourceID, Extension: episode.Extension, Title: episode.Details.Title, Duration: int(episode.Details.DurationMillis), Bitrate: episode.Details.Bitrate, MediaFacts: episode.MediaFacts, Show: &show, Season: &season, Episode: &episode}
		size, available := h.detailCapability(r, playable)
		h.catalog.scheduleMediaInspection(playable, int64(size))
		catalogProfileMark(r, "availability_and_inspection_schedule")
		container := h.realTVContainer([]tvItem{h.episodeModel(show, season, episode, true, r.URL.Query().Get("includeExtras") == "1", available, size)})
		writeNegotiatedTVContainer(w, r, container)
		catalogProfileMark(r, "serialization")
		return true
	}
	return false
}

func (h *realCatalogHandler) detailCapability(r *http.Request, item catalogPlayable) (int, bool) {
	if r.URL.Query().Get("checkFiles") != "1" {
		return 0, false
	}
	size := item.MediaFacts.Size
	h.catalog.mu.RLock()
	capability, cached := h.catalog.capabilities[item.Kind+":"+item.SourceID]
	h.catalog.mu.RUnlock()
	if cached {
		size = capability.Size
	}
	if size > math.MaxInt {
		return 0, true
	}
	return int(size), true
}

func (h *realCatalogHandler) lazyMediaFacts(parent context.Context, item catalogPlayable, size int64) canonical.MediaFacts {
	if item.MediaFacts.State == "inspected" {
		return item.MediaFacts
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	facts, err := h.catalog.inspectMedia(ctx, item, size)
	if err != nil {
		log.Printf("lazy media inspection failed kind=%q source_id=%q state=%q", item.Kind, item.SourceID, facts.State)
		if item.MediaFacts.State == "inspected" {
			return item.MediaFacts
		}
	}
	return facts
}

func (h *realCatalogHandler) writeSeasons(w http.ResponseWriter, r *http.Request, show canonicalShow) {
	items := make([]tvItem, 0, len(show.Seasons))
	for _, season := range show.Seasons {
		items = append(items, h.seasonModel(show, season, false))
	}
	container := h.realTVContainer(items)
	container.Key = showObjectID(show)
	container.Art = h.artPath(showObjectID(show))
	container.Title1 = tvLibraryName
	container.Title2 = show.Title
	container.ViewGroup = "season"
	container.ViewMode = 65593
	container.ParentTitle = show.Title
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
}
func (h *realCatalogHandler) writeEpisodes(w http.ResponseWriter, r *http.Request, show canonicalShow, season canonicalSeason) {
	items := make([]tvItem, 0, len(season.Episodes))
	for _, episode := range season.Episodes {
		items = append(items, h.episodeModel(show, season, episode, false, false, false, 0))
	}
	container := h.realTVContainer(items)
	container.Key = seasonObjectID(show, season)
	container.Art = h.artPath(showObjectID(show))
	container.Title1 = show.Title
	container.Title2 = fmt.Sprintf("Season %d", season.Number)
	container.ViewGroup = "episode"
	container.ViewMode = 65592
	container.ParentIndex = season.Number
	container.GrandparentRatingKey = parsePositiveInt(showObjectID(show))
	container.GrandparentTitle = show.Title
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
}

func (h *realCatalogHandler) movieModel(item canonicalMovie, detail, available bool, size int) movie {
	key := movieObjectID(item)
	duration := int(item.Details.DurationMillis)
	if duration <= 0 {
		duration = item.Details.Runtime * 60 * 1000
	}
	if item.MediaFacts.State == "inspected" && item.MediaFacts.DurationMS > 0 {
		duration = int(item.MediaFacts.DurationMS)
	}
	year := parsePositiveInt(item.Year)
	rating, _ := strconv.ParseFloat(item.Details.Rating, 64)
	timestamp := itemTimestamp(item.Revision)
	result := movie{RatingKey: key, Key: "/library/metadata/" + key, GUID: "local://" + key, Type: "movie", Title: item.Title, TitleSort: item.Title, Slug: slugify(item.Title, item.Year), Thumb: h.thumbPath(key), Art: h.artPath(key), Summary: item.Details.Plot, Year: year, Duration: duration, AddedAt: timestamp, UpdatedAt: timestamp, LibrarySectionID: 1, LibrarySectionKey: "/library/sections/1", LibrarySectionTitle: libraryName, LibrarySectionUUID: libraryUUID, OriginallyAvailableAt: item.Details.Premiere, Rating: rating, Media: []media{h.mediaModel("movie", item.SourceID, item.Extension, duration, item.Details.Bitrate, size, detail, available, item.MediaFacts)}}
	if item.Details.TMDBID != "" {
		result.Guids = []plexGuid{{ID: "tmdb://" + item.Details.TMDBID}}
	}
	result.Images = []plexImage{{Alt: item.Title, Type: "coverPoster", URL: result.Thumb}, {Alt: item.Title, Type: "background", URL: result.Art}}
	if !detail {
		result.LibrarySectionID = 0
		result.LibrarySectionKey = ""
		result.LibrarySectionTitle = ""
		result.LibrarySectionUUID = ""
		result.Guids = nil
		result.Rating = 0
		result.OriginallyAvailableAt = ""
		result.Media[0].Part[0].Stream = nil
	}
	return result
}

func (h *realCatalogHandler) showModel(show canonicalShow, detail bool) tvItem {
	key := showObjectID(show)
	leaf := 0
	for _, s := range show.Seasons {
		leaf += len(s.Episodes)
	}
	item := tvBaseItem()
	item.RatingKey = key
	item.Key = "/library/metadata/" + key + "/children"
	item.GUID = "local://" + key
	item.Type = "show"
	item.Title = show.Title
	item.TitleSort = show.Title
	item.Summary = show.Details.Plot
	item.Thumb = h.thumbPath(key)
	item.Art = h.artPath(key)
	item.ChildCount = len(show.Seasons)
	item.LeafCount = leaf
	item.ViewedLeafCount = intPointer(0)
	item.AddedAt = itemTimestamp(show.Revision)
	item.UpdatedAt = item.AddedAt
	item.LibrarySectionUUID = tvLibraryUUID
	if !detail {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
	}
	return item
}
func (h *realCatalogHandler) seasonModel(show canonicalShow, season canonicalSeason, detail bool) tvItem {
	showKey := showObjectID(show)
	key := seasonObjectID(show, season)
	item := tvBaseItem()
	item.RatingKey = key
	item.Key = "/library/metadata/" + key + "/children"
	item.GUID = "local://" + key
	item.Type = "season"
	item.Title = fmt.Sprintf("Season %d", season.Number)
	item.Index = season.Number
	item.LeafCount = len(season.Episodes)
	item.ViewedLeafCount = intPointer(0)
	item.ParentGUID = "local://" + showKey
	item.ParentRatingKey = showKey
	item.ParentKey = "/library/metadata/" + showKey
	item.ParentTitle = show.Title
	item.Thumb = h.thumbPath(key)
	item.Art = h.artPath(showKey)
	item.AddedAt = itemTimestamp(show.Revision)
	item.UpdatedAt = item.AddedAt
	item.LibrarySectionUUID = tvLibraryUUID
	if !detail {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
	}
	return item
}
func (h *realCatalogHandler) episodeModel(show canonicalShow, season canonicalSeason, episode canonicalEpisode, detail, includeExtras, available bool, size int) tvItem {
	showKey := showObjectID(show)
	seasonKey := seasonObjectID(show, season)
	key := episodeObjectID(episode)
	duration := int(episode.Details.DurationMillis)
	if duration <= 0 {
		duration = episode.Details.Runtime * 60 * 1000
	}
	if episode.MediaFacts.State == "inspected" && episode.MediaFacts.DurationMS > 0 {
		duration = int(episode.MediaFacts.DurationMS)
	}
	item := tvBaseItem()
	item.RatingKey = key
	item.Key = "/library/metadata/" + key
	item.GUID = "local://" + key
	item.Type = "episode"
	item.Title = firstNonEmptyString(episode.Details.Title, fmt.Sprintf("Episode %d", episode.Number))
	item.Summary = episode.Details.Plot
	item.Index = episode.Number
	item.ParentIndex = season.Number
	item.Duration = duration
	item.ParentGUID = "local://" + seasonKey
	item.ParentRatingKey = seasonKey
	item.ParentKey = "/library/metadata/" + seasonKey
	item.ParentTitle = fmt.Sprintf("Season %d", season.Number)
	item.GrandparentGUID = "local://" + showKey
	item.GrandparentRatingKey = showKey
	item.GrandparentKey = "/library/metadata/" + showKey
	item.GrandparentTitle = show.Title
	item.Thumb = h.thumbPath(key)
	item.Art = h.artPath(showKey)
	item.Images = []plexImage{{Alt: item.Title, Type: "snapshot", URL: item.Thumb}}
	item.Media = []media{h.mediaModel("episode", episode.SourceID, episode.Extension, duration, episode.Details.Bitrate, size, detail, available, episode.MediaFacts)}
	item.LibrarySectionUUID = tvLibraryUUID
	item.AddedAt = itemTimestamp(show.Revision)
	item.UpdatedAt = item.AddedAt
	if includeExtras {
		item.Extras = &extras{Size: 0}
	}
	if !detail {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
		item.Media[0].Part[0].Stream = nil
	}
	return item
}

func (h *realCatalogHandler) mediaModel(kind, sourceID, extension string, duration, bitrate, size int, detail, available bool, facts canonical.MediaFacts) media {
	if extension == "" {
		extension = "mkv"
	}
	partID := partObjectID(kind, sourceID)
	partKey := fmt.Sprintf("/library/parts/%d/file.%s", partID, extension)
	p := part{ID: int(partID), Key: partKey, Duration: duration, File: "/virtual/" + kind + "-" + sourceID + "." + extension, Size: size, Container: extension, Has64BitOffsets: false, OptimizedForStreaming: false, VideoProfile: "high"}
	if facts.State == "inspected" {
		if facts.Container != "" {
			extension = facts.Container
			p.Container = facts.Container
		}
		if facts.DurationMS > 0 {
			duration = int(facts.DurationMS)
			p.Duration = duration
		}
		if facts.BitrateKbps > 0 {
			bitrate = facts.BitrateKbps
		}
		if facts.Size > 0 {
			size = int(facts.Size)
			p.Size = size
		}
	}
	if detail && facts.State != "inspected" {
		p.Stream = []stream{{ID: int(videoStreamObjectID(kind, sourceID)), StreamType: 1, Default: true, Codec: "h264", Index: 0, Profile: "high", StreamIdentifier: "1", DisplayTitle: "Video", ExtendedDisplayTitle: "Video (H.264)"}, {ID: int(audioStreamObjectID(kind, sourceID)), StreamType: 2, Selected: true, Default: true, Codec: "aac", Index: 1, Channels: 2, SamplingRate: 48000, StreamIdentifier: "2", DisplayTitle: "Audio", ExtendedDisplayTitle: "Audio (AAC Stereo)"}}
	}
	if available {
		p.Accessible = truePointer()
		p.Exists = truePointer()
	}
	result := media{ID: int(mediaObjectID(kind, sourceID)), Duration: duration, Bitrate: bitrate, Width: 1920, Height: 1080, AspectRatio: 1.78, AudioChannels: 2, AudioCodec: "aac", VideoCodec: "h264", VideoResolution: "1080", Container: extension, VideoFrameRate: "NTSC", OptimizedForStreaming: 0, Has64BitOffsets: false, VideoProfile: "high", HasVoiceActivity: false, Part: []part{p}}
	if facts.State == "inspected" {
		applyMediaFacts(&result, kind, sourceID, facts, detail)
	}
	return result
}

func applyMediaFacts(result *media, kind, sourceID string, facts canonical.MediaFacts, detail bool) {
	if len(facts.Video) > 0 {
		video := facts.Video[0]
		result.VideoCodec, result.VideoProfile = video.Codec, video.Profile
		result.Width, result.Height = video.Width, video.Height
		if video.Width > 0 && video.Height > 0 {
			result.AspectRatio = math.Round(float64(video.Width)/float64(video.Height)*100) / 100
		}
		result.VideoResolution = strconv.Itoa(video.Height)
		if video.FrameRate > 0 {
			result.VideoFrameRate = frameRateLabel(video.FrameRate)
		}
		result.Part[0].VideoProfile = video.Profile
	}
	if len(facts.Audio) > 0 {
		audio := facts.Audio[0]
		result.AudioCodec, result.AudioChannels = audio.Codec, audio.Channels
	}
	if !detail {
		return
	}
	streams := make([]stream, 0, len(facts.Video)+len(facts.Audio)+len(facts.Subtitles))
	for index, value := range facts.Video {
		id := videoStreamObjectID(kind, sourceID)
		if index > 0 {
			id = stableNumericID('7', "xtream:"+kind+":"+sourceID+":video:"+strconv.Itoa(value.Index))
		}
		streams = append(streams, stream{ID: int(id), StreamType: 1, Default: plexBool(value.Default), Codec: value.Codec, Index: value.Index, BitDepth: value.BitDepth, ChromaLocation: value.ChromaLocation, ChromaSubsampling: value.ChromaSubsampling, CodedHeight: value.CodedHeight, CodedWidth: value.CodedWidth, FrameRate: plexFrameRatePointer(value.FrameRate), Height: value.Height, Width: value.Width, Level: value.Level, Profile: value.Profile, RefFrames: value.RefFrames, StreamIdentifier: strconv.Itoa(value.Index + 1), DisplayTitle: strings.ToUpper(value.Codec) + " Video", ExtendedDisplayTitle: strings.ToUpper(value.Codec) + " Video", PixelFormat: value.PixelFormat, ColorSpace: value.ColorSpace, ColorTrc: value.ColorTransfer, ColorPrimaries: value.ColorPrimaries})
	}
	for index, value := range facts.Audio {
		id := audioStreamObjectID(kind, sourceID)
		if index > 0 {
			id = stableNumericID('8', "xtream:"+kind+":"+sourceID+":audio:"+strconv.Itoa(value.Index))
		}
		title := firstNonEmptyString(value.Title, strings.ToUpper(value.Codec)+" Audio")
		streams = append(streams, stream{ID: int(id), StreamType: 2, Selected: plexBool(value.Default), Default: plexBool(value.Default), Codec: value.Codec, Index: value.Index, Bitrate: value.BitrateKbps, Profile: value.Profile, Channels: value.Channels, SamplingRate: value.SampleRate, StreamIdentifier: strconv.Itoa(value.Index + 1), DisplayTitle: title, ExtendedDisplayTitle: title, Language: value.Language, Title: value.Title, ChannelLayout: value.ChannelLayout})
	}
	for _, value := range facts.Subtitles {
		codec := value.Codec
		if codec == "subrip" {
			codec = "srt"
		}
		title := firstNonEmptyString(value.Title, value.Language, "Subtitle")
		streams = append(streams, stream{ID: int(subtitleStreamObjectID(kind, sourceID, value.Index)), StreamType: 3, Selected: plexBool(value.Default), Default: plexBool(value.Default), Forced: plexBool(value.Forced), Codec: codec, Index: value.Index, StreamIdentifier: strconv.Itoa(value.Index + 1), DisplayTitle: title, ExtendedDisplayTitle: title, Language: value.Language, Title: value.Title})
	}
	result.Part[0].Stream = streams
}

func plexFrameRatePointer(value float64) *plexFrameRate {
	if value <= 0 {
		return nil
	}
	rate := plexFrameRate(value)
	return &rate
}

func frameRateLabel(value float64) string {
	if value > 23 && value < 25 {
		return "24p"
	}
	if value > 29 && value < 31 {
		return "NTSC"
	}
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func (h *realCatalogHandler) thumbPath(key string) string {
	return "/library/metadata/" + key + "/thumb/1"
}
func (h *realCatalogHandler) artPath(key string) string { return "/library/metadata/" + key + "/art/1" }
func slugify(title, year string) string {
	value := strings.ToLower(strings.TrimSpace(title + " " + year))
	var b strings.Builder
	dash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (h *realCatalogHandler) playableByPartID(id int64) (catalogPlayable, bool) {
	return h.catalog.playableByPart(id)
}

func (h *realCatalogHandler) servePart(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		return false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return false
	}
	item, ok := h.playableByPartID(id)
	if !ok {
		return false
	}
	requestID := h.mediaID.Add(1)
	startedAt := time.Now()
	contextIdentity := fmt.Sprintf("%p", r.Context())
	priorRequestID, sharedContext := h.mediaCtx.LoadOrStore(contextIdentity, requestID)
	if !sharedContext {
		priorRequestID = nil
	}
	defer h.mediaCtx.Delete(contextIdentity)
	logMediaContext(requestID, "handler_entry", startedAt, r.Context(), contextIdentity, sharedContext, priorRequestID)
	source, err := h.catalog.source(item)
	if err != nil {
		http.Error(w, "media source unavailable", http.StatusBadGateway)
		return true
	}
	requestedRange := r.Header.Get("Range")
	logMediaContext(requestID, "before_upstream_open", startedAt, r.Context(), contextIdentity, sharedContext, priorRequestID)
	resp, err := source.OpenWithTrace(r.Context(), r.Method, requestedRange, func(event xtream.OpenTrace) {
		checkpoint := "after_upstream_attempt"
		if event.RetryDecision != "" {
			checkpoint = "before_retry"
		}
		log.Printf("WONKVOD_MEDIA_TRACE phase=upstream request_id=%d context_checkpoint=%q context_state=%q context_identity=%q elapsed_ms=%.3f source_id=%q part_id=%d method=%q range=%q host=%q source_state=%q attempt=%d upstream_status=%d content_length=%d content_range=%q error_class=%q retry_decision=%q", requestID, checkpoint, requestContextState(r.Context()), contextIdentity, elapsedMilliseconds(startedAt), item.SourceID, id, r.Method, requestedRange, event.Host, event.SourceState, event.Attempt, event.Status, event.ContentLength, event.ContentRange, event.ErrorClass, event.RetryDecision)
	})
	logMediaContext(requestID, "after_upstream_open", startedAt, r.Context(), contextIdentity, sharedContext, priorRequestID)
	if err != nil {
		if r.Context().Err() != nil {
			markTraceClientCanceled(w)
			log.Printf("WONKVOD_MEDIA_TRACE phase=downstream request_id=%d context_state=%q elapsed_ms=%.3f source_id=%q part_id=%d method=%q range=%q status=0 bytes=0 termination=%q", requestID, requestContextState(r.Context()), elapsedMilliseconds(startedAt), item.SourceID, id, r.Method, requestedRange, "current_request_canceled")
			return true
		}
		log.Printf("WONKVOD_MEDIA_TRACE phase=downstream request_id=%d context_state=%q elapsed_ms=%.3f source_id=%q part_id=%d method=%q range=%q status=%d bytes=0 termination=%q", requestID, requestContextState(r.Context()), elapsedMilliseconds(startedAt), item.SourceID, id, r.Method, requestedRange, http.StatusBadGateway, "upstream_open_error")
		http.Error(w, "media source unavailable", http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()
	addCommonHeaders(w)
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := resp.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "video/"+item.Extension)
	}
	downstreamStatus := resp.StatusCode
	w.WriteHeader(downstreamStatus)
	var copied int64
	var copyErr error
	var readTracker mediaReadTracker
	var writeTracker mediaWriteTracker
	if r.Method != http.MethodHead {
		readTracker.reader = resp.Body
		writeTracker.writer = w
		copied, copyErr = io.Copy(&writeTracker, &readTracker)
	}
	log.Printf("WONKVOD_MEDIA_TRACE phase=downstream request_id=%d context_state=%q elapsed_ms=%.3f source_id=%q part_id=%d method=%q range=%q status=%d bytes=%d termination=%q", requestID, requestContextState(r.Context()), elapsedMilliseconds(startedAt), item.SourceID, id, r.Method, requestedRange, downstreamStatus, copied, mediaCopyTermination(r.Context(), readTracker.err, writeTracker.err, copyErr))
	return true
}

func (h *realCatalogHandler) acknowledgePart(w http.ResponseWriter, r *http.Request) bool {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 {
		return false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return false
	}
	if _, ok := h.playableByPartID(id); !ok {
		return false
	}
	writeEmptyContainer(w)
	return true
}

type mediaReadTracker struct {
	reader io.Reader
	err    error
}

func (tracker *mediaReadTracker) Read(buffer []byte) (int, error) {
	count, err := tracker.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		tracker.err = err
	}
	return count, err
}

type mediaWriteTracker struct {
	writer io.Writer
	err    error
}

func (tracker *mediaWriteTracker) Write(buffer []byte) (int, error) {
	count, err := tracker.writer.Write(buffer)
	if err != nil {
		tracker.err = err
	}
	return count, err
}

func mediaCopyTermination(ctx context.Context, readErr, writeErr, copyErr error) string {
	if copyErr == nil {
		return ""
	}
	if errors.Is(writeErr, syscall.EPIPE) {
		return "downstream_broken_pipe"
	}
	if errors.Is(writeErr, syscall.ECONNRESET) {
		return "downstream_connection_reset"
	}
	if writeErr != nil {
		return "downstream_write_error"
	}
	if ctx.Err() != nil {
		return "downstream_client_canceled"
	}
	if readErr != nil {
		return "upstream_read_error"
	}
	return "server_copy_error"
}

func requestContextState(ctx context.Context) string {
	switch ctx.Err() {
	case nil:
		return "live"
	case context.Canceled:
		return "canceled"
	case context.DeadlineExceeded:
		return "deadline_exceeded"
	default:
		return "error"
	}
}

func elapsedMilliseconds(startedAt time.Time) float64 {
	return float64(time.Since(startedAt).Microseconds()) / 1000
}

func logMediaContext(requestID uint64, checkpoint string, startedAt time.Time, ctx context.Context, identity string, shared bool, prior any) {
	log.Printf("WONKVOD_MEDIA_TRACE phase=context request_id=%d context_checkpoint=%q context_state=%q context_identity=%q context_shared=%t prior_request_id=%v elapsed_ms=%.3f", requestID, checkpoint, requestContextState(ctx), identity, shared, prior, elapsedMilliseconds(startedAt))
}

func markTraceClientCanceled(w http.ResponseWriter) {
	if writer, ok := w.(*traceResponseWriter); ok && writer.statusCode == 0 {
		writer.statusCode = 499
	}
}

func (h *realCatalogHandler) artworkURL(metadataPath string) (string, bool) {
	trimmed := strings.TrimPrefix(metadataPath, "/library/metadata/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) < 2 {
		return "", false
	}
	key, kind := parts[0], parts[1]
	locator, movie, show, season, episode, ok := h.catalog.metadataAt(key)
	if !ok {
		return "", false
	}
	switch locator.kind {
	case catalogMovie:
		if kind == "thumb" {
			return movie.Details.Poster, movie.Details.Poster != ""
		}
		return movie.Details.Fanart, movie.Details.Fanart != ""
	case catalogShow:
		if kind == "thumb" {
			return show.Details.Poster, show.Details.Poster != ""
		}
		return show.Details.Fanart, show.Details.Fanart != ""
	case catalogSeason:
		return firstNonEmptyString(season.Poster, show.Details.Poster), true
	case catalogEpisode:
		return firstNonEmptyString(episode.Details.Artwork, show.Details.Fanart, show.Details.Poster), true
	}
	return "", false
}
func (h *realCatalogHandler) servePhoto(w http.ResponseWriter, r *http.Request) bool {
	source := r.URL.Query().Get("url")
	if decoded, err := url.QueryUnescape(source); err == nil {
		source = decoded
	}
	target, ok := h.artworkURL(source)
	catalogProfileMark(r, "artwork_lookup")
	if !ok {
		return false
	}
	result := h.proxyImage(w, r, target)
	catalogProfileMark(r, "artwork_upstream")
	return result
}
func (h *realCatalogHandler) proxyImage(w http.ResponseWriter, r *http.Request, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		http.NotFound(w, r)
		return true
	}
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.NotFound(w, r)
		return true
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	resp, err := h.httpClient.Do(request)
	if err != nil {
		http.Error(w, "artwork unavailable", http.StatusBadGateway)
		return true
	}
	defer resp.Body.Close()
	addCommonHeaders(w)
	if value := resp.Header.Get("Content-Type"); strings.HasPrefix(strings.ToLower(value), "image/") {
		w.Header().Set("Content-Type", value)
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	if value := resp.Header.Get("Content-Length"); value != "" {
		w.Header().Set("Content-Length", value)
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 20<<20))
	}
	return true
}

func metadataKeyFromURI(value string) string {
	decoded, _ := url.QueryUnescape(value)
	marker := "/library/metadata/"
	index := strings.LastIndex(decoded, marker)
	if index < 0 {
		return ""
	}
	key := strings.Trim(decoded[index+len(marker):], "/")
	if slash := strings.IndexByte(key, '/'); slash >= 0 {
		key = key[:slash]
	}
	return key
}
func (h *realCatalogHandler) createPlayQueue(w http.ResponseWriter, r *http.Request) bool {
	uriKey := metadataKeyFromURI(r.URL.Query().Get("uri"))
	selectedKey := metadataKeyFromURI(r.URL.Query().Get("key"))
	contextKey := ""
	uriKind, virtualURI := h.catalog.metadataKind(uriKey)
	if virtualURI {
		contextKey = uriKey
	}
	item, selected := h.catalog.playable(selectedKey)
	if !selected && selectedKey == "" && uriKind == "show" {
		item, selected = h.catalog.firstPlayableEpisode(uriKey)
		if !selected {
			writeJSONStatus(w, http.StatusNotFound, map[string]any{"MediaContainer": map[string]any{"size": 0, "playQueueTotalCount": 0, "Metadata": []any{}}})
			return true
		}
	}
	if !selected {
		item, selected = h.catalog.playable(uriKey)
	}
	if !selected {
		return false
	}
	catalogProfileMark(r, "indexed_selection")
	if contextKey == "" {
		contextKey = item.RatingKey
	}
	id := h.queueID.Add(1)
	h.queuesMu.Lock()
	h.queues[id] = virtualPlayQueue{SelectedKey: item.RatingKey, ContextKey: contextKey}
	h.queuesMu.Unlock()
	h.writePlayQueue(w, r, id, item, contextKey)
	catalogProfileMark(r, "serialization")
	return true
}
func (h *realCatalogHandler) readPlayQueue(w http.ResponseWriter, r *http.Request) bool {
	idText := path.Base(r.URL.Path)
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil {
		return false
	}
	h.queuesMu.RLock()
	queue, ok := h.queues[id]
	h.queuesMu.RUnlock()
	if !ok {
		return false
	}
	item, ok := h.catalog.playable(queue.SelectedKey)
	catalogProfileMark(r, "indexed_selection")
	if !ok {
		return false
	}
	h.writePlayQueue(w, r, id, item, queue.ContextKey)
	catalogProfileMark(r, "serialization")
	return true
}
func (h *realCatalogHandler) queueMovie(item catalogPlayable) movie {
	if item.Movie != nil {
		return h.movieModel(*item.Movie, true, true, 0)
	}
	episode := item.Episode
	timestamp := itemTimestamp(item.Show.Revision)
	result := movie{RatingKey: item.RatingKey, Key: "/library/metadata/" + item.RatingKey, GUID: "local://" + item.RatingKey, Type: "episode", Title: item.Title, Slug: slugify(item.Title, ""), Thumb: h.thumbPath(item.RatingKey), Art: h.artPath(showObjectID(*item.Show)), Summary: episode.Details.Plot, Duration: item.Duration, AddedAt: timestamp, UpdatedAt: timestamp, LibrarySectionID: 2, LibrarySectionKey: "/library/sections/2", LibrarySectionTitle: tvLibraryName, LibrarySectionUUID: tvLibraryUUID, Media: []media{h.mediaModel("episode", item.SourceID, item.Extension, item.Duration, item.Bitrate, 0, true, true, item.MediaFacts)}}
	return result
}
func (h *realCatalogHandler) writePlayQueue(w http.ResponseWriter, r *http.Request, id int64, item catalogPlayable, contextKey string) {
	projected := h.queueMovie(item)
	projected.PlayQueueItemID = 1
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": 1, "identifier": "com.plexapp.plugins.library", "playQueueID": id, "playQueueSelectedItemID": 1, "playQueueSelectedItemOffset": 0, "playQueueTotalCount": 1, "playQueueVersion": 1, "playQueueShuffled": false, "playQueueSourceURI": "library://x/item/" + url.QueryEscape("/library/metadata/"+contextKey), "Metadata": []any{projected}}})
}
func (h *realCatalogHandler) writeDecision(w http.ResponseWriter, r *http.Request) bool {
	key := metadataKeyFromURI(r.URL.Query().Get("path"))
	item, ok := h.catalog.playable(key)
	if !ok {
		return false
	}
	item.MediaFacts = h.lazyMediaFacts(r.Context(), item, item.MediaFacts.Size)
	if item.Movie != nil {
		item.Movie.MediaFacts = item.MediaFacts
	}
	if item.Episode != nil {
		item.Episode.MediaFacts = item.MediaFacts
	}
	audioSelection, selectionErr := decisionAudioStream(r, item)
	if selectionErr != nil {
		addCommonHeaders(w)
		http.Error(w, selectionErr.Error(), http.StatusUnprocessableEntity)
		return true
	}
	if reason := unsupportedDirectPlayReason(r, item.MediaFacts, audioSelection); reason != "" {
		addCommonHeaders(w)
		http.Error(w, reason, http.StatusUnprocessableEntity)
		return true
	}
	projected := h.queueMovie(item)
	projected.Media[0].Selected = truePointer()
	projected.Media[0].Part[0].Decision = "directplay"
	projected.Media[0].Part[0].Selected = truePointer()
	for index := range projected.Media[0].Part[0].Stream {
		projected.Media[0].Part[0].Stream[index].Location = "direct"
		if projected.Media[0].Part[0].Stream[index].StreamType == 2 && audioSelection.Found {
			projected.Media[0].Part[0].Stream[index].Selected = plexBool(projected.Media[0].Part[0].Stream[index].ID == audioSelection.PlexID)
		}
	}
	session := firstNonEmptyString(r.URL.Query().Get("X-Plex-Session-Identifier"), r.URL.Query().Get("session"))
	container := mediaContainer{Size: 1, AllowSync: true, Identifier: "com.plexapp.plugins.library", LibrarySectionID: 1, LibrarySectionTitle: libraryName, LibrarySectionUUID: libraryUUID, MDEDecisionCode: 1000, MDEDecisionText: "Direct play OK.", MediaTagPrefix: "/system/bundle/media/flags/", MediaTagVersion: 1, ResourceSession: session, Metadata: []movie{projected}}
	if item.Kind == "episode" {
		container.LibrarySectionID = 2
		container.LibrarySectionTitle = tvLibraryName
		container.LibrarySectionUUID = tvLibraryUUID
	}
	writeNegotiatedMediaContainer(w, r, container)
	return true
}

type selectedAudioStream struct {
	Facts  canonical.AudioStream
	PlexID int
	Found  bool
}

func decisionAudioStream(r *http.Request, item catalogPlayable) (selectedAudioStream, error) {
	audio := item.MediaFacts.Audio
	if len(audio) == 0 {
		return selectedAudioStream{}, nil
	}
	plexID := func(index int) int {
		id := audioStreamObjectID(item.Kind, item.SourceID)
		if index > 0 {
			id = stableNumericID('8', "xtream:"+item.Kind+":"+item.SourceID+":audio:"+strconv.Itoa(audio[index].Index))
		}
		return int(id)
	}

	if value := strings.TrimSpace(r.URL.Query().Get("audioStreamID")); value != "" {
		requested, err := strconv.Atoi(value)
		if err != nil {
			return selectedAudioStream{}, fmt.Errorf("invalid audioStreamID %q", value)
		}
		for index, stream := range audio {
			if id := plexID(index); id == requested {
				return selectedAudioStream{Facts: stream, PlexID: id, Found: true}, nil
			}
		}
		return selectedAudioStream{}, fmt.Errorf("unknown audioStreamID %d", requested)
	}

	defaultIndex := -1
	for index, stream := range audio {
		if !stream.Default {
			continue
		}
		if defaultIndex >= 0 {
			return selectedAudioStream{}, errors.New("ambiguous audio selection: multiple default streams")
		}
		defaultIndex = index
	}
	if defaultIndex >= 0 {
		return selectedAudioStream{Facts: audio[defaultIndex], PlexID: plexID(defaultIndex), Found: true}, nil
	}

	// Media.audioCodec/audioChannels are projected from the first canonical
	// audio stream. Use that same stable fallback when ffprobe marks no stream
	// as default, rather than allowing an alternate track to veto playback.
	return selectedAudioStream{Facts: audio[0], PlexID: plexID(0), Found: true}, nil
}

func unsupportedDirectPlayReason(r *http.Request, facts canonical.MediaFacts, audio selectedAudioStream) string {
	platform := firstNonEmptyString(r.Header.Get("X-Plex-Platform"), r.URL.Query().Get("X-Plex-Platform"))
	if !strings.EqualFold(platform, "Roku") || facts.State != "inspected" {
		return ""
	}
	if audio.Found && audio.Facts.Codec == "aac" && audio.Facts.Channels > 2 {
		return "media requires AAC multichannel audio transcoding, which WonkVOD does not provide"
	}
	return ""
}
func (h *realCatalogHandler) acknowledgeVirtualState(w http.ResponseWriter, r *http.Request) bool {
	key := firstNonEmptyString(r.URL.Query().Get("ratingKey"), metadataKeyFromURI(r.URL.Query().Get("key")))
	if _, ok := h.catalog.playable(key); !ok {
		return false
	}
	writeEmptyContainer(w)
	return true
}

func (h *realCatalogHandler) writeEmptyNavigation(w http.ResponseWriter, section int) {
	name, uuid, view := libraryName, libraryUUID, "movie"
	if section == 2 {
		name, uuid, view = tvLibraryName, tvLibraryUUID, "show"
	}
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": 0, "allowSync": true, "identifier": "com.plexapp.plugins.library", "librarySectionID": section, "librarySectionTitle": name, "librarySectionUUID": uuid, "mediaTagPrefix": "/system/bundle/media/flags/", "mediaTagVersion": 1, "viewGroup": view, "Metadata": []any{}}})
}

func (h *realCatalogHandler) movieHubItems(limit int) []any {
	movies := h.catalog.hubMovies(limit)
	items := make([]any, 0, len(movies))
	for _, movie := range movies {
		items = append(items, h.movieModel(movie, false, false, 0))
	}
	return items
}
func (h *realCatalogHandler) showHubItems(limit int) []any {
	shows := h.catalog.hubShows(limit)
	items := make([]any, 0, len(shows))
	for _, show := range shows {
		items = append(items, h.showModel(show, true))
	}
	return items
}
func (h *realCatalogHandler) realMovieHub(home bool) map[string]any {
	items := h.movieHubItems(10)
	identifier, context, title, key := "movie.recentlyadded.1", "hub.movie.recentlyadded", "Recently Added in "+libraryName, "/library/sections/1/all?sort=addedAt:desc"
	if home {
		identifier, context, title, key = "home.movies.recent", "hub.home.movies.recent", "Recently Added Movies", "/hubs/home/recentlyAdded?type=1"
	}
	hub := map[string]any{"context": context, "hubIdentifier": identifier, "key": key, "more": len(items) == 10, "promoted": true, "size": len(items), "style": "shelf", "title": title, "type": "movie", "Metadata": items}
	if len(items) > 0 {
		hub["hubKey"] = "/library/metadata/" + items[0].(movie).RatingKey
	}
	return hub
}
func (h *realCatalogHandler) realTVHub(home bool) map[string]any {
	items := h.showHubItems(10)
	identifier, context, title, key := "tv.recentlyadded.2", "hub.tv.recentlyadded", "Recently Added in "+tvLibraryName, "/library/sections/2/all?type=2&sort=addedAt:desc"
	if home {
		identifier, context, title, key = "home.television.recent", "hub.home.television.recent", "Recently Added TV", "/hubs/home/recentlyAdded?type=2"
	}
	hub := map[string]any{"context": context, "hubIdentifier": identifier, "key": key, "more": len(items) == 10, "promoted": true, "size": len(items), "style": "shelf", "title": title, "type": "show", "Metadata": items}
	if len(items) > 0 {
		hub["hubKey"] = "/library/metadata/" + items[0].(tvItem).RatingKey
	}
	return hub
}
func (h *realCatalogHandler) writeHomeHubs(w http.ResponseWriter, r *http.Request) {
	requested := map[string]bool{}
	for _, value := range strings.Split(r.URL.Query().Get("identifier"), ",") {
		requested[value] = true
	}
	all := len(requested) == 0 || requested[""]
	hubs := []any{}
	if all || requested["home.continue"] {
		hubs = append(hubs, homeContinueHub())
	}
	if all || requested["home.ondeck"] {
		hubs = append(hubs, homeOnDeckHub())
	}
	if all || requested["home.movies.recent"] {
		hubs = append(hubs, h.realMovieHub(true))
	}
	if all || requested["home.television.recent"] {
		hubs = append(hubs, h.realTVHub(true))
	}
	catalogProfileMark(r, "indexed_hub_assembly")
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": len(hubs), "allowSync": true, "identifier": "com.plexapp.plugins.library", "Hub": hubs}})
	catalogProfileMark(r, "serialization")
}
func (h *realCatalogHandler) writePromotedHubs(w http.ResponseWriter, r *http.Request) {
	hubs := []any{homeContinueHub(), homeOnDeckHub(), h.realMovieHub(false), h.realTVHub(false)}
	catalogProfileMark(r, "indexed_hub_assembly")
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": len(hubs), "allowSync": true, "identifier": "com.plexapp.plugins.library", "Hub": hubs}})
	catalogProfileMark(r, "serialization")
}
func (h *realCatalogHandler) writeSectionHubs(w http.ResponseWriter, r *http.Request, section int) {
	var hubs []any
	name, uuid := libraryName, libraryUUID
	if section == 1 {
		hubs = []any{sectionContinueWatchingHub(), h.realMovieHub(false)}
	} else {
		name, uuid = tvLibraryName, tvLibraryUUID
		hubs = []any{map[string]any{"context": "hub.tv.inprogress", "hubIdentifier": "tv.inprogress.2", "key": "/hubs/sections/2/continueWatching/items", "more": false, "size": 0, "style": "shelf", "title": "Continue Watching", "type": "episode"}, h.realTVHub(false)}
	}
	catalogProfileMark(r, "indexed_hub_assembly")
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": len(hubs), "allowSync": true, "identifier": "com.plexapp.plugins.library", "librarySectionID": section, "librarySectionTitle": name, "librarySectionUUID": uuid, "Hub": hubs}})
	catalogProfileMark(r, "serialization")
}
