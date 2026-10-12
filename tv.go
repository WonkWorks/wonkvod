package main

import (
	"encoding/xml"
	"net/http"
)

const (
	tvLibraryID        = "2"
	tvLibraryUUID      = "09e2e2bf-06d5-4fc6-8278-b155eec5d209"
	tvLibraryName      = "WonkVOD TV"
	tvShowRatingKey    = "2"
	tvSeasonRatingKey  = "3"
	tvEpisodeRatingKey = "4"
	tvShowTitle        = "WonkVOD Test Show"
	tvSeasonTitle      = "Season 1"
	tvEpisodeTitle     = "Test Episode"
	tvPartPath         = "/library/parts/2/1790908037/file.mp4"
	tvVirtualFile      = "/virtual/WonkVOD-TV-Test-Episode.mp4"
)

type tvMediaContainer struct {
	XMLName              xml.Name         `json:"-" xml:"MediaContainer"`
	Size                 int              `json:"size" xml:"size,attr"`
	TotalSize            *int             `json:"totalSize,omitempty" xml:"totalSize,attr,omitempty"`
	Offset               *int             `json:"offset,omitempty" xml:"offset,attr,omitempty"`
	AllowSync            plexBool         `json:"allowSync" xml:"allowSync,attr"`
	Identifier           string           `json:"identifier" xml:"identifier,attr"`
	LibrarySectionID     int              `json:"librarySectionID" xml:"librarySectionID,attr"`
	LibrarySectionTitle  string           `json:"librarySectionTitle" xml:"librarySectionTitle,attr"`
	LibrarySectionUUID   string           `json:"librarySectionUUID" xml:"librarySectionUUID,attr"`
	Key                  string           `json:"key,omitempty" xml:"key,attr,omitempty"`
	Art                  string           `json:"art,omitempty" xml:"art,attr,omitempty"`
	Thumb                string           `json:"thumb,omitempty" xml:"thumb,attr,omitempty"`
	Title1               string           `json:"title1,omitempty" xml:"title1,attr,omitempty"`
	Title2               string           `json:"title2,omitempty" xml:"title2,attr,omitempty"`
	ViewGroup            string           `json:"viewGroup,omitempty" xml:"viewGroup,attr,omitempty"`
	ViewMode             int              `json:"viewMode,omitempty" xml:"viewMode,attr,omitempty"`
	ParentIndex          int              `json:"parentIndex,omitempty" xml:"parentIndex,attr,omitempty"`
	ParentTitle          string           `json:"parentTitle,omitempty" xml:"parentTitle,attr,omitempty"`
	GrandparentRatingKey int              `json:"grandparentRatingKey,omitempty" xml:"grandparentRatingKey,attr,omitempty"`
	GrandparentTitle     string           `json:"grandparentTitle,omitempty" xml:"grandparentTitle,attr,omitempty"`
	MediaTagPrefix       string           `json:"mediaTagPrefix" xml:"mediaTagPrefix,attr"`
	MediaTagVersion      int              `json:"mediaTagVersion" xml:"mediaTagVersion,attr"`
	Nocache              *plexBool        `json:"nocache,omitempty" xml:"nocache,attr,omitempty"`
	Meta                 *libraryListMeta `json:"Meta,omitempty" xml:"Meta,omitempty"`
	Metadata             []tvItem         `json:"Metadata" xml:"Directory"`
}

type tvMediaContainerResponse struct {
	MediaContainer tvMediaContainer `json:"MediaContainer"`
}

