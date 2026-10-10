package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func newTVStage4TestHandler(t *testing.T) (http.Handler, []byte, func()) {
	t.Helper()
	mediaBytes := bytes.Repeat([]byte("wonkvod-tv-media-"), 400)
	mediaPath := t.TempDir() + "/test2.mp4"
	if err := os.WriteFile(mediaPath, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"source":"upstream","method":%q,"path":%q}`, r.Method, r.URL.Path)
	}))
	handler, err := pmsPassthroughPresentationHandler(upstream.URL, mediaPath)
	if err != nil {
		upstream.Close()
		t.Fatal(err)
	}
	return handler, mediaBytes, upstream.Close
}

func tvRequest(t *testing.T, handler http.Handler, method, target, accept string, configure func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if configure != nil {
		configure(request)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeTVJSON(t *testing.T, response *httptest.ResponseRecorder) tvMediaContainer {
	t.Helper()
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("TV JSON response = status %d content-type %q body %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var decoded tvMediaContainerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.MediaContainer
}

func TestTVSectionProviderAndTimelineContract(t *testing.T) {
	handler, _, closeUpstream := newTVStage4TestHandler(t)
	defer closeUpstream()

	sections := tvRequest(t, handler, http.MethodGet, "/library/sections", "application/json", nil)
	container := responseContainerObject(t, sections)
	directories := requireArray(t, container["Directory"], "Directory", 2)
	movieSection := requireObject(t, directories[0], "Directory[0]")
	if movieSection["key"] != libraryID || movieSection["uuid"] != libraryUUID || movieSection["title"] != sacrificialLibraryName {
		t.Fatalf("movie section changed: %#v", movieSection)
	}
	tvSection := requireObject(t, directories[1], "Directory[1]")
	if tvSection["key"] != tvLibraryID || tvSection["uuid"] != tvLibraryUUID || tvSection["title"] != tvLibraryName || tvSection["type"] != "show" ||
		tvSection["agent"] != "tv.plex.agents.series" || tvSection["scanner"] != "Plex TV Series" {
		t.Fatalf("TV section = %#v", tvSection)
	}

	providers := tvRequest(t, handler, http.MethodGet, "/media/providers?includePreferences=1", "application/json", nil)
	providerContainer := responseContainerObject(t, providers)
	provider := requireObject(t, requireArray(t, providerContainer["MediaProvider"], "MediaProvider", 1)[0], "MediaProvider[0]")
	features := requireArray(t, provider["Feature"], "Feature", 5)
	var content, timeline map[string]any
	for _, raw := range features {
		feature := requireObject(t, raw, "Feature")
		switch feature["type"] {
		case "content":
			content = feature
		case "timeline":
			timeline = feature
		}
	}
	if timeline["scrobbleKey"] != "/:/scrobble" || timeline["unscrobbleKey"] != "/:/unscrobble" {
		t.Fatalf("timeline compatibility keys changed: %#v", timeline)
	}
	providerDirectories := requireArray(t, content["Directory"], "content.Directory", 3)
	tvProvider := requireObject(t, providerDirectories[2], "content.Directory[2]")
	if tvProvider["id"] != tvLibraryID || tvProvider["uuid"] != tvLibraryUUID || tvProvider["key"] != "/library/sections/2" ||
		tvProvider["hubKey"] != "/hubs/sections/2" || tvProvider["type"] != "show" {
		t.Fatalf("TV provider directory = %#v", tvProvider)
	}
	pivots := requireArray(t, tvProvider["Pivot"], "TV Pivot", 3)
	if requireObject(t, pivots[1], "TV Pivot[1]")["key"] != "/library/sections/2/all?type=2" {
		t.Fatalf("TV library pivot = %#v", pivots[1])
	}
	settings := requireArray(t, requireObject(t, tvProvider["Preferences"], "TV Preferences")["Setting"], "TV Preferences.Setting", 3)
	ids := []string{}
	for _, raw := range settings {
		ids = append(ids, requireObject(t, raw, "TV setting")["id"].(string))
	}
	if !reflect.DeepEqual(ids, []string{"hidden", "episodeSort", "flattenSeasons"}) {
		t.Fatalf("TV preference IDs = %v", ids)
	}
}

func TestTVHierarchyJSONAndXML(t *testing.T) {
	handler, _, closeUpstream := newTVStage4TestHandler(t)
	defer closeUpstream()

	cases := []struct {
		path, itemType, ratingKey, key, xmlElement string
	}{
		{"/library/sections/2/all?type=2", "show", tvShowRatingKey, "/library/metadata/" + tvShowRatingKey + "/children", "Directory"},
		{"/library/metadata/" + tvShowRatingKey, "show", tvShowRatingKey, "/library/metadata/" + tvShowRatingKey + "/children", "Directory"},
		{"/library/metadata/" + tvShowRatingKey + "/children", "season", tvSeasonRatingKey, "/library/metadata/" + tvSeasonRatingKey + "/children", "Directory"},
		{"/library/metadata/" + tvSeasonRatingKey, "season", tvSeasonRatingKey, "/library/metadata/" + tvSeasonRatingKey + "/children", "Directory"},
		{"/library/metadata/" + tvSeasonRatingKey + "/children", "episode", tvEpisodeRatingKey, "/library/metadata/" + tvEpisodeRatingKey, "Video"},
		{"/library/metadata/" + tvEpisodeRatingKey + "?checkFiles=1&includeExtras=1", "episode", tvEpisodeRatingKey, "/library/metadata/" + tvEpisodeRatingKey, "Video"},
	}
	for _, test := range cases {
		t.Run(test.itemType+test.path, func(t *testing.T) {
			container := decodeTVJSON(t, tvRequest(t, handler, http.MethodGet, test.path, "application/json", nil))
			if container.LibrarySectionID != 2 || container.LibrarySectionUUID != tvLibraryUUID || len(container.Metadata) != 1 {
				t.Fatalf("container = %#v", container)
			}
			item := container.Metadata[0]
			if item.Type != test.itemType || item.RatingKey != test.ratingKey || item.Key != test.key {
				t.Fatalf("item = %#v", item)
			}
			xmlResponse := tvRequest(t, handler, http.MethodGet, test.path, "text/xml", nil)
			if xmlResponse.Code != http.StatusOK || !strings.HasPrefix(xmlResponse.Header().Get("Content-Type"), "text/xml") ||
				!strings.Contains(xmlResponse.Body.String(), "<"+test.xmlElement+" ") ||
				!strings.Contains(xmlResponse.Body.String(), `ratingKey="`+test.ratingKey+`"`) {
				t.Fatalf("TV XML response = status %d content-type %q body %q", xmlResponse.Code, xmlResponse.Header().Get("Content-Type"), xmlResponse.Body.String())
			}
		})
	}

	show := decodeTVJSON(t, tvRequest(t, handler, http.MethodGet, "/library/metadata/"+tvShowRatingKey, "application/json", nil)).Metadata[0]
	if show.ChildCount != 1 || show.LeafCount != 1 || show.Title != tvShowTitle {
		t.Fatalf("show hierarchy = %#v", show)
	}
	season := decodeTVJSON(t, tvRequest(t, handler, http.MethodGet, "/library/metadata/"+tvSeasonRatingKey, "application/json", nil)).Metadata[0]
	if season.ParentRatingKey != tvShowRatingKey || season.ParentKey != "/library/metadata/"+tvShowRatingKey || season.ParentTitle != tvShowTitle || season.Index != 1 || season.LeafCount != 1 {
		t.Fatalf("season hierarchy = %#v", season)
	}
	episode := decodeTVJSON(t, tvRequest(t, handler, http.MethodGet, "/library/metadata/"+tvEpisodeRatingKey+"?checkFiles=1&includeExtras=1", "application/json", nil)).Metadata[0]
	if episode.ParentRatingKey != tvSeasonRatingKey || episode.ParentKey != "/library/metadata/"+tvSeasonRatingKey || episode.ParentTitle != tvSeasonTitle ||
		episode.GrandparentRatingKey != tvShowRatingKey || episode.GrandparentKey != "/library/metadata/"+tvShowRatingKey || episode.GrandparentTitle != tvShowTitle ||
		episode.Index != 1 || episode.ParentIndex != 1 || episode.Title != tvEpisodeTitle || episode.Extras == nil {
		t.Fatalf("episode hierarchy = %#v", episode)
	}
	if len(episode.Media) != 1 || episode.Media[0].ID != 2 || len(episode.Media[0].Part) != 1 || episode.Media[0].Part[0].ID != 2 ||
		episode.Media[0].Part[0].Key != tvPartPath || len(episode.Media[0].Part[0].Stream) != 2 ||
		episode.Media[0].Part[0].Stream[0].ID != 3 || episode.Media[0].Part[0].Stream[1].ID != 4 ||
		episode.Media[0].Part[0].Accessible == nil || episode.Media[0].Part[0].Exists == nil {
		t.Fatalf("episode Media/Part/Stream = %#v", episode.Media)
	}
}

func TestTVPartAndControlPlaneOwnership(t *testing.T) {
	handler, mediaBytes, closeUpstream := newTVStage4TestHandler(t)
	defer closeUpstream()

	full := tvRequest(t, handler, http.MethodGet, tvPartPath, "video/mp4", nil)
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), mediaBytes) || full.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("TV Part GET = status %d content-type %q bytes %d", full.Code, full.Header().Get("Content-Type"), full.Body.Len())
	}
	head := tvRequest(t, handler, http.MethodHead, tvPartPath, "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(mediaBytes)) || head.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("TV Part HEAD = status %d headers %#v bytes %d", head.Code, head.Header(), head.Body.Len())
	}
	ranged := tvRequest(t, handler, http.MethodGet, tvPartPath, "video/mp4", func(request *http.Request) {
		request.Header.Set("Range", "bytes=25-74")
	})
	if ranged.Code != http.StatusPartialContent || ranged.Header().Get("Content-Range") != fmt.Sprintf("bytes 25-74/%d", len(mediaBytes)) ||
		ranged.Header().Get("Content-Length") != "50" || !bytes.Equal(ranged.Body.Bytes(), mediaBytes[25:75]) {
		t.Fatalf("TV Part Range = status %d headers %#v bytes %d", ranged.Code, ranged.Header(), ranged.Body.Len())
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/playQueues"},
		{http.MethodGet, "/video/:/transcode/universal/decision"},
		{http.MethodGet, "/:/timeline"},
		{http.MethodGet, "/:/scrobble"},
		{http.MethodGet, "/:/unscrobble"},
		{http.MethodGet, "/library/metadata/" + tvEpisodeRatingKey + "/related"},
		{http.MethodGet, "/status/sessions"},
		{http.MethodGet, "/activities"},
		{http.MethodGet, "/:/websockets/notifications"},
	} {
		response := tvRequest(t, handler, route.method, route.path, "application/json", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source":"upstream"`) {
			t.Errorf("control route %s %s not proxied: status %d body %q", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestTVHubsAndMovieFixtureRemainCoherent(t *testing.T) {
	handler, _, closeUpstream := newTVStage4TestHandler(t)
	defer closeUpstream()

	hubs := responseContainerObject(t, tvRequest(t, handler, http.MethodGet, "/hubs/sections/2", "application/json", nil))
	if hubs["librarySectionID"] != json.Number("2") || hubs["librarySectionUUID"] != tvLibraryUUID || hubs["librarySectionTitle"] != tvLibraryName {
		t.Fatalf("TV hub container = %#v", hubs)
	}
	values := requireArray(t, hubs["Hub"], "Hub", 2)
	recent := requireObject(t, values[1], "Hub[1]")
	if recent["type"] != "show" || recent["hubKey"] != "/library/metadata/"+tvShowRatingKey ||
		requireObject(t, requireArray(t, recent["Metadata"], "Hub[1].Metadata", 1)[0], "show")["ratingKey"] != tvShowRatingKey {
		t.Fatalf("TV recently-added hub = %#v", recent)
	}

	movie := decodeTVIndependentMovie(t, tvRequest(t, handler, http.MethodGet, "/library/metadata/1?checkFiles=1&includeExtras=1", "application/json", nil))
	want := sacrificialMovieDetail(true, true)
	if !reflect.DeepEqual(movie, want) {
		t.Fatalf("movie fixture changed by TV addition:\n got %#v\nwant %#v", movie, want)
	}
}

func decodeTVIndependentMovie(t *testing.T, response *httptest.ResponseRecorder) movie {
	t.Helper()
	var decoded mediaContainerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.MediaContainer.Metadata) != 1 {
		t.Fatalf("movie metadata count = %d", len(decoded.MediaContainer.Metadata))
	}
	return decoded.MediaContainer.Metadata[0]
}
