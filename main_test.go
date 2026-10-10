package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSanitizedURLRedactsPlexToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/identity?foo=bar&X-Plex-Token=secret", nil)
	got := sanitizedURL(req.URL)

	if strings.Contains(got, "secret") {
		t.Fatalf("sanitized URL leaked token: %s", got)
	}
	if !strings.Contains(got, "X-Plex-Token=%5BREDACTED%5D") {
		t.Fatalf("sanitized URL did not contain redaction marker: %s", got)
	}
}

func TestSanitizedURLRedactsPlexTokenEmbeddedInQueryValue(t *testing.T) {
	const sensitiveValue = "sensitive-value"
	for _, requestURL := range []string{
		"/identity?X-Plex-Playback-Id=playback%3Ffoo%3Dbar%26X-Plex-Token%3D" + sensitiveValue + "%26tail%3D1",
		"/identity?context=playback%2526X-Plex-Token%253D" + sensitiveValue + "%2526tail%253D1",
	} {
		req := httptest.NewRequest(http.MethodGet, requestURL, nil)
		got := sanitizedURL(req.URL)

		if strings.Contains(got, sensitiveValue) {
			t.Fatalf("sanitized URL leaked embedded token")
		}
		if !strings.Contains(got, "%5BREDACTED%5D") {
			t.Fatalf("sanitized URL did not contain redaction marker")
		}
	}
}

func TestIdentityContentNegotiation(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/identity", nil)
		req.Header.Set("Accept", "application/json")
		response := httptest.NewRecorder()

		writeIdentity(response, req)

		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}

		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}
	})

	t.Run("xml", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/identity", nil)
		response := httptest.NewRecorder()

		writeIdentity(response, req)

		if got := response.Header().Get("Content-Type"); got != "text/xml;charset=utf-8" {
			t.Fatalf("Content-Type = %q, want XML", got)
		}
		if !strings.Contains(response.Body.String(), "<MediaContainer") {
			t.Fatalf("response did not contain MediaContainer: %s", response.Body.String())
		}
	})
}

func metadataResponse(t *testing.T, accept string) *httptest.ResponseRecorder {
	t.Helper()
	return metadataResponseForURL(t, "/library/metadata/1001", accept)
}

func metadataResponseForURL(t *testing.T, requestURL, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, requestURL, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	response := httptest.NewRecorder()
	writeMovieMetadata(response, req)
	return response
}

func TestMetadataIncludesExtrasOnlyWhenRequested(t *testing.T) {
	for _, accept := range []string{"application/json", "application/xml"} {
		t.Run(accept, func(t *testing.T) {
			withExtras := metadataResponseForURL(t, "/library/metadata/1001?includeExtras=1", accept)
			withoutExtras := metadataResponseForURL(t, "/library/metadata/1001", accept)

			var withContainer, withoutContainer mediaContainer
			if accept == "application/json" {
				withContainer = decodeJSONContainer(t, withExtras)
				withoutContainer = decodeJSONContainer(t, withoutExtras)
			} else {
				withContainer = decodeXMLContainer(t, withExtras)
				withoutContainer = decodeXMLContainer(t, withoutExtras)
			}

			if got := withContainer.Metadata[0].Extras; got == nil || got.Size != 0 {
				t.Errorf("Extras with includeExtras=1 = %#v, want empty container", got)
			}
			if got := withoutContainer.Metadata[0].Extras; got != nil {
				t.Errorf("Extras without includeExtras = %#v, want absent", got)
			}
		})
	}
}

func TestMetadataIncludesPartAvailabilityOnlyWhenCheckingFiles(t *testing.T) {
	for _, accept := range []string{"application/json", "application/xml"} {
		t.Run(accept, func(t *testing.T) {
			withCheckFiles := metadataResponseForURL(t, "/library/metadata/1001?checkFiles=1&includeExtras=1&includeOnDeck=1", accept)
			withoutCheckFiles := metadataResponseForURL(t, "/library/metadata/1001?includeExtras=1&includeOnDeck=1", accept)

			var withContainer, withoutContainer mediaContainer
			if accept == "application/json" {
				withContainer = decodeJSONContainer(t, withCheckFiles)
				withoutContainer = decodeJSONContainer(t, withoutCheckFiles)
			} else {
				withContainer = decodeXMLContainer(t, withCheckFiles)
				withoutContainer = decodeXMLContainer(t, withoutCheckFiles)
			}

			withPart := withContainer.Metadata[0].Media[0].Part[0]
			if withPart.Accessible == nil || !bool(*withPart.Accessible) {
				t.Errorf("Part accessible with checkFiles=1 = %#v, want true", withPart.Accessible)
			}
			if withPart.Exists == nil || !bool(*withPart.Exists) {
				t.Errorf("Part exists with checkFiles=1 = %#v, want true", withPart.Exists)
			}

			withoutPart := withoutContainer.Metadata[0].Media[0].Part[0]
			if withoutPart.Accessible != nil || withoutPart.Exists != nil {
				t.Errorf("Part availability without checkFiles = (%#v, %#v), want absent", withoutPart.Accessible, withoutPart.Exists)
			}
		})
	}
}