type tvItem struct {
	RatingKey            string      `json:"ratingKey" xml:"ratingKey,attr"`
	Key                  string      `json:"key" xml:"key,attr"`
	GUID                 string      `json:"guid" xml:"guid,attr"`
	Type                 string      `json:"type" xml:"type,attr"`
	Title                string      `json:"title" xml:"title,attr"`
	TitleSort            string      `json:"titleSort,omitempty" xml:"titleSort,attr,omitempty"`
	Summary              string      `json:"summary" xml:"summary,attr"`
	Thumb                string      `json:"thumb,omitempty" xml:"thumb,attr,omitempty"`
	Art                  string      `json:"art,omitempty" xml:"art,attr,omitempty"`
	Index                int         `json:"index,omitempty" xml:"index,attr,omitempty"`
	ParentIndex          int         `json:"parentIndex,omitempty" xml:"parentIndex,attr,omitempty"`
	Duration             int         `json:"duration,omitempty" xml:"duration,attr,omitempty"`
	AddedAt              int64       `json:"addedAt" xml:"addedAt,attr"`
	UpdatedAt            int64       `json:"updatedAt,omitempty" xml:"updatedAt,attr,omitempty"`
	ChildCount           int         `json:"childCount,omitempty" xml:"childCount,attr,omitempty"`
	LeafCount            int         `json:"leafCount,omitempty" xml:"leafCount,attr,omitempty"`
	ViewedLeafCount      *int        `json:"viewedLeafCount,omitempty" xml:"viewedLeafCount,attr,omitempty"`
	LibrarySectionID     int         `json:"librarySectionID,omitempty" xml:"librarySectionID,attr,omitempty"`
	LibrarySectionKey    string      `json:"librarySectionKey,omitempty" xml:"librarySectionKey,attr,omitempty"`
	LibrarySectionTitle  string      `json:"librarySectionTitle,omitempty" xml:"librarySectionTitle,attr,omitempty"`
	LibrarySectionUUID   string      `json:"librarySectionUUID,omitempty" xml:"librarySectionUUID,attr,omitempty"`
	ParentGUID           string      `json:"parentGuid,omitempty" xml:"parentGuid,attr,omitempty"`
	ParentRatingKey      string      `json:"parentRatingKey,omitempty" xml:"parentRatingKey,attr,omitempty"`
	ParentKey            string      `json:"parentKey,omitempty" xml:"parentKey,attr,omitempty"`
	ParentTitle          string      `json:"parentTitle,omitempty" xml:"parentTitle,attr,omitempty"`
	GrandparentGUID      string      `json:"grandparentGuid,omitempty" xml:"grandparentGuid,attr,omitempty"`
	GrandparentRatingKey string      `json:"grandparentRatingKey,omitempty" xml:"grandparentRatingKey,attr,omitempty"`
	GrandparentKey       string      `json:"grandparentKey,omitempty" xml:"grandparentKey,attr,omitempty"`
	GrandparentTitle     string      `json:"grandparentTitle,omitempty" xml:"grandparentTitle,attr,omitempty"`
	Media                []media     `json:"Media,omitempty" xml:"Media,omitempty"`
	Images               []plexImage `json:"Image,omitempty" xml:"Image,omitempty"`
	Extras               *extras     `json:"Extras,omitempty" xml:"Extras,omitempty"`
}

func (item tvItem) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	if item.Type == "episode" {
		start.Name.Local = "Video"
	} else {
		start.Name.Local = "Directory"
	}
	type alias tvItem
	return encoder.EncodeElement(alias(item), start)
}

func intPointer(value int) *int { return &value }

func truePointer() *plexBool {
	value := plexBool(true)
	return &value
}

func tvBaseItem() tvItem {
	return tvItem{
		Summary:             "",
		AddedAt:             libraryUpdatedAt,
		LibrarySectionID:    2,
		LibrarySectionKey:   "/library/sections/2",
		LibrarySectionTitle: tvLibraryName,
		LibrarySectionUUID:  tvLibraryUUID,
	}
}

func tvShow(includeSection bool) tvItem {
	item := tvBaseItem()
	item.RatingKey = tvShowRatingKey
	item.Key = "/library/metadata/" + tvShowRatingKey + "/children"
	item.GUID = "local://" + tvShowRatingKey
	item.Type = "show"
	item.Title = tvShowTitle
	item.TitleSort = tvShowTitle
	item.Index = 1
	item.ChildCount = 1
	item.LeafCount = 1
	item.ViewedLeafCount = intPointer(0)
	item.Thumb = "/:/resources/show.png"
	item.Art = "/:/resources/show-fanart.jpg"
	if !includeSection {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
	}
	return item
}

func tvSeason(includeSection bool) tvItem {
	item := tvBaseItem()
	item.RatingKey = tvSeasonRatingKey
	item.Key = "/library/metadata/" + tvSeasonRatingKey + "/children"
	item.GUID = "local://" + tvSeasonRatingKey
	item.Type = "season"
	item.Title = tvSeasonTitle
	item.Index = 1
	item.LeafCount = 1
	item.ViewedLeafCount = intPointer(0)
	item.ParentGUID = "local://" + tvShowRatingKey
	item.ParentRatingKey = tvShowRatingKey
	item.ParentKey = "/library/metadata/" + tvShowRatingKey
	item.ParentTitle = tvShowTitle
	item.Thumb = "/:/resources/show.png"
	item.Art = "/:/resources/show-fanart.jpg"
	if !includeSection {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
	}
	return item
}

