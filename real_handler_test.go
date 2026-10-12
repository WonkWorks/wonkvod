package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"syscall"
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
			Details:    canonical.MovieDetails{ID: "movie-1", Title: "Fixture Movie", Plot: "Movie plot", Premiere: "2024-01-02", DurationMillis: 120000, Bitrate: 1500, Rating: "7.5", TMDBID: "123", Poster: upstream.URL + "/poster.png", Fanart: upstream.URL + "/fanart.png", Extension: "mp4"},
			MediaFacts: fixtureMediaFacts("mp4", 120000, 1500),
		}},
		Shows: []canonicalShow{{
			SourceID: "show-1", Category: catalogCategory{ID: "887", Name: realTVCategoryName, Count: 1},
			Title: "Fixture Show", Year: "2023", Revision: "1700000002",
			Details: canonical.SeriesDetails{ID: "show-1", Title: "Fixture Show", Plot: "Show plot", Poster: upstream.URL + "/poster.png", Fanart: upstream.URL + "/fanart.png"},
			Seasons: []canonicalSeason{{Number: 1, Poster: upstream.URL + "/poster.png", Episodes: []canonicalEpisode{{SourceID: "episode-1", Season: 1, Number: 2, Extension: "mp4", Details: canonical.EpisodeDetails{ID: "episode-1", Title: "Fixture Episode", Season: 1, Number: 2, Plot: "Episode plot", DurationMillis: 60000, Bitrate: 800, Artwork: upstream.URL + "/fanart.png", Extension: "mp4"}, MediaFacts: fixtureMediaFacts("mp4", 60000, 800)}}}},
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

func fixtureMediaFacts(container string, duration, bitrate int) canonical.MediaFacts {
	return canonical.MediaFacts{State: "inspected", Container: container, DurationMS: int64(duration), BitrateKbps: bitrate, Video: []canonical.VideoStream{{Index: 0, Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, CodedWidth: 1920, CodedHeight: 1080, FrameRate: 30, BitDepth: 8, PixelFormat: "yuv420p", ChromaLocation: "left", ChromaSubsampling: "4:2:0", RefFrames: 4, Default: true}}, Audio: []canonical.AudioStream{{Index: 1, Codec: "aac", Profile: "lc", Channels: 2, ChannelLayout: "stereo", SampleRate: 48000, Default: true}}}
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

func assertPlayQueueSelection(t *testing.T, queue map[string]any, wantRatingKey string) {
	t.Helper()
	metadata, ok := queue["Metadata"].([]any)
	if !ok || len(metadata) != 1 {
		t.Fatalf("queue Metadata=%#v, want one selected item", queue["Metadata"])
	}
	selected, ok := metadata[0].(map[string]any)
	if !ok {
		t.Fatalf("queue Metadata[0]=%#v, want object", metadata[0])
	}
	selectedMetadataID, ok := queue["playQueueSelectedMetadataItemID"].(string)
	if !ok {
		t.Fatalf("playQueueSelectedMetadataItemID=%#v (%T), want string", queue["playQueueSelectedMetadataItemID"], queue["playQueueSelectedMetadataItemID"])
	}
	ratingKey, ok := selected["ratingKey"].(string)
	if !ok {
		t.Fatalf("Metadata[0].ratingKey=%#v (%T), want string", selected["ratingKey"], selected["ratingKey"])
	}
	if selectedMetadataID != ratingKey || ratingKey != wantRatingKey {
		t.Fatalf("metadata selection=%q ratingKey=%q, want %q", selectedMetadataID, ratingKey, wantRatingKey)
	}
	selectedItemID, ok := queue["playQueueSelectedItemID"].(float64)
	if !ok {
		t.Fatalf("playQueueSelectedItemID=%#v (%T), want number", queue["playQueueSelectedItemID"], queue["playQueueSelectedItemID"])
	}
	playQueueItemID, ok := selected["playQueueItemID"].(float64)
	if !ok {
		t.Fatalf("Metadata[0].playQueueItemID=%#v (%T), want number", selected["playQueueItemID"], selected["playQueueItemID"])
	}
	if selectedItemID != playQueueItemID {
		t.Fatalf("queue item selection=%v playQueueItemID=%v", selectedItemID, playQueueItemID)
	}
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
	if len(features) != 7 {
		t.Fatalf("features=%d, want 7", len(features))
	}
	if search := features[1].(map[string]any); search["type"] != "search" || search["key"] != "/hubs/search" {
		t.Fatalf("incomplete search feature: %#v", search)
	}
	timeline := features[5].(map[string]any)
	if timeline["type"] != "timeline" || timeline["scrobbleKey"] != "/:/scrobble" || timeline["unscrobbleKey"] != "/:/unscrobble" {
		t.Fatalf("incomplete timeline feature: %#v", timeline)
	}
	if universal := features[6].(map[string]any); universal["type"] != "universalsearch" || universal["key"] != "/library/search" {
		t.Fatalf("incomplete universal search feature: %#v", universal)
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
	assertPlayQueueSelection(t, queue, movieID)
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
		assertPlayQueueSelection(t, queue, episodeID)
		metadata := queue["Metadata"].([]any)[0].(map[string]any)
		if metadata["ratingKey"] != episodeID || metadata["type"] != "episode" {
			t.Fatalf("context=%s selected metadata=%#v", contextID, metadata)
		}
		if !strings.Contains(queue["playQueueSourceURI"].(string), url.QueryEscape("/library/metadata/"+contextID)) {
			t.Fatalf("context=%s source URI=%q", contextID, queue["playQueueSourceURI"])
		}
		queueID := int64(queue["playQueueID"].(float64))
		read := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/playQueues/"+strconv.FormatInt(queueID, 10), "application/json", nil))
		assertPlayQueueSelection(t, read, episodeID)
		if read["Metadata"].([]any)[0].(map[string]any)["ratingKey"] != episodeID {
			t.Fatalf("GET queue did not retain episode: %#v", read)
		}
	}
	showQueueTarget := "/playQueues?type=video&continuous=0&uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/"+showID)
	showQueue := decodeContainer(t, performRealRequest(handler, http.MethodPost, showQueueTarget, "application/json", nil))
	assertPlayQueueSelection(t, showQueue, episodeID)
	showSelected := showQueue["Metadata"].([]any)[0].(map[string]any)
	if showSelected["ratingKey"] != episodeID || showSelected["type"] != "episode" {
		t.Fatalf("show-level queue selected %#v", showSelected)
	}
	showQueueID := int64(showQueue["playQueueID"].(float64))
	showQueueRead := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/playQueues/"+strconv.FormatInt(showQueueID, 10), "application/json", nil))
	assertPlayQueueSelection(t, showQueueRead, episodeID)
	if showQueueRead["Metadata"].([]any)[0].(map[string]any)["ratingKey"] != episodeID {
		t.Fatalf("show-level GET queue lost selection: %#v", showQueueRead)
	}

	emptyShow := canonicalShow{SourceID: "empty-show", Title: "Empty Show", Seasons: []canonicalSeason{{Number: 1}}}
	handler.catalog.mu.Lock()
	handler.catalog.snapshot.Shows = append(handler.catalog.snapshot.Shows, emptyShow)
	handler.catalog.indexes = nil
	handler.catalog.mu.Unlock()
	emptyTarget := "/playQueues?type=video&uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/"+showObjectID(emptyShow))
	emptyResponse := performRealRequest(handler, http.MethodPost, emptyTarget, "application/json", nil)
	if emptyResponse.Code != http.StatusNotFound || emptyResponse.Header().Get("X-Test-Upstream") != "" {
		t.Fatalf("empty virtual show status=%d upstream=%q body=%s", emptyResponse.Code, emptyResponse.Header().Get("X-Test-Upstream"), emptyResponse.Body.String())
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

func TestRealCatalogSourceIdentity(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	movieID := movieObjectID(snapshot.Movies[0])
	episodeID := episodeObjectID(snapshot.Shows[0].Seasons[0].Episodes[0])

	for _, test := range []struct {
		name      string
		ratingKey string
	}{
		{name: "movie", ratingKey: movieID},
		{name: "episode", ratingKey: episodeID},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/library/metadata/"+test.ratingKey, "application/json", nil))
			if metadata["identifier"] != "com.plexapp.plugins.library" {
				t.Fatalf("metadata identifier=%#v", metadata["identifier"])
			}

			queueTarget := "/playQueues?uri=" + url.QueryEscape("server://"+machineID+"/com.plexapp.plugins.library/library/metadata/"+test.ratingKey)
			queue := decodeContainer(t, performRealRequest(handler, http.MethodPost, queueTarget, "application/json", nil))
			identifier, ok := queue["identifier"].(string)
			if !ok || identifier != "com.plexapp.plugins.library" {
				t.Fatalf("play queue identifier=%#v", queue["identifier"])
			}
			item := queue["Metadata"].([]any)[0].(map[string]any)
			sourceURI := "server://" + machineID + "/" + identifier + item["key"].(string)
			want := "server://" + machineID + "/com.plexapp.plugins.library/library/metadata/" + test.ratingKey
			if sourceURI != want {
				t.Fatalf("source URI=%q want=%q", sourceURI, want)
			}
			if strings.Contains(sourceURI, "/undefined/library/metadata/") {
				t.Fatalf("invalid source URI=%q", sourceURI)
			}

			queueID := int64(queue["playQueueID"].(float64))
			storedQueue := decodeContainer(t, performRealRequest(handler, http.MethodGet, "/playQueues/"+strconv.FormatInt(queueID, 10), "application/json", nil))
			if storedQueue["identifier"] != identifier {
				t.Fatalf("stored play queue identifier=%#v", storedQueue["identifier"])
			}
		})
	}
}

func TestFirstPlayableEpisodeSkipsUnplayableEntries(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	show := canonicalShow{SourceID: "ordered-show", Title: "Ordered Show", Seasons: []canonicalSeason{
		{Number: 1, Episodes: []canonicalEpisode{{Season: 1, Number: 1}, {SourceID: "first-playable", Season: 1, Number: 2, Extension: "mkv", Details: canonical.EpisodeDetails{Title: "First playable"}}}},
		{Number: 2, Episodes: []canonicalEpisode{{SourceID: "later", Season: 2, Number: 1, Extension: "mkv"}}},
	}}
	handler.catalog.mu.Lock()
	handler.catalog.snapshot.Shows = []canonicalShow{show}
	handler.catalog.mu.Unlock()
	selected, ok := handler.catalog.firstPlayableEpisode(showObjectID(show))
	if !ok || selected.SourceID != "first-playable" || selected.Season.Number != 1 || selected.Episode.Number != 2 {
		t.Fatalf("selected=%#v ok=%t", selected, ok)
	}
}

func TestCanceledPartRequestDoesNotRetryOrWrite502(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	partID := partObjectID("movie", snapshot.Movies[0].SourceID)
	var trace bytes.Buffer
	traced := traceRequests(handler, log.New(&trace, "", 0))
	request := httptest.NewRequest(http.MethodGet, "/library/parts/"+strconvFormatInt(partID)+"/file.mp4", nil)
	request.Header.Set("Range", "bytes=3434-")
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	traced.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusBadGateway || recorder.Body.Len() != 0 {
		t.Fatalf("canceled request status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(trace.String(), "status=499") {
		t.Fatalf("trace did not classify client cancellation: %s", trace.String())
	}
}

func TestMediaCopyTerminationOwnership(t *testing.T) {
	if got := mediaCopyTermination(context.Background(), nil, syscall.EPIPE, syscall.EPIPE); got != "downstream_broken_pipe" {
		t.Fatalf("broken pipe classified as %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := mediaCopyTermination(ctx, context.Canceled, nil, context.Canceled); got != "downstream_client_canceled" {
		t.Fatalf("client cancellation classified as %q", got)
	}
	upstreamErr := errors.New("upstream read")
	if got := mediaCopyTermination(context.Background(), upstreamErr, nil, upstreamErr); got != "upstream_read_error" {
		t.Fatalf("upstream failure classified as %q", got)
	}
}

func TestTruthfulMediaFactsProjectionPreservesVirtualIDs(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	facts := canonical.MediaFacts{State: "inspected", Container: "mkv", DurationMS: 5651114, BitrateKbps: 2075, Size: 1465406864,
		Video:     []canonical.VideoStream{{Index: 0, Codec: "hevc", Profile: "main", Level: 120, Width: 1920, Height: 1080, CodedWidth: 1920, CodedHeight: 1080, FrameRate: 24000.0 / 1001.0, BitDepth: 8, PixelFormat: "yuv420p", ChromaLocation: "left", ChromaSubsampling: "4:2:0", RefFrames: 1, ColorSpace: "bt709", ColorTransfer: "bt709", ColorPrimaries: "bt709", Default: true}},
		Audio:     []canonical.AudioStream{{Index: 1, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1", SampleRate: 48000, Language: "eng", Title: "Surround", Default: true}},
		Subtitles: []canonical.TextStream{{Index: 2, Codec: "subrip", Language: "eng"}, {Index: 3, Codec: "subrip", Language: "eng", Title: "SDH"}}}
	projected := handler.mediaModel("movie", "4601568", "mkv", 5651000, 2075, 0, true, true, facts)
	if projected.ID != int(mediaObjectID("movie", "4601568")) || projected.Part[0].ID != int(partObjectID("movie", "4601568")) {
		t.Fatalf("virtual IDs changed: %#v", projected)
	}
	if projected.VideoCodec != "hevc" || projected.VideoProfile != "main" || projected.AudioCodec != "aac" || projected.AudioChannels != 6 || projected.Duration != 5651114 || projected.Bitrate != 2075 {
		t.Fatalf("media projection=%#v", projected)
	}
	streams := projected.Part[0].Stream
	if len(streams) != 4 || streams[0].ID != int(videoStreamObjectID("movie", "4601568")) || streams[1].ID != int(audioStreamObjectID("movie", "4601568")) {
		t.Fatalf("streams=%#v", streams)
	}
	if streams[0].Codec != "hevc" || streams[0].Level != 120 || streams[0].BitDepth != 8 || streams[0].FrameRate == nil || streams[1].Channels != 6 || streams[1].ChannelLayout != "5.1" || streams[2].Codec != "srt" || streams[3].Title != "SDH" {
		t.Fatalf("stream semantics=%#v", streams)
	}
}

func TestUninspectedMetadataReturnsBeforeBackgroundProbe(t *testing.T) {
	probeStarted := make(chan struct{}, 1)
	releaseProbe := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/movie/user/pass/movie-slow.mkv") {
			select {
			case probeStarted <- struct{}{}:
			default:
			}
			<-releaseProbe
			w.Header().Set("Content-Range", "bytes 0-35/36")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123456789abcdefghijklmnopqrstuvwxyz"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(func() {
		close(releaseProbe)
		upstream.Close()
	})
	client, err := xtream.NewClient(upstream.Client(), upstream.URL, "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := canonicalSnapshot{Version: realCatalogVersion, Movies: []canonicalMovie{{SourceID: "movie-slow", Title: "Slow Probe", Extension: "mkv", Details: canonical.MovieDetails{ID: "movie-slow", Title: "Slow Probe", Extension: "mkv"}}}}
	runtime := &catalogRuntime{client: client, snapshot: snapshot, indexes: buildCatalogIndexes(snapshot), sources: map[string]*xtream.VODSource{}, capabilities: map[string]xtream.CapabilityProbe{}}
	handler := &realCatalogHandler{catalog: runtime, passthrough: http.NotFoundHandler(), httpClient: upstream.Client(), queues: map[int64]virtualPlayQueue{}}
	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		responseReady <- performRealRequest(handler, http.MethodGet, "/library/metadata/"+movieObjectID(snapshot.Movies[0])+"?checkFiles=1&includeExtras=1", "application/json", nil)
	}()
	select {
	case response := <-responseReady:
		if response.Code != http.StatusOK {
			t.Fatalf("metadata status=%d body=%s", response.Code, response.Body.String())
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("metadata response waited for lazy media inspection")
	}
	select {
	case <-probeStarted:
	case <-time.After(time.Second):
		t.Fatal("metadata response did not schedule background media inspection")
	}
}

func TestRokuDecisionDoesNotClaimDirectPlayForMultichannelAAC(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	snapshot.Movies[0].MediaFacts.Audio[0].Channels = 6
	handler.catalog.mu.Lock()
	handler.catalog.snapshot = *snapshot
	handler.catalog.indexes = nil
	handler.catalog.mu.Unlock()
	movieID := movieObjectID(snapshot.Movies[0])
	request := httptest.NewRequest(http.MethodGet, "/video/:/transcode/universal/decision?path="+url.QueryEscape("/library/metadata/"+movieID), nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Plex-Platform", "Roku")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "does not provide") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRokuDecisionEvaluatesSelectedAudioStream(t *testing.T) {
	tests := []struct {
		name             string
		audio            []canonical.AudioStream
		explicitAudio    int
		wantStatus       int
		wantSelectedPlex int
	}{
		{
			name: "primary compatible secondary incompatible uses canonical first fallback",
			audio: []canonical.AudioStream{
				{Index: 1, Codec: "eac3", Channels: 8, ChannelLayout: "7.1"},
				{Index: 2, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1"},
			},
			wantStatus:       http.StatusOK,
			wantSelectedPlex: int(audioStreamObjectID("movie", "movie-1")),
		},
		{
			name: "primary incompatible secondary compatible remains incompatible",
			audio: []canonical.AudioStream{
				{Index: 1, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1", Default: true},
				{Index: 2, Codec: "eac3", Channels: 6, ChannelLayout: "5.1"},
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "explicit compatible alternate",
			audio: []canonical.AudioStream{
				{Index: 1, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1", Default: true},
				{Index: 2, Codec: "eac3", Channels: 6, ChannelLayout: "5.1"},
			},
			explicitAudio:    int(stableNumericID('8', "xtream:movie:movie-1:audio:2")),
			wantStatus:       http.StatusOK,
			wantSelectedPlex: int(stableNumericID('8', "xtream:movie:movie-1:audio:2")),
		},
		{
			name: "explicit incompatible alternate",
			audio: []canonical.AudioStream{
				{Index: 1, Codec: "eac3", Channels: 6, ChannelLayout: "5.1", Default: true},
				{Index: 2, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1"},
			},
			explicitAudio: int(stableNumericID('8', "xtream:movie:movie-1:audio:2")),
			wantStatus:    http.StatusUnprocessableEntity,
		},
		{
			name: "single incompatible track preserves existing behavior",
			audio: []canonical.AudioStream{
				{Index: 1, Codec: "aac", Profile: "lc", Channels: 6, ChannelLayout: "5.1", Default: true},
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "no audio is deterministic",
			audio:      nil,
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, snapshot, _ := realCatalogTestHandler(t)
			snapshot.Movies[0].MediaFacts.Audio = test.audio
			handler.catalog.mu.Lock()
			handler.catalog.snapshot = *snapshot
			handler.catalog.indexes = nil
			handler.catalog.mu.Unlock()

			movieID := movieObjectID(snapshot.Movies[0])
			target := "/video/:/transcode/universal/decision?path=" + url.QueryEscape("/library/metadata/"+movieID)
			if test.explicitAudio != 0 {
				target += "&audioStreamID=" + strconv.Itoa(test.explicitAudio)
			}
			request := httptest.NewRequest(http.MethodGet, target, nil)
			request.Header.Set("Accept", "application/json")
			request.Header.Set("X-Plex-Platform", "Roku")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if response.Code != http.StatusOK || test.wantSelectedPlex == 0 {
				return
			}

			container := decodeContainer(t, response)
			metadata := container["Metadata"].([]any)[0].(map[string]any)
			media := metadata["Media"].([]any)[0].(map[string]any)
			part := media["Part"].([]any)[0].(map[string]any)
			streams := part["Stream"].([]any)
			selected := 0
			for _, raw := range streams {
				stream := raw.(map[string]any)
				if stream["streamType"] == float64(2) && stream["selected"] == true {
					selected = int(stream["id"].(float64))
				}
			}
			if selected != test.wantSelectedPlex {
				t.Fatalf("selected audio ID=%d want=%d", selected, test.wantSelectedPlex)
			}
		})
	}
}

func TestRokuDecisionRejectsAmbiguousOrUnknownAudioSelection(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	snapshot.Movies[0].MediaFacts.Audio = []canonical.AudioStream{
		{Index: 1, Codec: "eac3", Channels: 6, Default: true},
		{Index: 2, Codec: "aac", Channels: 2, Default: true},
	}
	handler.catalog.mu.Lock()
	handler.catalog.snapshot = *snapshot
	handler.catalog.indexes = nil
	handler.catalog.mu.Unlock()
	movieID := movieObjectID(snapshot.Movies[0])
	base := "/video/:/transcode/universal/decision?path=" + url.QueryEscape("/library/metadata/"+movieID)
	for _, target := range []string{base, base + "&audioStreamID=123"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("X-Plex-Platform", "Roku")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("target=%q status=%d body=%q", target, response.Code, response.Body.String())
		}
	}
}

func strconvFormatInt(value int64) string {
	return strings.TrimSpace(strconv.FormatInt(value, 10))
}
