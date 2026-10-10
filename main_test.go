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
			if item.Year != 0 || item.OriginallyAvailableAt != "" || item.ContentRating != "" || item.Rating != 0 {
				t.Errorf("real-PMS-absent detail fields were populated: %#v", item)
			}
			if item.LibrarySectionID != 1 || item.LibrarySectionKey != "/library/sections/1" || item.LibrarySectionTitle != libraryName {
				t.Errorf("library section metadata = (%d, %q, %q)", item.LibrarySectionID, item.LibrarySectionKey, item.LibrarySectionTitle)
			}
			if item.LibrarySectionUUID != "" || item.OriginalTitle != "" || item.TitleSort != "" {
				t.Errorf("real-PMS-absent detail identity fields were populated: %#v", item)
			}
			if item.Extras == nil || item.Extras.Size != 0 {
				t.Errorf("Extras = %#v", item.Extras)
			}
			coreSemantics(t, container)
		})
	}
}

func TestSyntheticMoviePrimaryGUIDExperimentIsIsolated(t *testing.T) {
	item := syntheticMovie()
	if item.GUID != "plex://movie/f4a0eda242f58b0a4e24106a" {
		t.Fatalf("primary guid = %q", item.GUID)
	}
	if item.RatingKey != "1001" || item.Key != "/library/metadata/1001" {
		t.Errorf("protected item identity changed: ratingKey=%q key=%q", item.RatingKey, item.Key)
	}
	if item.LibrarySectionID != 1 || item.LibrarySectionUUID != libraryUUID {
		t.Errorf("protected library identity changed: id=%d uuid=%q", item.LibrarySectionID, item.LibrarySectionUUID)
	}
	if len(item.Guids) != 1 || item.Guids[0].ID != "tmdb://601860" {
		t.Errorf("external Guid array changed: %#v", item.Guids)
	}
	core := coreSemantics(t, mediaContainer{Metadata: []movie{item}})
	if core.Media.ID != 2001 || core.Part.ID != 3001 || core.Video.ID != 4001 || core.Audio.ID != 4002 {
		t.Errorf("protected media identity changed: media=%d part=%d video=%d audio=%d", core.Media.ID, core.Part.ID, core.Video.ID, core.Audio.ID)
	}
	if core.Part.File != "/virtual/WonkPlex-Test-Movie.mp4" {
		t.Errorf("virtual Part path changed: %q", core.Part.File)
	}
}

func jsonKind(value any) string {
	switch value := value.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		if strings.ContainsAny(value.String(), ".eE") {
			return "decimal"
		}
		return "integer"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return "unexpected"
	}
}

func decodeStrictJSON(t *testing.T, body []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode strict JSON: %v", err)
	}
	return document
}

func requireObject(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s type = %s, want object", path, jsonKind(value))
	}
	return object
}

func requireArray(t *testing.T, value any, path string, length int) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("%s type = %s, want array", path, jsonKind(value))
	}
	if len(array) != length {
		t.Fatalf("%s length = %d, want %d", path, len(array), length)
	}
	return array
}

func assertObjectShape(t *testing.T, object map[string]any, path string, expected map[string]string) {
	t.Helper()
	actualKeys := make(map[string]bool, len(object))
	for key := range object {
		actualKeys[key] = true
	}
	expectedKeys := make(map[string]bool, len(expected))
	for key := range expected {
		expectedKeys[key] = true
	}
	if !reflect.DeepEqual(actualKeys, expectedKeys) {
		t.Errorf("%s keys differ\nactual:   %#v\nexpected: %#v", path, actualKeys, expectedKeys)
	}
	for key, expectedKind := range expected {
		value, ok := object[key]
		if !ok {
			continue
		}
		if got := jsonKind(value); got != expectedKind {
			t.Errorf("%s.%s type = %s, want %s (value %#v)", path, key, got, expectedKind, value)
		}
	}
}