func tvEpisodeMedia(detail, checkFiles bool) media {
	item := sacrificialMedia()
	item.ID = 2
	item.Duration = movieDuration
	item.Bitrate = movieBitrate
	item.Part[0].ID = 2
	item.Part[0].Key = tvPartPath
	item.Part[0].Duration = movieDuration
	item.Part[0].File = tvVirtualFile
	item.Part[0].Stream[0].ID = 3
	item.Part[0].Stream[1].ID = 4
	if !detail {
		item.Part[0].Stream = nil
	}
	if checkFiles {
		item.Part[0].Accessible = truePointer()
		item.Part[0].Exists = truePointer()
	}
	return item
}

func tvEpisode(includeSection, detail, includeExtras, checkFiles bool) tvItem {
	item := tvBaseItem()
	item.RatingKey = tvEpisodeRatingKey
	item.Key = "/library/metadata/" + tvEpisodeRatingKey
	item.GUID = "local://" + tvEpisodeRatingKey
	item.Type = "episode"
	item.Title = tvEpisodeTitle
	item.Index = 1
	item.ParentIndex = 1
	item.Duration = movieDuration
	item.UpdatedAt = libraryUpdatedAt
	item.ParentGUID = "local://" + tvSeasonRatingKey
	item.ParentRatingKey = tvSeasonRatingKey
	item.ParentKey = "/library/metadata/" + tvSeasonRatingKey
	item.ParentTitle = tvSeasonTitle
	item.GrandparentGUID = "local://" + tvShowRatingKey
	item.GrandparentRatingKey = tvShowRatingKey
	item.GrandparentKey = "/library/metadata/" + tvShowRatingKey
	item.GrandparentTitle = tvShowTitle
	item.Thumb = "/:/resources/show.png"
	item.Art = "/:/resources/show-fanart.jpg"
	item.Images = []plexImage{{Alt: tvEpisodeTitle, Type: "snapshot", URL: "/:/resources/show-fanart.jpg"}}
	item.Media = []media{tvEpisodeMedia(detail, checkFiles)}
	if includeExtras {
		item.Extras = &extras{Size: 0}
	}
	if !includeSection {
		item.LibrarySectionID = 0
		item.LibrarySectionKey = ""
		item.LibrarySectionTitle = ""
		item.LibrarySectionUUID = ""
	}
	return item
}

func tvContainer(metadata []tvItem) tvMediaContainer {
	return tvMediaContainer{
		Size:                len(metadata),
		AllowSync:           true,
		Identifier:          "com.plexapp.plugins.library",
		LibrarySectionID:    2,
		LibrarySectionTitle: tvLibraryName,
		LibrarySectionUUID:  tvLibraryUUID,
		MediaTagPrefix:      "/system/bundle/media/flags/",
		MediaTagVersion:     1,
		Metadata:            metadata,
	}
}

func writeNegotiatedTVContainer(w http.ResponseWriter, r *http.Request, container tvMediaContainer) {
	if acceptsJSON(r) {
		writeJSON(w, tvMediaContainerResponse{MediaContainer: container})
		return
	}
	writeXML(w, container)
}

func writeTVShowLibrary(w http.ResponseWriter, r *http.Request) {
	container := tvContainer([]tvItem{tvShow(false)})
	container.Art = "/:/resources/show-fanart.jpg"
	container.Thumb = "/:/resources/show.png"
	container.Title1 = tvLibraryName
	container.Title2 = "All Shows"
	container.ViewGroup = "show"
	container.ViewMode = 65592
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
}

func writeTVItem(w http.ResponseWriter, r *http.Request, item tvItem) {
	writeNegotiatedTVContainer(w, r, tvContainer([]tvItem{item}))
}

func writeTVShowChildren(w http.ResponseWriter, r *http.Request) {
	container := tvContainer([]tvItem{tvSeason(false)})
	container.Key = tvShowRatingKey
	container.Art = "/:/resources/show-fanart.jpg"
	container.Title1 = tvLibraryName
	container.Title2 = tvShowTitle
	container.ViewGroup = "season"
	container.ViewMode = 65593
	container.ParentIndex = 1
	container.ParentTitle = tvShowTitle
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
}

func writeTVSeasonChildren(w http.ResponseWriter, r *http.Request) {
	container := tvContainer([]tvItem{tvEpisode(false, false, false, false)})
	container.Key = tvSeasonRatingKey
	container.Art = "/:/resources/show-fanart.jpg"
	container.Title1 = tvShowTitle
	container.Title2 = tvSeasonTitle
	container.ViewGroup = "episode"
	container.ViewMode = 65592
	container.ParentIndex = 1
	container.ParentTitle = ""
	container.GrandparentRatingKey = 2
	container.GrandparentTitle = tvShowTitle
	container.Nocache = truePointer()
	writeNegotiatedTVContainer(w, r, container)
}