func TestRichMovieDetailShape(t *testing.T) {
	for _, accept := range []string{"application/json", "application/xml"} {
		t.Run(accept, func(t *testing.T) {
			response := metadataResponseForURL(t, "/library/metadata/1001?checkFiles=1&includeExtras=1&includeOnDeck=1", accept)
			var container mediaContainer
			if accept == "application/json" {
				container = decodeJSONContainer(t, response)
			} else {
				container = decodeXMLContainer(t, response)
			}

			if len(container.Metadata) != 1 {
				t.Fatalf("metadata count = %d", len(container.Metadata))
			}
			item := container.Metadata[0]
			if item.Thumb != movieThumbPath || item.Art != movieArtPath || item.Slug == "" {
				t.Errorf("presentation paths/slug = (%q, %q, %q)", item.Thumb, item.Art, item.Slug)
			}
			if item.UltraBlurColors == nil || len(item.Images) != 2 {
				t.Errorf("presentation objects = colors:%#v images:%#v", item.UltraBlurColors, item.Images)
			}
			if len(item.Guids) != 1 || item.Guids[0].ID != "tmdb://601860" {
				t.Errorf("Guid = %#v", item.Guids)
			}
			if len(item.Ratings) != 1 || item.Ratings[0].Type != "audience" || item.Ratings[0].Value != 8.0 {
				t.Errorf("Rating = %#v", item.Ratings)
			}
			if item.AudienceRatingImage != "themoviedb://image.rating" {
				t.Errorf("audienceRatingImage = %q", item.AudienceRatingImage)
			}
			if item.Year != 2026 || item.OriginallyAvailableAt != "2026-10-01" || item.ContentRating != "NR" {
				t.Errorf("release metadata = (%d, %q, %q)", item.Year, item.OriginallyAvailableAt, item.ContentRating)
			}
			if item.LibrarySectionID != 1 || item.LibrarySectionKey != "/library/sections/1" || item.LibrarySectionTitle != libraryName {
				t.Errorf("library section metadata = (%d, %q, %q)", item.LibrarySectionID, item.LibrarySectionKey, item.LibrarySectionTitle)
			}
			if item.Extras == nil || item.Extras.Size != 0 {
				t.Errorf("Extras = %#v", item.Extras)
			}
			coreSemantics(t, container)
		})
	}
}

func TestMovieListAndDetailProjectionsDifferLikePMS(t *testing.T) {
	response := httptest.NewRecorder()
	writeMovieLibrary(response)
	listContainer := decodeJSONContainer(t, response)
	detailContainer := metadataContainer(true, true)

	listItem := listContainer.Metadata[0]
	detailItem := detailContainer.Metadata[0]
	if len(listItem.Guids) != 0 || len(listItem.Ratings) != 0 || listItem.Extras != nil {
		t.Errorf("list contains detail-only structures: %#v", listItem)
	}
	if listItem.LibrarySectionID != 0 || listItem.LibrarySectionKey != "" || listItem.LibrarySectionTitle != "" {
		t.Errorf("list contains item-level library section fields: %#v", listItem)
	}
	if got := len(listItem.Media[0].Part[0].Stream); got != 0 {
		t.Errorf("list stream count = %d, want 0", got)
	}
	if listItem.Media[0].Part[0].Accessible != nil || listItem.Media[0].Part[0].Exists != nil {
		t.Error("list Part contains checkFiles availability")
	}
	if len(detailItem.Guids) != 1 || len(detailItem.Ratings) != 1 || detailItem.Extras == nil {
		t.Errorf("detail structures missing: %#v", detailItem)
	}
	if got := len(detailItem.Media[0].Part[0].Stream); got != 2 {
		t.Errorf("detail stream count = %d, want 2", got)
	}
	if len(listItem.Images) != 2 || listItem.UltraBlurColors == nil || len(detailItem.Images) != 2 || detailItem.UltraBlurColors == nil {
		t.Error("list/detail presentation metadata is incomplete")
	}
}