func assertCommonPresentationShape(t *testing.T, item map[string]any, detail bool) {
	t.Helper()
	images := requireArray(t, item["Image"], "Metadata[0].Image", 2)
	for index, value := range images {
		assertObjectShape(t, requireObject(t, value, "Image"), "Metadata[0].Image", map[string]string{
			"alt": "string", "type": "string", "url": "string",
		})
		_ = index
	}
	assertObjectShape(t, requireObject(t, item["UltraBlurColors"], "Metadata[0].UltraBlurColors"), "Metadata[0].UltraBlurColors", map[string]string{
		"topLeft": "string", "topRight": "string", "bottomRight": "string", "bottomLeft": "string",
	})

	mediaArray := requireArray(t, item["Media"], "Metadata[0].Media", 1)
	mediaObject := requireObject(t, mediaArray[0], "Metadata[0].Media[0]")
	assertObjectShape(t, mediaObject, "Metadata[0].Media[0]", map[string]string{
		"id": "integer", "duration": "integer", "bitrate": "integer", "width": "integer", "height": "integer",
		"aspectRatio": "decimal", "audioChannels": "integer", "audioCodec": "string", "videoCodec": "string",
		"videoResolution": "string", "container": "string", "videoFrameRate": "string", "optimizedForStreaming": "integer",
		"has64bitOffsets": "boolean", "videoProfile": "string", "hasVoiceActivity": "boolean", "Part": "array",
	})
	parts := requireArray(t, mediaObject["Part"], "Metadata[0].Media[0].Part", 1)
	partObject := requireObject(t, parts[0], "Metadata[0].Media[0].Part[0]")
	partShape := map[string]string{
		"id": "integer", "key": "string", "duration": "integer", "file": "string", "size": "integer",
		"container": "string", "has64bitOffsets": "boolean", "optimizedForStreaming": "boolean", "videoProfile": "string",
	}
	if detail {
		partShape["accessible"] = "boolean"
		partShape["exists"] = "boolean"
		partShape["Stream"] = "array"
	}
	assertObjectShape(t, partObject, "Metadata[0].Media[0].Part[0]", partShape)
	if !detail {
		return
	}

	streams := requireArray(t, partObject["Stream"], "Metadata[0].Media[0].Part[0].Stream", 2)
	assertObjectShape(t, requireObject(t, streams[0], "Stream[0]"), "Stream[0]", map[string]string{
		"id": "integer", "streamType": "integer", "default": "boolean", "codec": "string", "index": "integer",
		"bitrate": "integer", "bitDepth": "integer", "chromaLocation": "string", "chromaSubsampling": "string",
		"codedHeight": "integer", "codedWidth": "integer", "frameRate": "decimal", "hasScalingMatrix": "boolean",
		"height": "integer", "level": "integer", "profile": "string", "refFrames": "integer", "scanType": "string",
		"streamIdentifier": "string", "width": "integer", "displayTitle": "string", "extendedDisplayTitle": "string",
	})
	assertObjectShape(t, requireObject(t, streams[1], "Stream[1]"), "Stream[1]", map[string]string{
		"id": "integer", "streamType": "integer", "selected": "boolean", "default": "boolean", "codec": "string",
		"index": "integer", "channels": "integer", "bitrate": "integer", "samplingRate": "integer",
		"streamIdentifier": "string", "displayTitle": "string", "extendedDisplayTitle": "string",
	})
}