func writeTVEpisodeDetail(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	writeTVItem(w, r, tvEpisode(true, true, query.Get("includeExtras") == "1", query.Get("checkFiles") == "1"))
}

func tvLibraryPreferences() []any {
	hidden := libraryPreference("hidden", "int", "0", "0", "Visibility", "Restrict where content from this library should appear.")
	hidden["enumValues"] = "0:Include in home screen and global search|1:Exclude from home screen|2:Exclude from home screen and global search"
	episodeSort := libraryPreference("episodeSort", "text", "-1", "-1", "Episode sorting", "How to sort the episodes for this show.")
	episodeSort["enumValues"] = "-1:Library default|0:Oldest first|1:Newest first"
	flatten := libraryPreference("flattenSeasons", "int", "0", "0", "Seasons", "Choose whether to display seasons.")
	flatten["enumValues"] = "0:Show|2:Hide for single-season series|1:Hide"
	return []any{hidden, episodeSort, flatten}
}

func tvLibraryDirectory(includePreferences bool) map[string]any {
	directory := map[string]any{
		"agent": "tv.plex.agents.series", "scanner": "Plex TV Series", "language": "en-US",
		"refreshing": false, "uuid": tvLibraryUUID, "id": tvLibraryID,
		"key": "/library/sections/2", "hubKey": "/hubs/sections/2", "type": "show",
		"title": tvLibraryName, "updatedAt": libraryUpdatedAt, "scannedAt": libraryScannedAt,
		"Pivot": []any{
			map[string]any{"id": "recommended", "key": "/hubs/sections/2", "type": "hub", "title": "Recommended", "context": "content.discover", "symbol": "star"},
			map[string]any{"id": "library", "key": "/library/sections/2/all?type=2", "type": "list", "title": "Library", "context": "content.library", "symbol": "library"},
			map[string]any{"id": "categories", "key": "/library/sections/2/categories", "type": "list", "title": "Categories", "context": "content.categories", "symbol": "stack"},
		},
	}
	if includePreferences {
		directory["Preferences"] = map[string]any{"Setting": tvLibraryPreferences()}
	}
	return directory
}

func tvLibrarySectionDirectory() map[string]any {
	return map[string]any{
		"agent": "tv.plex.agents.series", "allowSync": true, "content": true,
		"createdAt": libraryUpdatedAt, "directory": true, "filters": true, "hidden": 0,
		"key": tvLibraryID, "language": "en-US", "metadataAgentProviderGroupId": 2,
		"refreshing": false, "scannedAt": libraryScannedAt, "scanner": "Plex TV Series",
		"title": tvLibraryName, "type": "show", "updatedAt": libraryUpdatedAt, "uuid": tvLibraryUUID,
	}
}

func movieLibrarySectionDirectory() map[string]any {
	return map[string]any{
		"agent": "tv.plex.agents.movie", "allowSync": true, "content": true,
		"createdAt": libraryUpdatedAt, "directory": true, "filters": true, "hidden": 0,
		"key": libraryID, "language": "en-US", "metadataAgentProviderGroupId": 1,
		"refreshing": false, "scannedAt": libraryScannedAt, "scanner": "Plex Movie",
		"title": sacrificialLibraryName, "type": "movie", "updatedAt": libraryUpdatedAt, "uuid": libraryUUID,
	}
}

func writeStage4LibrarySections(w http.ResponseWriter) {
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": 2, "allowSync": false, "title1": "Plex Library",
		"Directory": []any{movieLibrarySectionDirectory(), tvLibrarySectionDirectory()},
	}})
}

func tvLibraryDirectories() []any {
	return []any{
		map[string]any{"key": "all", "title": "All Shows"},
		map[string]any{"key": "unwatched", "title": "Unwatched"},
		map[string]any{"key": "newest", "title": "Recently Released"},
		map[string]any{"key": "recentlyAdded", "title": "Recently Added"},
		map[string]any{"key": "recentlyViewed", "title": "Recently Viewed Episodes"},
		map[string]any{"key": "recentlyViewedShows", "title": "Recently Viewed Shows"},
		map[string]any{"key": "onDeck", "title": "Continue Watching"},
		map[string]any{"key": "collection", "secondary": true, "title": "By Collection"},
		map[string]any{"key": "firstCharacter", "secondary": true, "title": "By First Letter"},
		map[string]any{"key": "genre", "secondary": true, "title": "By Genre"},
		map[string]any{"key": "year", "secondary": true, "title": "By Year"},
		map[string]any{"key": "contentRating", "secondary": true, "title": "By Content Rating"},
		map[string]any{"key": "search?type=2", "prompt": "Search for Shows", "search": true, "title": "Search Shows..."},
		map[string]any{"key": "search?type=4", "prompt": "Search for Episodes", "search": true, "title": "Search Episodes..."},
	}
}

