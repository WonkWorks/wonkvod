package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

func realCatalogTestHandler(t *testing.T) (*realCatalogHandler, *canonicalSnapshot, []byte) {
	t.Helper()
	mediaBytes := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	imageBytes := []byte("\x89PNG\r\n\x1a\nfixture")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/poster.png" || r.URL.Path == "/fanart.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBytes)
		case r.URL.Path == "/movie/user/pass/movie-1.mp4" || r.URL.Path == "/series/user/pass/episode-1.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "fixture.mp4", time.Unix(1, 0), bytes.NewReader(mediaBytes))
		default:
			w.Header().Set("X-Test-Upstream", "true")
			w.WriteHeader(http.StatusTeapot)
			_, _ = w.Write([]byte("passthrough"))
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := xtream.NewClient(upstream.Client(), upstream.URL, "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := canonicalSnapshot{
		Version: realCatalogVersion, GeneratedAt: time.Unix(1_700_000_000, 0).UTC(),
		MovieCategory: catalogCategory{ID: "917", Name: realMovieCategoryName, Count: 1},
		TVCategory:    catalogCategory{ID: "887", Name: realTVCategoryName, Count: 1},
		Movies: []canonicalMovie{{
			SourceID: "movie-1", Category: catalogCategory{ID: "917", Name: realMovieCategoryName, Count: 1},
			Title: "Fixture Movie", Year: "2024", Extension: "mp4", Revision: "1700000001",
			Details: canonical.MovieDetails{ID: "movie-1", Title: "Fixture Movie", Plot: "Movie plot", Premiere: "2024-01-02", DurationMillis: 120000, Bitrate: 1500, Rating: "7.5", TMDBID: "123", Poster: upstream.URL + "/poster.png", Fanart: upstream.URL + "/fanart.png", Extension: "mp4"},
		}},
		Shows: []canonicalShow{{
			SourceID: "show-1", Category: catalogCategory{ID: "887", Name: realTVCategoryName, Count: 1},
			Title: "Fixture Show", Year: "2023", Revision: "1700000002",
			Details: canonical.SeriesDetails{ID: "show-1", Title: "Fixture Show", Plot: "Show plot", Poster: upstream.URL + "/poster.png", Fanart: upstream.URL + "/fanart.png"},
			Seasons: []canonicalSeason{{Number: 1, Poster: upstream.URL + "/poster.png", Episodes: []canonicalEpisode{{SourceID: "episode-1", Season: 1, Number: 2, Extension: "mp4", Details: canonical.EpisodeDetails{ID: "episode-1", Title: "Fixture Episode", Season: 1, Number: 2, Plot: "Episode plot", DurationMillis: 60000, Bitrate: 800, Artwork: upstream.URL + "/fanart.png", Extension: "mp4"}}}}},
		}},
	}
	runtime := &catalogRuntime{client: client, snapshot: snapshot, sources: make(map[string]*xtream.VODSource), capabilities: make(map[string]xtream.CapabilityProbe)}
	passthrough := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Upstream", "true")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("passthrough"))
	})
	handler := &realCatalogHandler{catalog: runtime, passthrough: passthrough, httpClient: upstream.Client(), queues: make(map[int64]virtualPlayQueue)}
	handler.queueID.Store(1000)
	return handler, &snapshot, mediaBytes
}

func performRealRequest(handler http.Handler, method, target, accept string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, body)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeContainer(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	container, ok := document["MediaContainer"].(map[string]any)
	if !ok {
		t.Fatalf("missing MediaContainer in %#v", document)
	}
	return container
}

func TestRealCatalogStableTypedIDs(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	movieID := movieObjectID(snapshot.Movies[0])
	episodeID := episodeObjectID(snapshot.Shows[0].Seasons[0].Episodes[0])
	if movieID != movieObjectID(snapshot.Movies[0]) || episodeID != episodeObjectID(snapshot.Shows[0].Seasons[0].Episodes[0]) {
		t.Fatal("stable IDs changed across repeated projection")
	}
	all := []int64{
		stableNumericID('1', "xtream:movie:movie-1:object"),
		stableNumericID('2', "xtream:show:show-1"),
		stableNumericID('3', "xtream:show:show-1:season:1"),
		stableNumericID('4', "xtream:episode:episode-1:object"),
		mediaObjectID("movie", "movie-1"), partObjectID("movie", "movie-1"),
		videoStreamObjectID("movie", "movie-1"), audioStreamObjectID("movie", "movie-1"),
	}
	seen := map[int64]bool{}
	for _, id := range all {
		if id <= 0 || id > 9_007_199_254_740_991 || seen[id] {
			t.Fatalf("invalid or duplicate typed ID %d", id)
		}
		seen[id] = true
	}
	if err := validateStableIDs(*snapshot); err != nil {
		t.Fatal(err)
	}
	if _, ok := handler.catalog.playable(movieID); !ok {
		t.Fatal("movie ID did not resolve")
	}
	if _, ok := handler.catalog.playable(episodeID); !ok {
		t.Fatal("episode ID did not resolve")
	}
}

