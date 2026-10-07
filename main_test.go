package main

import (
	"encoding/json"
	"encoding/xml"
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