func TestStrictRealPMSJSONShapes(t *testing.T) {
	t.Run("detail", func(t *testing.T) {
		response := metadataResponseForURL(t, "/library/metadata/1001?checkFiles=1&includeExtras=1&includeOnDeck=1", "application/json")
		document := decodeStrictJSON(t, response.Body.Bytes())
		assertObjectShape(t, document, "$", map[string]string{"MediaContainer": "object"})
		container := requireObject(t, document["MediaContainer"], "MediaContainer")
		assertObjectShape(t, container, "MediaContainer", map[string]string{
			"size": "integer", "allowSync": "boolean", "identifier": "string", "librarySectionID": "integer",
			"librarySectionTitle": "string", "librarySectionUUID": "string", "mediaTagPrefix": "string",
			"mediaTagVersion": "integer", "Metadata": "array",
		})
		items := requireArray(t, container["Metadata"], "MediaContainer.Metadata", 1)
		item := requireObject(t, items[0], "MediaContainer.Metadata[0]")
		assertObjectShape(t, item, "MediaContainer.Metadata[0]", map[string]string{
			"ratingKey": "string", "key": "string", "guid": "string", "type": "string", "title": "string", "slug": "string",
			"thumb": "string", "art": "string", "summary": "string", "duration": "integer", "addedAt": "integer",
			"updatedAt": "integer", "librarySectionID": "integer", "librarySectionKey": "string",
			"librarySectionTitle": "string", "audienceRating": "decimal", "audienceRatingImage": "string",
			"Media": "array", "Image": "array", "UltraBlurColors": "object", "Guid": "array", "Rating": "array", "Extras": "object",
		})
		assertCommonPresentationShape(t, item, true)
		guid := requireObject(t, requireArray(t, item["Guid"], "Metadata[0].Guid", 1)[0], "Metadata[0].Guid[0]")
		assertObjectShape(t, guid, "Metadata[0].Guid[0]", map[string]string{"id": "string"})
		rating := requireObject(t, requireArray(t, item["Rating"], "Metadata[0].Rating", 1)[0], "Metadata[0].Rating[0]")
		assertObjectShape(t, rating, "Metadata[0].Rating[0]", map[string]string{"image": "string", "type": "string", "value": "decimal"})
		assertObjectShape(t, requireObject(t, item["Extras"], "Metadata[0].Extras"), "Metadata[0].Extras", map[string]string{"size": "integer"})
	})

	t.Run("compact list", func(t *testing.T) {
		response := httptest.NewRecorder()
		writeMovieLibrary(response)
		document := decodeStrictJSON(t, response.Body.Bytes())
		assertObjectShape(t, document, "$", map[string]string{"MediaContainer": "object"})
		container := requireObject(t, document["MediaContainer"], "MediaContainer")
		assertObjectShape(t, container, "MediaContainer", map[string]string{
			"size": "integer", "allowSync": "boolean", "identifier": "string", "librarySectionID": "integer",
			"librarySectionTitle": "string", "librarySectionUUID": "string", "mediaTagPrefix": "string",
			"mediaTagVersion": "integer", "art": "string", "thumb": "string", "title1": "string", "title2": "string",
			"viewGroup": "string", "viewMode": "integer", "Metadata": "array",
		})
		items := requireArray(t, container["Metadata"], "MediaContainer.Metadata", 1)
		item := requireObject(t, items[0], "MediaContainer.Metadata[0]")
		assertObjectShape(t, item, "MediaContainer.Metadata[0]", map[string]string{
			"ratingKey": "string", "key": "string", "guid": "string", "type": "string", "title": "string", "slug": "string",
			"thumb": "string", "art": "string", "summary": "string", "duration": "integer", "addedAt": "integer",
			"updatedAt": "integer", "audienceRating": "decimal", "audienceRatingImage": "string",
			"Media": "array", "Image": "array", "UltraBlurColors": "object",
		})
		assertCommonPresentationShape(t, item, false)
	})
}

func responseContainerObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	document := decodeStrictJSON(t, response.Body.Bytes())
	assertObjectShape(t, document, "$", map[string]string{"MediaContainer": "object"})
	return requireObject(t, document["MediaContainer"], "MediaContainer")
}

func hubObjects(t *testing.T, container map[string]any, count int) []map[string]any {
	t.Helper()
	values := requireArray(t, container["Hub"], "MediaContainer.Hub", count)
	hubs := make([]map[string]any, len(values))
	for index, value := range values {
		hubs[index] = requireObject(t, value, "MediaContainer.Hub")
	}
	return hubs
}

func TestMediaProvidersAdvertisesOnlySupportedPromotedCapability(t *testing.T) {
	response := httptest.NewRecorder()
	writeMediaProviders(response)
	container := responseContainerObject(t, response)
	providers := requireArray(t, container["MediaProvider"], "MediaContainer.MediaProvider", 1)
	provider := requireObject(t, providers[0], "MediaContainer.MediaProvider[0]")
	features := requireArray(t, provider["Feature"], "MediaContainer.MediaProvider[0].Feature", 5)

	promotedCount := 0
	featureTypes := map[string]bool{}
	for _, value := range features {
		feature := requireObject(t, value, "Feature")
		featureType, _ := feature["type"].(string)
		featureTypes[featureType] = true
		if featureType == "promoted" {
			promotedCount++
			assertObjectShape(t, feature, "promoted Feature", map[string]string{"key": "string", "type": "string"})
			if feature["key"] != "/hubs/promoted" {
				t.Errorf("promoted feature key = %#v", feature["key"])
			}
		}
	}
	if promotedCount != 1 {
		t.Fatalf("promoted feature count = %d", promotedCount)
	}
	for _, unsupported := range []string{"search", "match", "rate", "imagetranscoder", "continuewatching", "playlist", "collection", "universalsearch"} {
		if featureTypes[unsupported] {
			t.Errorf("unsupported feature %q was advertised", unsupported)
		}
	}
}