func TestRealProviderAndCatalogHierarchy(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	provider := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/media/providers", "application/json", nil))
	providers := provider["MediaProvider"].([]any)
	features := providers[0].(map[string]any)["Feature"].([]any)
	if len(features) != 5 {
		t.Fatalf("features=%d, want 5", len(features))
	}
	timeline := features[4].(map[string]any)
	if timeline["type"] != "timeline" || timeline["scrobbleKey"] != "/:/scrobble" || timeline["unscrobbleKey"] != "/:/unscrobble" {
		t.Fatalf("incomplete timeline feature: %#v", timeline)
	}

	movies := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/sections/1/all?X-Plex-Container-Start=0&X-Plex-Container-Size=1", "application/json", nil))
	if movies["size"] != float64(1) || len(movies["Metadata"].([]any)) != 1 {
		t.Fatalf("unexpected movie page: %#v", movies)
	}
	movieID := movieObjectID(snapshot.Movies[0])
	detail := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/metadata/"+movieID+"?includeExtras=1", "application/json", nil))
	projectedMovie := detail["Metadata"].([]any)[0].(map[string]any)
	if projectedMovie["ratingKey"] != movieID || projectedMovie["title"] != "Fixture Movie" {
		t.Fatalf("bad movie detail: %#v", projectedMovie)
	}
	xmlDetail := performRealRequest(handler, http.MethodGet, "/library/metadata/"+movieID, "text/xml", nil)
	if xmlDetail.Code != http.StatusOK || !strings.Contains(xmlDetail.Body.String(), `ratingKey="`+movieID+`"`) {
		t.Fatalf("bad XML detail status=%d body=%s", xmlDetail.Code, xmlDetail.Body.String())
	}

	showID := showObjectID(snapshot.Shows[0])
	seasons := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/metadata/"+showID+"/children", "application/json", nil))
	season := seasons["Metadata"].([]any)[0].(map[string]any)
	if season["parentRatingKey"] != showID || season["index"] != float64(1) {
		t.Fatalf("bad season projection: %#v", season)
	}
	seasonID := seasonObjectID(snapshot.Shows[0], snapshot.Shows[0].Seasons[0])
	episodes := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/metadata/"+seasonID+"/children", "application/json", nil))
	episode := episodes["Metadata"].([]any)[0].(map[string]any)
	if episode["parentRatingKey"] != seasonID || episode["grandparentRatingKey"] != showID || episode["index"] != float64(2) {
		t.Fatalf("bad episode projection: %#v", episode)
	}
}

func TestRealArtworkMediaRangeAndControlSeam(t *testing.T) {
	handler, snapshot, mediaBytes := realCatalogTestHandler(t)
	movie := snapshot.Movies[0]
	movieID := movieObjectID(movie)
	partID := partObjectID("movie", movie.SourceID)

	art := performRealRequest(handler, http.MethodGet, "/library/metadata/"+movieID+"/thumb/1", "", nil)
	if art.Code != http.StatusOK || art.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(art.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("bad artwork response status=%d type=%q", art.Code, art.Header().Get("Content-Type"))
	}
	photoURL := "/photo/:/transcode?url=" + url.QueryEscape("/library/metadata/"+movieID+"/art/1") + "&width=853&height=480"
	photo := performRealRequest(handler, http.MethodGet, photoURL, "", nil)
	if photo.Code != http.StatusOK || !strings.HasPrefix(photo.Header().Get("Content-Type"), "image/") {
		t.Fatalf("bad photo response status=%d type=%q", photo.Code, photo.Header().Get("Content-Type"))
	}

	rangeRequest := httptest.NewRequest(http.MethodGet, "/library/parts/"+strconvFormatInt(partID)+"/file.mp4", nil)
	rangeRequest.Header.Set("Range", "bytes=4-9")
	rangeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(rangeRecorder, rangeRequest)
	if rangeRecorder.Code != http.StatusPartialContent || rangeRecorder.Header().Get("Content-Range") != "bytes 4-9/36" || !bytes.Equal(rangeRecorder.Body.Bytes(), mediaBytes[4:10]) {
		t.Fatalf("bad range status=%d content-range=%q body=%q", rangeRecorder.Code, rangeRecorder.Header().Get("Content-Range"), rangeRecorder.Body.Bytes())
	}
	head := performRealRequest(handler, http.MethodHead, "/library/parts/"+strconvFormatInt(partID)+"/file.mp4", "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "36" {
		t.Fatalf("bad HEAD status=%d length=%q body=%d", head.Code, head.Header().Get("Content-Length"), head.Body.Len())
	}

	queue := decodeContainer(t, performRealRequest(handler, http.MethodPost, "/playQueues?uri="+url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/"+movieID), "application/json", nil))
	queueID := int64(queue["playQueueID"].(float64))
	if queueID <= 1000 || queue["playQueueTotalCount"] != float64(1) {
		t.Fatalf("bad queue: %#v", queue)
	}
	decision := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/video/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+movieID), "application/json", nil))
	if decision["mdeDecisionCode"] != float64(1000) {
		t.Fatalf("bad decision: %#v", decision)
	}

	virtualState := performRealRequest(handler, http.MethodGet, "/:/scrobble?ratingKey="+movieID, "application/json", nil)
	if virtualState.Code != http.StatusOK || virtualState.Header().Get("X-Test-Upstream") != "" {
		t.Fatal("virtual scrobble was not handled locally")
	}
	fallbackResponse := performRealRequest(handler, http.MethodGet, "/:/scrobble?ratingKey=999", "", nil)
	if fallbackResponse.Code != http.StatusTeapot || fallbackResponse.Header().Get("X-Test-Upstream") != "true" {
		t.Fatal("unrelated control request did not fall through")
	}

	partAck := performRealRequest(handler, http.MethodPut, "/library/parts/"+strconvFormatInt(partID)+"?subtitleStreamID=0&allParts=1", "application/json", nil)
	if partAck.Code != http.StatusOK || partAck.Header().Get("X-Test-Upstream") != "" {
		t.Fatal("virtual Part PUT was not acknowledged locally")
	}
	unknownPartAck := performRealRequest(handler, http.MethodPut, "/library/parts/999?subtitleStreamID=0", "application/json", nil)
	if unknownPartAck.Code != http.StatusTeapot || unknownPartAck.Header().Get("X-Test-Upstream") != "true" {
		t.Fatal("unknown Part PUT did not fall through")
	}
}