func TestEveryAdvertisedArtworkPathReturnsValidImage(t *testing.T) {
	listContainer := movieLibraryContainer()
	detail := syntheticMovieDetail(true, true)
	paths := map[string]bool{
		listContainer.Thumb: true,
		listContainer.Art:   true,
		detail.Thumb:        true,
		detail.Art:          true,
	}
	for _, descriptor := range detail.Images {
		paths[descriptor.URL] = true
	}

	for path := range paths {
		t.Run(path, func(t *testing.T) {
			if _, ok := artworkForPath(path); !ok {
				t.Fatalf("advertised artwork path has no asset: %s", path)
			}
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			writeArtwork(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != "image/png" {
				t.Fatalf("Content-Type = %q", got)
			}
			configuration, format, err := image.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatalf("invalid image: %v", err)
			}
			if format != "png" || configuration.Width < 600 || configuration.Height < 450 {
				t.Fatalf("image = %s %dx%d", format, configuration.Width, configuration.Height)
			}
			if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("Access-Control-Allow-Origin = %q", got)
			}
		})
	}

	headRequest := httptest.NewRequest(http.MethodHead, movieThumbPath, nil)
	headResponse := httptest.NewRecorder()
	writeArtwork(headResponse, headRequest)
	if headResponse.Code != http.StatusOK || headResponse.Body.Len() != 0 || headResponse.Header().Get("Content-Length") == "" {
		t.Errorf("artwork HEAD response = status:%d bytes:%d length:%q", headResponse.Code, headResponse.Body.Len(), headResponse.Header().Get("Content-Length"))
	}
}

func decisionResponse(t *testing.T, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/video/:/transcode/universal/decision?session=session-1", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	response := httptest.NewRecorder()
	writeDirectPlayDecision(response, req)
	return response
}

func decodeJSONContainer(t *testing.T, response *httptest.ResponseRecorder) mediaContainer {
	t.Helper()
	var body mediaContainerResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON response: %v\n%s", err, response.Body.String())
	}
	return body.MediaContainer
}

func decodeXMLContainer(t *testing.T, response *httptest.ResponseRecorder) mediaContainer {
	t.Helper()
	var body mediaContainer
	if err := xml.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid XML response: %v\n%s", err, response.Body.String())
	}
	return body
}

func TestMetadataRepresentationNegotiation(t *testing.T) {
	jsonResponse := metadataResponse(t, "application/json")
	if got := jsonResponse.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("JSON Content-Type = %q", got)
	}
	decodeJSONContainer(t, jsonResponse)

	xmlResponse := metadataResponse(t, "text/xml")
	if got := xmlResponse.Header().Get("Content-Type"); got != "text/xml;charset=utf-8" {
		t.Fatalf("XML Content-Type = %q", got)
	}
	decodeXMLContainer(t, xmlResponse)

	for _, accept := range []string{"", "*/*", "application/octet-stream"} {
		response := metadataResponse(t, accept)
		if got := response.Header().Get("Content-Type"); got != "text/xml;charset=utf-8" {
			t.Errorf("default Content-Type for Accept %q = %q, want XML", accept, got)
		}
	}
}

func TestDecisionRepresentationNegotiation(t *testing.T) {
	jsonResponse := decisionResponse(t, "application/json")
	if got := jsonResponse.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("JSON Content-Type = %q", got)
	}
	jsonContainer := decodeJSONContainer(t, jsonResponse)

	xmlResponse := decisionResponse(t, "application/xml")
	if got := xmlResponse.Header().Get("Content-Type"); got != "text/xml;charset=utf-8" {
		t.Fatalf("XML Content-Type = %q", got)
	}
	xmlContainer := decodeXMLContainer(t, xmlResponse)

	for name, container := range map[string]mediaContainer{"json": jsonContainer, "xml": xmlContainer} {
		if container.MDEDecisionCode != 1000 || container.MDEDecisionText != "Direct play OK." {
			t.Errorf("%s decision = (%d, %q)", name, container.MDEDecisionCode, container.MDEDecisionText)
		}
		part := container.Metadata[0].Media[0].Part[0]
		if part.Decision != "directplay" {
			t.Errorf("%s Part decision = %q", name, part.Decision)
		}
		for _, stream := range part.Stream {
			if stream.Location != "direct" {
				t.Errorf("%s stream %d location = %q", name, stream.ID, stream.Location)
			}
		}
	}

	for _, accept := range []string{"", "*/*", "text/plain"} {
		response := decisionResponse(t, accept)
		if got := response.Header().Get("Content-Type"); got != "text/xml;charset=utf-8" {
			t.Errorf("default Content-Type for Accept %q = %q, want XML", accept, got)
		}
	}
}