func TestPromotedHubsHavePMSHomeShape(t *testing.T) {
	response := httptest.NewRecorder()
	writePromotedHubs(response)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{
		"size": "integer", "allowSync": "boolean", "identifier": "string", "Hub": "array",
	})
	hubs := hubObjects(t, container, 3)

	assertObjectShape(t, hubs[0], "home.continue", map[string]string{
		"context": "string", "hubIdentifier": "string", "key": "string", "more": "boolean", "promoted": "boolean",
		"size": "integer", "style": "string", "title": "string", "type": "string",
	})
	assertObjectShape(t, hubs[1], "home.ondeck", map[string]string{
		"context": "string", "hubIdentifier": "string", "key": "string", "more": "boolean", "promoted": "boolean",
		"size": "integer", "style": "string", "title": "string", "type": "string",
	})
	assertObjectShape(t, hubs[2], "movie.recentlyadded.1", map[string]string{
		"context": "string", "hubIdentifier": "string", "hubKey": "string", "key": "string", "more": "boolean",
		"promoted": "boolean", "size": "integer", "style": "string", "title": "string", "type": "string", "Metadata": "array",
	})
	if hubs[0]["hubIdentifier"] != "home.continue" || hubs[1]["hubIdentifier"] != "home.ondeck" || hubs[2]["hubIdentifier"] != "movie.recentlyadded.1" {
		t.Errorf("promoted hub identifiers = %#v, %#v, %#v", hubs[0]["hubIdentifier"], hubs[1]["hubIdentifier"], hubs[2]["hubIdentifier"])
	}
	if metadata := requireArray(t, hubs[2]["Metadata"], "recently added Metadata", 1); requireObject(t, metadata[0], "recently added movie")["ratingKey"] != movieRatingKey {
		t.Errorf("recently added movie does not reference fixture ratingKey")
	}
}

func TestRokuHomeHubIdentifiersReturnDeclaredEmptyHubs(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/hubs?identifier=home.continue%2Chome.ondeck&includeDetails=1", nil)
	response := httptest.NewRecorder()
	writeHomeHubs(response, request)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{
		"size": "integer", "allowSync": "boolean", "identifier": "string", "Hub": "array",
	})
	hubs := hubObjects(t, container, 2)
	for index, identifier := range []string{"home.continue", "home.ondeck"} {
		if hubs[index]["hubIdentifier"] != identifier || hubs[index]["size"] != json.Number("0") || hubs[index]["more"] != false || hubs[index]["promoted"] != true {
			t.Errorf("%s declaration = %#v", identifier, hubs[index])
		}
		if _, present := hubs[index]["Metadata"]; present {
			t.Errorf("empty hub %s unexpectedly has Metadata", identifier)
		}
	}
}

func TestMovieSectionHubsUsePMSContextAndPromotionSemantics(t *testing.T) {
	response := httptest.NewRecorder()
	writeMovieHub(response)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{
		"size": "integer", "allowSync": "boolean", "identifier": "string", "librarySectionID": "integer",
		"librarySectionTitle": "string", "librarySectionUUID": "string", "Hub": "array",
	})
	hubs := hubObjects(t, container, 2)
	assertObjectShape(t, hubs[0], "movie.inprogress.1", map[string]string{
		"context": "string", "hubIdentifier": "string", "key": "string", "more": "boolean", "size": "integer",
		"style": "string", "title": "string", "type": "string",
	})
	assertObjectShape(t, hubs[1], "movie.recentlyadded.1", map[string]string{
		"context": "string", "hubIdentifier": "string", "hubKey": "string", "key": "string", "more": "boolean",
		"promoted": "boolean", "size": "integer", "style": "string", "title": "string", "type": "string", "Metadata": "array",
	})
	if hubs[0]["hubIdentifier"] != "movie.inprogress.1" || hubs[0]["context"] != "hub.movie.inprogress" {
		t.Errorf("continue-watching hub semantics = %#v", hubs[0])
	}
	if _, promoted := hubs[0]["promoted"]; promoted {
		t.Error("empty section continue-watching hub should not be promoted")
	}
	if hubs[1]["hubIdentifier"] != "movie.recentlyadded.1" || hubs[1]["context"] != "hub.movie.recentlyadded" || hubs[1]["promoted"] != true {
		t.Errorf("recently-added hub semantics = %#v", hubs[1])
	}
}

func xmlAttributeNames(attributes []xml.Attr) map[string]bool {
	names := make(map[string]bool, len(attributes))
	for _, attribute := range attributes {
		names[attribute.Name.Local] = true
	}
	return names
}

func assertXMLAttributeSet(t *testing.T, path string, attributes []xml.Attr, expected ...string) {
	t.Helper()
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}
	if got := xmlAttributeNames(attributes); !reflect.DeepEqual(got, want) {
		t.Errorf("%s attributes differ\nactual:   %#v\nexpected: %#v", path, got, want)
	}
}

type structuralXMLNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr          `xml:",any,attr"`
	Children []structuralXMLNode `xml:",any"`
}

func TestStrictRealPMSDetailXMLShape(t *testing.T) {
	response := metadataResponseForURL(t, "/library/metadata/1001?checkFiles=1&includeExtras=1&includeOnDeck=1", "application/xml")
	var root structuralXMLNode
	if err := xml.Unmarshal(response.Body.Bytes(), &root); err != nil {
		t.Fatalf("decode XML: %v", err)
	}
	assertXMLAttributeSet(t, "MediaContainer", root.Attrs, "size", "allowSync", "identifier", "librarySectionID", "librarySectionTitle", "librarySectionUUID", "mediaTagPrefix", "mediaTagVersion")
	if len(root.Children) != 1 || root.Children[0].XMLName.Local != "Video" {
		t.Fatalf("MediaContainer children = %#v", root.Children)
	}
	video := root.Children[0]
	assertXMLAttributeSet(t, "Video", video.Attrs, "ratingKey", "key", "guid", "type", "title", "slug", "thumb", "art", "summary", "duration", "addedAt", "updatedAt", "librarySectionID", "librarySectionKey", "librarySectionTitle", "audienceRating", "audienceRatingImage")
	wantChildren := []string{"Media", "Image", "Image", "UltraBlurColors", "Guid", "Rating", "Extras"}
	gotChildren := make([]string, len(video.Children))
	for index, child := range video.Children {
		gotChildren[index] = child.XMLName.Local
	}
	if !reflect.DeepEqual(gotChildren, wantChildren) {
		t.Errorf("Video child order/shape = %#v, want %#v", gotChildren, wantChildren)
	}
	if len(video.Children) != len(wantChildren) {
		return
	}
	mediaNode := video.Children[0]
	assertXMLAttributeSet(t, "Media", mediaNode.Attrs, "id", "duration", "bitrate", "width", "height", "aspectRatio", "audioChannels", "audioCodec", "videoCodec", "videoResolution", "container", "videoFrameRate", "optimizedForStreaming", "has64bitOffsets", "videoProfile", "hasVoiceActivity")
	if len(mediaNode.Children) != 1 || mediaNode.Children[0].XMLName.Local != "Part" {
		t.Fatalf("Media children = %#v", mediaNode.Children)
	}
	partNode := mediaNode.Children[0]
	assertXMLAttributeSet(t, "Part", partNode.Attrs, "accessible", "exists", "id", "key", "duration", "file", "size", "container", "has64bitOffsets", "optimizedForStreaming", "videoProfile")
	if len(partNode.Children) != 2 || partNode.Children[0].XMLName.Local != "Stream" || partNode.Children[1].XMLName.Local != "Stream" {
		t.Fatalf("Part children = %#v", partNode.Children)
	}
	assertXMLAttributeSet(t, "Video Stream", partNode.Children[0].Attrs, "id", "streamType", "default", "codec", "index", "bitrate", "bitDepth", "chromaLocation", "chromaSubsampling", "codedHeight", "codedWidth", "frameRate", "hasScalingMatrix", "height", "level", "profile", "refFrames", "scanType", "streamIdentifier", "width", "displayTitle", "extendedDisplayTitle")
	assertXMLAttributeSet(t, "Audio Stream", partNode.Children[1].Attrs, "id", "streamType", "selected", "default", "codec", "index", "channels", "bitrate", "samplingRate", "streamIdentifier", "displayTitle", "extendedDisplayTitle")
	assertXMLAttributeSet(t, "Image[0]", video.Children[1].Attrs, "alt", "type", "url")
	assertXMLAttributeSet(t, "Image[1]", video.Children[2].Attrs, "alt", "type", "url")
	assertXMLAttributeSet(t, "UltraBlurColors", video.Children[3].Attrs, "topLeft", "topRight", "bottomRight", "bottomLeft")
	assertXMLAttributeSet(t, "Guid", video.Children[4].Attrs, "id")
	assertXMLAttributeSet(t, "Rating", video.Children[5].Attrs, "image", "type", "value")
	assertXMLAttributeSet(t, "Extras", video.Children[6].Attrs, "size")

	if got := attributeValue(video.Attrs, "audienceRating"); got != "8.0" {
		t.Errorf("XML audienceRating = %q, want 8.0", got)
	}
	if got := attributeValue(video.Children[5].Attrs, "value"); got != "8.0" {
		t.Errorf("XML Rating value = %q, want 8.0", got)
	}
}

func attributeValue(attributes []xml.Attr, name string) string {
	for _, attribute := range attributes {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
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