func TestRealTVPlayQueueSelectsEpisodeFromKey(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	show := snapshot.Shows[0]
	season := show.Seasons[0]
	episode := season.Episodes[0]
	showID := showObjectID(show)
	seasonID := seasonObjectID(show, season)
	episodeID := episodeObjectID(episode)

	requestQueue := func(contextID string) map[string]any {
		t.Helper()
		target := "/playQueues?type=video&continuous=1&uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/"+contextID) + "&key=" + url.QueryEscape("/library/metadata/"+episodeID)
		return decodeContainer(t, performRealRequest(handler, http.MethodPost, target, "application/json", nil))
	}
	for _, contextID := range []string{showID, seasonID} {
		queue := requestQueue(contextID)
		metadata := queue["Metadata"].([]any)[0].(map[string]any)
		if metadata["ratingKey"] != episodeID || metadata["type"] != "episode" {
			t.Fatalf("context=%s selected metadata=%#v", contextID, metadata)
		}
		if !strings.Contains(queue["playQueueSourceURI"].(string), url.QueryEscape("/library/metadata/"+contextID)) {
			t.Fatalf("context=%s source URI=%q", contextID, queue["playQueueSourceURI"])
		}
		queueID := int64(queue["playQueueID"].(float64))
		read := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/playQueues/"+strconv.FormatInt(queueID, 10), "application/json", nil))
		if read["Metadata"].([]any)[0].(map[string]any)["ratingKey"] != episodeID {
			t.Fatalf("GET queue did not retain episode: %#v", read)
		}
	}

	decision := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/video/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+episodeID), "application/json", nil))
	selected := decision["Metadata"].([]any)[0].(map[string]any)
	if selected["ratingKey"] != episodeID || selected["Media"].([]any)[0].(map[string]any)["Part"].([]any)[0].(map[string]any)["key"] != "/library/parts/"+strconvFormatInt(partObjectID("episode", episode.SourceID))+"/file.mp4" {
		t.Fatalf("decision did not resolve episode: %#v", selected)
	}

	unknown := "/playQueues?type=video&continuous=1&uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/999") + "&key=" + url.QueryEscape("/library/metadata/998")
	fallbackResponse := performRealRequest(handler, http.MethodPost, unknown, "application/json", nil)
	if fallbackResponse.Code != http.StatusTeapot || fallbackResponse.Header().Get("X-Test-Upstream") != "true" {
		t.Fatal("unknown TV queue did not fall through")
	}
	malformed := "/playQueues?type=video&uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/not-a-wonkvod-id") + "&key=bad"
	fallbackResponse = performRealRequest(handler, http.MethodPost, malformed, "application/json", nil)
	if fallbackResponse.Code != http.StatusTeapot || fallbackResponse.Header().Get("X-Test-Upstream") != "true" {
		t.Fatal("malformed TV queue did not fall through")
	}
}

func strconvFormatInt(value int64) string {
	return strings.TrimSpace(strconv.FormatInt(value, 10))
}