func writeTVLibrarySection(w http.ResponseWriter) {
	directories := tvLibraryDirectories()
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": len(directories), "allowSync": false, "art": "/:/resources/show-fanart.jpg",
		"content": "secondary", "identifier": "com.plexapp.plugins.library", "librarySectionID": 2,
		"mediaTagPrefix": "/system/bundle/media/flags/", "mediaTagVersion": 1, "nocache": true,
		"thumb": "/:/resources/show.png", "title1": tvLibraryName, "viewGroup": "secondary", "viewMode": 65592,
		"Directory": directories,
	}})
}

func writeTVLibraryPrefs(w http.ResponseWriter) {
	settings := tvLibraryPreferences()
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{"size": len(settings), "Setting": settings}})
}

func writeEmptyTVNavigation(w http.ResponseWriter, itemType string) {
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": 0, "allowSync": true, "identifier": "com.plexapp.plugins.library",
		"librarySectionID": 2, "librarySectionTitle": tvLibraryName, "librarySectionUUID": tvLibraryUUID,
		"mediaTagPrefix": "/system/bundle/media/flags/", "mediaTagVersion": 1,
		"viewGroup": itemType, "Metadata": []any{},
	}})
}

func tvRecentlyAddedHub(home bool) map[string]any {
	identifier := "tv.recentlyadded.2"
	key := "/library/sections/2/all?type=2&sort=addedAt:desc"
	context := "hub.tv.recentlyadded"
	title := "Recently Added in " + tvLibraryName
	if home {
		identifier = "home.television.recent"
		key = "/hubs/home/recentlyAdded?type=2"
		context = "hub.home.television.recent"
		title = "Recently Added TV"
	}
	return map[string]any{
		"context": context, "hubIdentifier": identifier, "hubKey": "/library/metadata/" + tvShowRatingKey,
		"key": key, "more": false, "promoted": true, "size": 1, "style": "shelf", "title": title,
		"type": "show", "Metadata": []any{tvShow(true)},
	}
}

func writeTVSectionHubs(w http.ResponseWriter) {
	hubs := []any{
		map[string]any{"context": "hub.tv.inprogress", "hubIdentifier": "tv.inprogress.2", "key": "/hubs/sections/2/continueWatching/items", "more": false, "size": 0, "style": "shelf", "title": "Continue Watching", "type": "episode"},
		tvRecentlyAddedHub(false),
	}
	writeJSON(w, map[string]any{"MediaContainer": map[string]any{
		"size": len(hubs), "allowSync": true, "identifier": "com.plexapp.plugins.library",
		"librarySectionID": 2, "librarySectionTitle": tvLibraryName, "librarySectionUUID": tvLibraryUUID,
		"Hub": hubs,
	}})
}

func serveTVPresentationRoute(w http.ResponseWriter, r *http.Request, mediaPath string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	switch r.URL.Path {
	case "/library/sections":
		writeStage4LibrarySections(w)
	case "/library/sections/2":
		writeTVLibrarySection(w)
	case "/library/sections/2/prefs":
		writeTVLibraryPrefs(w)
	case "/library/sections/2/all":
		writeTVShowLibrary(w, r)
	case "/library/sections/2/categories", "/library/sections/2/collection", "/library/sections/2/firstCharacter",
		"/library/sections/2/genre", "/library/sections/2/year", "/library/sections/2/contentRating":
		writeEmptyContainer(w)
	case "/library/sections/2/unwatched", "/library/sections/2/newest", "/library/sections/2/recentlyAdded",
		"/library/sections/2/recentlyViewed", "/library/sections/2/recentlyViewedShows", "/library/sections/2/onDeck",
		"/library/sections/2/search":
		writeEmptyTVNavigation(w, "show")
	case "/library/metadata/" + tvShowRatingKey:
		writeTVItem(w, r, tvShow(true))
	case "/library/metadata/" + tvShowRatingKey + "/children":
		writeTVShowChildren(w, r)
	case "/library/metadata/" + tvSeasonRatingKey:
		writeTVItem(w, r, tvSeason(true))
	case "/library/metadata/" + tvSeasonRatingKey + "/children":
		writeTVSeasonChildren(w, r)
	case "/library/metadata/" + tvEpisodeRatingKey:
		writeTVEpisodeDetail(w, r)
	case tvPartPath:
		addCommonHeaders(w)
		http.ServeFile(w, r, mediaPath)
	default:
		return false
	}
	return true
}