type coreMediaSemantics struct {
	Media media
	Part  part
	Video stream
	Audio stream
}

func coreSemantics(t *testing.T, container mediaContainer) coreMediaSemantics {
	t.Helper()
	if len(container.Metadata) != 1 || len(container.Metadata[0].Media) != 1 {
		t.Fatalf("unexpected metadata/media cardinality: %#v", container.Metadata)
	}
	media := container.Metadata[0].Media[0]
	if len(media.Part) != 1 || len(media.Part[0].Stream) != 2 {
		t.Fatalf("unexpected part/stream cardinality: %#v", media.Part)
	}
	return coreMediaSemantics{Media: media, Part: media.Part[0], Video: media.Part[0].Stream[0], Audio: media.Part[0].Stream[1]}
}

func TestJSONAndXMLHaveIdenticalCoreMediaSemantics(t *testing.T) {
	for _, endpoint := range []struct {
		name string
		get  func(*testing.T, string) *httptest.ResponseRecorder
	}{
		{name: "metadata", get: metadataResponse},
		{name: "decision", get: decisionResponse},
	} {
		t.Run(endpoint.name, func(t *testing.T) {
			jsonCore := coreSemantics(t, decodeJSONContainer(t, endpoint.get(t, "application/json")))
			xmlCore := coreSemantics(t, decodeXMLContainer(t, endpoint.get(t, "application/xml")))
			if !reflect.DeepEqual(jsonCore, xmlCore) {
				t.Fatalf("JSON/XML media semantics differ:\nJSON: %#v\nXML:  %#v", jsonCore, xmlCore)
			}
		})
	}
}

func TestExactAnalyzedMediaFields(t *testing.T) {
	container := decodeJSONContainer(t, metadataResponse(t, "application/json"))
	core := coreSemantics(t, container)

	if core.Media.VideoFrameRate != "NTSC" || bool(core.Media.Has64BitOffsets) || bool(core.Media.HasVoiceActivity) {
		t.Errorf("unexpected Media analysis fields: %#v", core.Media)
	}
	if bool(core.Part.Has64BitOffsets) {
		t.Errorf("Part has64bitOffsets = true")
	}

	video := core.Video
	if video.Bitrate != 196 || video.BitDepth != 8 || video.CodedWidth != 1920 || video.CodedHeight != 1088 ||
		video.ChromaLocation != "left" || video.ChromaSubsampling != "4:2:0" || video.FrameRate == nil || *video.FrameRate != 30 ||
		video.Level != 41 || video.RefFrames != 4 || video.ScanType != "progressive" || video.HasScalingMatrix == nil || bool(*video.HasScalingMatrix) {
		t.Errorf("unexpected video analysis fields: %#v", video)
	}

	audio := core.Audio
	if audio.Bitrate != 128 || audio.SamplingRate != 48000 || audio.Channels != 2 {
		t.Errorf("unexpected audio analysis fields: %#v", audio)
	}
}

func TestUnsupportedSameFileClaimsAreAbsent(t *testing.T) {
	for _, endpoint := range []struct {
		name string
		get  func(*testing.T, string) *httptest.ResponseRecorder
	}{
		{name: "metadata", get: metadataResponse},
		{name: "decision", get: decisionResponse},
	} {
		for _, accept := range []string{"application/json", "application/xml"} {
			t.Run(endpoint.name+"/"+accept, func(t *testing.T) {
				body := endpoint.get(t, accept).Body.String()
				for _, unsupported := range []string{"indexes", "audioProfile", "audioChannelLayout", `profile=\"lc\"`, `\"profile\":\"lc\"`} {
					if strings.Contains(body, unsupported) {
						t.Errorf("response contains unsupported same-file claim %q: %s", unsupported, body)
					}
				}
			})
		}
	}
}

func TestMediaRangeServingKeepsPlexAndCORSHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/library/parts/3001/file.mp4", nil)
	req.Header.Set("Range", "bytes=0-0")
	response := httptest.NewRecorder()

	addCommonHeaders(response)
	http.ServeFile(response, req, testFile)

	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusPartialContent)
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 0-0/209970" {
		t.Errorf("Content-Range = %q", got)
	}
	if got := response.Header().Get("X-Plex-Protocol"); got != "1.0" {
		t.Errorf("X-Plex-Protocol = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
}
