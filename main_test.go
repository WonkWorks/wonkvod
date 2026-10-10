package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
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

func TestRequestTraceCapturesTransactionAndExcludesSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := log.New(&output, "", 0)
	handler := traceRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.wonkvod-test")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("test response"))
	}), logger)

	const directToken = "direct-sensitive-value"
	const nestedToken = "nested-sensitive-value"
	req := httptest.NewRequest(http.MethodGet, "/library/metadata/1001?checkFiles=1&X-Plex-Token="+directToken+"&context=playback%2526X-Plex-Token%253D"+nestedToken+"%2526tail%253D1", nil)
	req.RemoteAddr = "192.0.2.20:43210"
	req.Header.Set("X-Forwarded-For", "192.0.2.30")
	req.Header.Set("X-Plex-Client-Identifier", "roku-client")
	req.Header.Set("X-Plex-Product", "Plex for Roku")
	req.Header.Set("X-Plex-Version", "7.0.0")
	req.Header.Set("X-Plex-Device", "Roku")
	req.Header.Set("X-Plex-Device-Name", "Bedroom Roku")
	req.Header.Set("X-Plex-Platform", "Roku")
	req.Header.Set("X-Plex-Platform-Version", "14.5")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", "header-sensitive-value")
	req.Header.Set("Authorization", "Bearer authorization-sensitive-value")
	req.Header.Set("Cookie", "session=cookie-sensitive-value")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if response.Code != http.StatusPartialContent || response.Body.String() != "test response" {
		t.Fatalf("instrumented response = status %d body %q", response.Code, response.Body.String())
	}
	trace := output.String()
	if !strings.Contains(trace, `source="wonkvod"`) {
		t.Fatalf("trace missing default source marker: %s", trace)
	}
	for _, expected := range []string{
		"WONKVOD_TRACE",
		`method="GET"`,
		`path="/library/metadata/1001"`,
		`remote_addr="192.0.2.20:43210"`,
		`client_ip="192.0.2.30"`,
		`x_plex_client_identifier="roku-client"`,
		`x_plex_product="Plex for Roku"`,
		`x_plex_device_name="Bedroom Roku"`,
		`accept="application/json"`,
		`status=206`,
		`content_type="application/vnd.wonkvod-test"`,
		`bytes=13`,
	} {
		if !strings.Contains(trace, expected) {
			t.Errorf("trace missing %q: %s", expected, trace)
		}
	}
	if !regexp.MustCompile(`timestamp="[^"]+\.\d{3}(?:Z|[+-]\d{2}:\d{2})"`).MatchString(trace) {
		t.Errorf("trace lacks millisecond timestamp: %s", trace)
	}
	if !strings.Contains(trace, "[REDACTED]") {
		t.Errorf("trace lacks redaction marker: %s", trace)
	}
	for _, secret := range []string{directToken, nestedToken, "header-sensitive-value", "authorization-sensitive-value", "cookie-sensitive-value"} {
		if strings.Contains(trace, secret) {
			t.Errorf("trace leaked secret %q: %s", secret, trace)
		}
	}
}

func TestPMSModeDefaultsToSyntheticHandler(t *testing.T) {
	synthetic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity" {
			t.Fatalf("synthetic handler path = %q", r.URL.Path)
		}
		writeIdentity(w, r)
	})
	handler, err := handlerForPMSMode("", "", synthetic)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/identity", nil)
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), machineID) {
		t.Fatalf("default mode response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestPMSPassthroughPreservesRequestResponseAndTraceSource(t *testing.T) {
	const directToken = "proxy-direct-secret"
	const nestedToken = "proxy-nested-secret"
	var received struct {
		method, path, rawQuery, accept, byteRange, clientID, token, body string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.path = r.URL.EscapedPath()
		received.rawQuery = r.URL.RawQuery
		received.accept = r.Header.Get("Accept")
		received.byteRange = r.Header.Get("Range")
		received.clientID = r.Header.Get("X-Plex-Client-Identifier")
		received.token = r.Header.Get("X-Plex-Token")
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read upstream request body: %v", readErr)
		}
		received.body = string(body)

		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 10-19/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("first"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("second"))
	}))
	defer upstream.Close()

	handler, err := handlerForPMSMode("passthrough", upstream.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	var traceOutput bytes.Buffer
	handler = traceRequests(handler, log.New(&traceOutput, "", 0))
	rawQuery := "url=%2Flibrary%2Fmetadata%2F1&context=playback%2526X-Plex-Token%253D" + nestedToken
	request := httptest.NewRequest(http.MethodPost, "/library/parts/9/file.mp4?"+rawQuery, strings.NewReader("request-body"))
	request.Header.Set("Accept", "video/mp4")
	request.Header.Set("Range", "bytes=10-19")
	request.Header.Set("X-Plex-Client-Identifier", "roku-client")
	request.Header.Set("X-Plex-Token", directToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if received.method != http.MethodPost || received.path != "/library/parts/9/file.mp4" || received.rawQuery != rawQuery ||
		received.accept != "video/mp4" || received.byteRange != "bytes=10-19" || received.clientID != "roku-client" ||
		received.token != directToken || received.body != "request-body" {
		t.Errorf("upstream request was mutated: %#v", received)
	}
	if response.Code != http.StatusPartialContent || response.Header().Get("Content-Type") != "video/mp4" ||
		response.Header().Get("Content-Range") != "bytes 10-19/100" || response.Body.String() != "firstsecond" {
		t.Errorf("proxied response = status %d headers %#v body %q", response.Code, response.Header(), response.Body.String())
	}
	trace := traceOutput.String()
	if !strings.Contains(trace, `source="plex-identity"`) || !strings.Contains(trace, `status=206`) {
		t.Errorf("passthrough trace missing source/status: %s", trace)
	}
	for _, secret := range []string{directToken, nestedToken} {
		if strings.Contains(trace, secret) {
			t.Errorf("passthrough trace leaked secret %q", secret)
		}
	}
}

func TestPMSPassthroughKeepsIdentityHealthcheckFunctional(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity" {
			t.Fatalf("healthcheck path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"` + machineID + `"}}`))
	}))
	defer upstream.Close()
	handler, err := handlerForPMSMode("passthrough", upstream.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/identity", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), machineID) {
		t.Fatalf("passthrough health response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestPMSPassthroughMediaOwnsOnlyExactPartBytes(t *testing.T) {
	mediaBytes := bytes.Repeat([]byte("wonkvod-media-"), 300)
	mediaPath := t.TempDir() + "/test2.mp4"
	if err := os.WriteFile(mediaPath, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"source":"upstream","method":%q,"path":%q}`, r.Method, r.URL.Path)
	}))
	defer upstream.Close()

	handler, err := pmsPassthroughMediaHandler(upstream.URL, mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target string, configure func(*http.Request)) (*httptest.ResponseRecorder, string) {
		t.Helper()
		var traceOutput bytes.Buffer
		traced := traceRequests(handler, log.New(&traceOutput, "", 0))
		req := httptest.NewRequest(method, target, nil)
		if configure != nil {
			configure(req)
		}
		response := httptest.NewRecorder()
		traced.ServeHTTP(response, req)
		return response, traceOutput.String()
	}

	control, controlTrace := request(http.MethodGet, "/identity", nil)
	if control.Code != http.StatusOK || !strings.Contains(control.Body.String(), `"source":"upstream"`) ||
		!strings.Contains(controlTrace, `source="plex-identity"`) {
		t.Fatalf("control route was not proxied: status=%d body=%q trace=%s", control.Code, control.Body.String(), controlTrace)
	}

	nonmatching, nonmatchingTrace := request(http.MethodGet, "/library/parts/99/other/file.mp4", nil)
	if nonmatching.Code != http.StatusOK || !strings.Contains(nonmatching.Body.String(), `"source":"upstream"`) ||
		!strings.Contains(nonmatchingTrace, `source="plex-identity"`) {
		t.Fatalf("nonmatching Part was not proxied: status=%d body=%q trace=%s", nonmatching.Code, nonmatching.Body.String(), nonmatchingTrace)
	}

	const fakeToken = "passthrough-media-fake-secret"
	ranged, rangeTrace := request(http.MethodGet, sacrificialPartPath+"?X-Plex-Token="+fakeToken, func(req *http.Request) {
		req.Header.Set("Range", "bytes=100-199")
	})
	if ranged.Code != http.StatusPartialContent || ranged.Header().Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(mediaBytes)) ||
		ranged.Header().Get("Content-Length") != "100" || ranged.Header().Get("Accept-Ranges") != "bytes" ||
		ranged.Header().Get("Content-Type") != "video/mp4" || !bytes.Equal(ranged.Body.Bytes(), mediaBytes[100:200]) {
		t.Errorf("intercepted Range response = status %d headers %#v bytes %d", ranged.Code, ranged.Header(), ranged.Body.Len())
	}
	if !strings.Contains(rangeTrace, `source="wonkvod"`) || strings.Contains(rangeTrace, fakeToken) ||
		!strings.Contains(rangeTrace, "[REDACTED]") {
		t.Errorf("intercepted media trace source/redaction = %s", rangeTrace)
	}

	head, headTrace := request(http.MethodHead, sacrificialPartPath, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(mediaBytes)) ||
		head.Header().Get("Content-Type") != "video/mp4" || !strings.Contains(headTrace, `source="wonkvod"`) {
		t.Errorf("intercepted HEAD response = status %d headers %#v bytes %d trace=%s", head.Code, head.Header(), head.Body.Len(), headTrace)
	}

	full, fullTrace := request(http.MethodGet, sacrificialPartPath, nil)
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), mediaBytes) || !strings.Contains(fullTrace, `source="wonkvod"`) {
		t.Errorf("intercepted full response = status %d bytes %d trace=%s", full.Code, full.Body.Len(), fullTrace)
	}
}

func TestPMSPassthroughMediaKeepsIdentityHealthcheckProxied(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/identity" {
			t.Fatalf("healthcheck path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"` + machineID + `"}}`))
	}))
	defer upstream.Close()
	handler, err := handlerForPMSMode("passthrough-media", upstream.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/identity", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), machineID) {
		t.Fatalf("passthrough-media health response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestPMSPassthroughMetadataOwnsOnlyMovieObjectsAndExactPart(t *testing.T) {
	mediaBytes := bytes.Repeat([]byte("wonkvod-stage2-media-"), 300)
	mediaPath := t.TempDir() + "/test2.mp4"
	if err := os.WriteFile(mediaPath, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"source":"upstream","method":%q,"path":%q}`, r.Method, r.URL.Path)
	}))
	defer upstream.Close()

	handler, err := pmsPassthroughMetadataHandler(upstream.URL, mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, target, accept string, configure func(*http.Request)) (*httptest.ResponseRecorder, string) {
		t.Helper()
		var traceOutput bytes.Buffer
		traced := traceRequests(handler, log.New(&traceOutput, "", 0))
		req := httptest.NewRequest(method, target, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if configure != nil {
			configure(req)
		}
		response := httptest.NewRecorder()
		traced.ServeHTTP(response, req)
		return response, traceOutput.String()
	}

	for _, target := range []string{
		"/library/sections/1/all?type=1&sort=titleSort",
		"/library/sections/1/all?includeDetails=1&includeAdvanced=1&includeCollections=1&includeMeta=1",
		"/library/sections/1/all?excludeElements=Media&excludeFields=summary&X-Plex-Container-Start=0&X-Plex-Container-Size=3",
	} {
		response, trace := request(http.MethodGet, target, "application/json", nil)
		container := decodeJSONContainer(t, response)
		if response.Code != http.StatusOK || len(container.Metadata) != 1 || container.Metadata[0].RatingKey != "1" ||
			!strings.Contains(trace, `source="wonkvod"`) {
			t.Errorf("Stage 2 list %q = status %d container %#v trace=%s", target, response.Code, container, trace)
		}
	}
	listXML, listXMLTrace := request(http.MethodGet, "/library/sections/1/all?type=1", "text/xml", nil)
	listXMLContainer := decodeXMLContainer(t, listXML)
	if listXML.Code != http.StatusOK || len(listXMLContainer.Metadata) != 1 || listXMLContainer.Metadata[0].RatingKey != "1" ||
		!strings.Contains(listXMLTrace, `source="wonkvod"`) {
		t.Errorf("Stage 2 XML list = status %d container %#v trace=%s", listXML.Code, listXMLContainer, listXMLTrace)
	}

	for _, test := range []struct {
		accept string
		decode func(*testing.T, *httptest.ResponseRecorder) mediaContainer
	}{
		{accept: "application/json", decode: decodeJSONContainer},
		{accept: "text/xml", decode: decodeXMLContainer},
	} {
		response, trace := request(http.MethodGet,
			"/library/metadata/1?includeUserState=1&includeExtras=1&includeMarkers=1&includeChapters=1&checkFiles=1",
			test.accept, nil)
		container := test.decode(t, response)
		if response.Code != http.StatusOK || len(container.Metadata) != 1 || !strings.Contains(trace, `source="wonkvod"`) {
			t.Fatalf("Stage 2 detail accept=%q = status %d trace=%s", test.accept, response.Code, trace)
		}
		assertSacrificialIdentity(t, container.Metadata[0])
	}

	for _, control := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/identity"},
		{http.MethodGet, "/media/providers"},
		{http.MethodGet, "/library/sections"},
		{http.MethodGet, "/library/sections/1"},
		{http.MethodGet, "/library/sections/1/prefs"},
		{http.MethodGet, "/library/sections/1/firstCharacter"},
		{http.MethodGet, "/hubs"},
		{http.MethodGet, "/hubs/promoted"},
		{http.MethodGet, "/hubs/sections/1"},
		{http.MethodGet, "/library/metadata/1/related"},
		{http.MethodGet, sacrificialMovieThumb},
		{http.MethodGet, sacrificialMovieArt},
		{http.MethodGet, "/photo/:/transcode"},
		{http.MethodPost, "/playQueues"},
		{http.MethodGet, "/video/:/transcode/universal/decision"},
		{http.MethodPut, sacrificialPartPath},
		{http.MethodGet, "/:/timeline"},
		{http.MethodGet, "/status/sessions"},
		{http.MethodGet, "/activities"},
		{http.MethodGet, "/:/websockets/notifications"},
	} {
		response, trace := request(control.method, control.path, "application/json", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"source":"upstream"`) ||
			!strings.Contains(trace, `source="plex-identity"`) {
			t.Errorf("control route %s %s was not proxied: status=%d body=%q trace=%s", control.method, control.path, response.Code, response.Body.String(), trace)
		}
	}

	const token = "stage2-sensitive-token"
	ranged, trace := request(http.MethodGet, sacrificialPartPath+"?X-Plex-Token="+token, "video/mp4", func(req *http.Request) {
		req.Header.Set("Range", "bytes=100-199")
	})
	if ranged.Code != http.StatusPartialContent || ranged.Header().Get("Content-Range") != fmt.Sprintf("bytes 100-199/%d", len(mediaBytes)) ||
		!bytes.Equal(ranged.Body.Bytes(), mediaBytes[100:200]) || !strings.Contains(trace, `source="wonkvod"`) ||
		strings.Contains(trace, token) || !strings.Contains(trace, "[REDACTED]") {
		t.Errorf("Stage 2 Part Range = status %d headers %#v trace=%s", ranged.Code, ranged.Header(), trace)
	}
	head, headTrace := request(http.MethodHead, sacrificialPartPath, "", nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(mediaBytes)) ||
		!strings.Contains(headTrace, `source="wonkvod"`) {
		t.Errorf("Stage 2 Part HEAD = status %d headers %#v bytes=%d trace=%s", head.Code, head.Header(), head.Body.Len(), headTrace)
	}
	full, fullTrace := request(http.MethodGet, sacrificialPartPath, "", nil)
	if full.Code != http.StatusOK || !bytes.Equal(full.Body.Bytes(), mediaBytes) || !strings.Contains(fullTrace, `source="wonkvod"`) {
		t.Errorf("Stage 2 Part full GET = status %d bytes=%d trace=%s", full.Code, full.Body.Len(), fullTrace)
	}
}

func TestPMSPassthroughMetadataModeSelectorAndHealthcheck(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"MediaContainer":{"machineIdentifier":"` + machineID + `"}}`))
	}))
	defer upstream.Close()
	handler, err := handlerForPMSMode("passthrough-metadata", upstream.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/identity", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), machineID) {
		t.Fatalf("passthrough-metadata health response = status %d body %q", response.Code, response.Body.String())
	}
}

func TestSacrificialMovieProjectionDoesNotMutateSyntheticFixture(t *testing.T) {
	before := syntheticMovie()
	projected := sacrificialMovieDetail(true, true)
	after := syntheticMovie()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("sacrificial projection mutated the normal synthetic fixture")
	}
	assertSacrificialIdentity(t, projected)
	if after.RatingKey != movieRatingKey || after.Media[0].ID != 2001 || after.Media[0].Part[0].ID != 3001 {
		t.Fatalf("synthetic IDs changed: %#v", after)
	}
}

func assertSacrificialIdentity(t *testing.T, item movie) {
	t.Helper()
	if item.RatingKey != "1" || item.Key != "/library/metadata/1" || item.GUID != sacrificialMovieGUID ||
		item.Title != "test2" || item.LibrarySectionID != 1 || item.LibrarySectionKey != "/library/sections/1" ||
		item.LibrarySectionTitle != sacrificialLibraryName {
		t.Errorf("sacrificial item identity = %#v", item)
	}
	if item.Thumb != sacrificialMovieThumb || item.Art != sacrificialMovieArt {
		t.Errorf("sacrificial artwork = thumb %q art %q", item.Thumb, item.Art)
	}
	if len(item.Media) != 1 || item.Media[0].ID != 1 || item.Media[0].Duration != sacrificialDuration || item.Media[0].Bitrate != sacrificialBitrate ||
		len(item.Media[0].Part) != 1 || item.Media[0].Part[0].ID != 1 || item.Media[0].Part[0].Key != sacrificialPartPath ||
		item.Media[0].Part[0].Size != movieSize || item.Media[0].Part[0].Duration != sacrificialDuration {
		t.Errorf("sacrificial Media/Part identity = %#v", item.Media)
	}
	streams := item.Media[0].Part[0].Stream
	if len(streams) != 2 || streams[0].ID != 1 || streams[1].ID != 2 {
		t.Errorf("sacrificial Stream identity = %#v", streams)
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
			wantGuids := []plexGuid{{ID: "imdb://tt16270280"}, {ID: "tmdb://1361210"}, {ID: "tvdb://360893"}}
			if !reflect.DeepEqual(item.Guids, wantGuids) {
				t.Errorf("Guid = %#v", item.Guids)
			}
			wantRatings := []plexRating{
				{Image: "imdb://image.rating", Type: "audience", Value: 6.1},
				{Image: "themoviedb://image.rating", Type: "audience", Value: 6.4},
			}
			if !reflect.DeepEqual(item.Ratings, wantRatings) {
				t.Errorf("Rating = %#v", item.Ratings)
			}
			if item.AudienceRating != 6.1 || item.AudienceRatingImage != "imdb://image.rating" {
				t.Errorf("audienceRatingImage = %q", item.AudienceRatingImage)
			}
			if item.TitleSort != "'90s Christmas" || item.Studio != "Vortex Productions" || item.Tagline != "Rewind to the one that got away" {
				t.Errorf("real-PMS presentation scalars differ: %#v", item)
			}
			if item.Year != 2024 || item.OriginallyAvailableAt != "2024-11-01" || item.ContentRating != "TV-G" || item.Rating != 0 {
				t.Errorf("real-PMS date/rating fields differ: %#v", item)
			}
			if item.LibrarySectionID != 1 || item.LibrarySectionKey != "/library/sections/1" || item.LibrarySectionTitle != libraryName {
				t.Errorf("library section metadata = (%d, %q, %q)", item.LibrarySectionID, item.LibrarySectionKey, item.LibrarySectionTitle)
			}
			if item.LibrarySectionUUID != "" || item.OriginalTitle != "" {
				t.Errorf("real-PMS-absent detail identity fields were populated: %#v", item)
			}
			if item.Extras == nil || item.Extras.Size != 0 {
				t.Errorf("Extras = %#v", item.Extras)
			}
			coreSemantics(t, container)
		})
	}
}

func TestSyntheticMoviePlexIdentityExperimentIsIsolated(t *testing.T) {
	item := syntheticMovie()
	if item.GUID != "plex://movie/61a8a0880e2b362bf841341c" {
		t.Fatalf("primary guid = %q", item.GUID)
	}
	if item.Slug != "a-90s-christmas-2024" {
		t.Fatalf("slug = %q", item.Slug)
	}
	if item.Title != "A '90s Christmas" {
		t.Fatalf("title = %q", item.Title)
	}
	if item.RatingKey != "1001" || item.Key != "/library/metadata/1001" {
		t.Errorf("protected item identity changed: ratingKey=%q key=%q", item.RatingKey, item.Key)
	}
	if item.LibrarySectionID != 1 || item.LibrarySectionUUID != libraryUUID {
		t.Errorf("protected library identity changed: id=%d uuid=%q", item.LibrarySectionID, item.LibrarySectionUUID)
	}
	wantGuids := []plexGuid{{ID: "imdb://tt16270280"}, {ID: "tmdb://1361210"}, {ID: "tvdb://360893"}}
	if !reflect.DeepEqual(item.Guids, wantGuids) {
		t.Errorf("external Guid array = %#v, want %#v", item.Guids, wantGuids)
	}
	for _, guid := range item.Guids {
		if guid.ID == "tmdb://601860" {
			t.Error("stale synthetic external Guid remains")
		}
	}
	if item.Summary != "Workaholic lawyer Lucy Miller is celebrating her promotion alone on Christmas Eve when a mysterious rideshare experience transports her back to 1999. Reliving the holiday with her mom, sister and best friend - not to mention her high school crush - Lucy gets a second chance to understand where her relationships went wrong. Will this magical journey help her rewrite her future or leave her destined to be alone?" {
		t.Errorf("summary = %q", item.Summary)
	}
	core := coreSemantics(t, mediaContainer{Metadata: []movie{item}})
	if core.Media.ID != 2001 || core.Part.ID != 3001 || core.Video.ID != 4001 || core.Audio.ID != 4002 {
		t.Errorf("protected media identity changed: media=%d part=%d video=%d audio=%d", core.Media.ID, core.Part.ID, core.Video.ID, core.Audio.ID)
	}
	if core.Part.File != "/virtual/WonkPlex-Test-Movie.mp4" {
		t.Errorf("virtual Part path changed: %q", core.Part.File)
	}
}

func TestRealPMSPresentationMetadataAlignment(t *testing.T) {
	item := syntheticMovieDetail(true, true)
	if item.Title != "A '90s Christmas" || item.TitleSort != "'90s Christmas" || item.Slug != "a-90s-christmas-2024" || item.GUID != "plex://movie/61a8a0880e2b362bf841341c" {
		t.Errorf("primary presentation identity differs: %#v", item)
	}
	if item.Studio != "Vortex Productions" || item.Tagline != "Rewind to the one that got away" || item.Year != 2024 || item.OriginallyAvailableAt != "2024-11-01" || item.ContentRating != "TV-G" {
		t.Errorf("portable presentation fields differ: %#v", item)
	}
	if item.AudienceRating != 6.1 || item.AudienceRatingImage != "imdb://image.rating" {
		t.Errorf("audience rating differs: value=%v image=%q", item.AudienceRating, item.AudienceRatingImage)
	}
	wantGuids := []plexGuid{{ID: "imdb://tt16270280"}, {ID: "tmdb://1361210"}, {ID: "tvdb://360893"}}
	if !reflect.DeepEqual(item.Guids, wantGuids) {
		t.Errorf("Guid = %#v, want %#v", item.Guids, wantGuids)
	}
	wantRatings := []plexRating{
		{Image: "imdb://image.rating", Type: "audience", Value: 6.1},
		{Image: "themoviedb://image.rating", Type: "audience", Value: 6.4},
	}
	if !reflect.DeepEqual(item.Ratings, wantRatings) {
		t.Errorf("Rating = %#v, want %#v", item.Ratings, wantRatings)
	}
	wantColors := &ultraBlurColors{TopLeft: "332254", TopRight: "3b51ba", BottomRight: "8c4924", BottomLeft: "af1115"}
	if !reflect.DeepEqual(item.UltraBlurColors, wantColors) {
		t.Errorf("UltraBlurColors = %#v, want %#v", item.UltraBlurColors, wantColors)
	}
	if len(item.Images) != 2 || item.Images[0].URL != movieThumbPath || item.Images[1].URL != movieArtPath {
		t.Errorf("local artwork descriptors changed: %#v", item.Images)
	}
}

func TestLibraryUUIDIsStableAndConsistentAcrossResponses(t *testing.T) {
	const oldLibraryUUID = "8212ee84-76af-4cf3-8ec3-58b81b52ce9f"
	const expectedLibraryUUID = "10d9d706-97da-4e34-b740-55f0ef3e309c"
	if libraryUUID != expectedLibraryUUID {
		t.Fatalf("libraryUUID = %q, want stable fixture UUID %q", libraryUUID, expectedLibraryUUID)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(libraryUUID) {
		t.Fatalf("libraryUUID is not an RFC 4122 version 4 UUID: %q", libraryUUID)
	}

	responses := map[string]*httptest.ResponseRecorder{}
	record := func(name string, writer func(http.ResponseWriter)) {
		response := httptest.NewRecorder()
		writer(response)
		responses[name] = response
	}
	record("providers", func(w http.ResponseWriter) {
		request := httptest.NewRequest(http.MethodGet, "/media/providers?includePreferences=1", nil)
		writeMediaProviders(w, request)
	})
	record("sections", writeLibrarySections)
	record("section detail", writeLibrarySection)
	record("movie list", writeMovieLibrary)
	record("movie detail", func(w http.ResponseWriter) {
		request := httptest.NewRequest(http.MethodGet, "/library/metadata/1001?checkFiles=1&includeExtras=1", nil)
		request.Header.Set("Accept", "application/json")
		writeMovieMetadata(w, request)
	})
	record("promoted hubs", writePromotedHubs)
	record("section hubs", writeMovieHub)
	record("play queue", writePlayQueue)
	record("decision", func(w http.ResponseWriter) {
		request := httptest.NewRequest(http.MethodGet, "/video/:/transcode/universal/decision", nil)
		request.Header.Set("Accept", "application/json")
		writeDirectPlayDecision(w, request)
	})
	record("movie navigation", writeEmptyMovieNavigation)
	record("directory navigation", writeEmptyDirectoryNavigation)

	totalUUIDFields := 0
	for name, response := range responses {
		body := response.Body.String()
		if strings.Contains(body, oldLibraryUUID) {
			t.Errorf("%s response contains old synthetic UUID", name)
		}
		var document any
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode %s response: %v", name, err)
		}
		var inspect func(any)
		inspect = func(value any) {
			switch value := value.(type) {
			case map[string]any:
				for key, child := range value {
					if key == "uuid" || key == "librarySectionUUID" {
						totalUUIDFields++
						if child != libraryUUID {
							t.Errorf("%s %s = %#v, want %q", name, key, child, libraryUUID)
						}
					}
					inspect(child)
				}
			case []any:
				for _, child := range value {
					inspect(child)
				}
			}
		}
		inspect(document)
	}
	if totalUUIDFields == 0 {
		t.Fatal("no emitted library UUID fields were inspected")
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
			"titleSort": "string", "thumb": "string", "art": "string", "studio": "string", "summary": "string",
			"tagline": "string", "year": "integer", "duration": "integer", "addedAt": "integer",
			"updatedAt": "integer", "librarySectionID": "integer", "librarySectionKey": "string",
			"librarySectionTitle": "string", "contentRating": "string", "audienceRating": "decimal",
			"audienceRatingImage": "string", "originallyAvailableAt": "string",
			"Media": "array", "Image": "array", "UltraBlurColors": "object", "Guid": "array", "Rating": "array", "Extras": "object",
		})
		assertCommonPresentationShape(t, item, true)
		for index, value := range requireArray(t, item["Guid"], "Metadata[0].Guid", 3) {
			guid := requireObject(t, value, "Metadata[0].Guid")
			assertObjectShape(t, guid, fmt.Sprintf("Metadata[0].Guid[%d]", index), map[string]string{"id": "string"})
		}
		for index, value := range requireArray(t, item["Rating"], "Metadata[0].Rating", 2) {
			rating := requireObject(t, value, "Metadata[0].Rating")
			assertObjectShape(t, rating, fmt.Sprintf("Metadata[0].Rating[%d]", index), map[string]string{"image": "string", "type": "string", "value": "decimal"})
		}
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
	request := httptest.NewRequest(http.MethodGet, "/media/providers?includePreferences=1", nil)
	writeMediaProviders(response, request)
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
	for _, unsupported := range []string{
		"search", "match", "rate", "imagetranscoder", "continuewatching", "actions", "playlist", "collection",
		"manage", "queryParser", "subscribe", "universalsearch",
	} {
		if featureTypes[unsupported] {
			t.Errorf("unsupported feature %q was advertised", unsupported)
		}
	}
}

func TestMediaProviderContentMatchesSafePMSControlFields(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/media/providers?includePreferences=1", nil)
	writeMediaProviders(response, request)
	container := responseContainerObject(t, response)
	providers := requireArray(t, container["MediaProvider"], "MediaContainer.MediaProvider", 1)
	provider := requireObject(t, providers[0], "MediaContainer.MediaProvider[0]")
	if provider["identifier"] != "com.plexapp.plugins.library" || provider["title"] != "Library" ||
		provider["types"] != "video" || provider["protocols"] != "stream" {
		t.Fatalf("provider identity/capability declaration = %#v", provider)
	}

	features := requireArray(t, provider["Feature"], "Feature", 5)
	var content map[string]any
	for _, value := range features {
		feature := requireObject(t, value, "Feature")
		if feature["type"] == "content" {
			content = feature
		}
	}
	if content == nil {
		t.Fatal("content feature is missing")
	}
	assertObjectShape(t, content, "content Feature", map[string]string{"key": "string", "type": "string", "Directory": "array"})
	if content["key"] != "/library/sections" {
		t.Fatalf("content feature key = %#v", content["key"])
	}

	directories := requireArray(t, content["Directory"], "content Feature.Directory", 2)
	home := requireObject(t, directories[0], "content Feature.Directory[0]")
	assertObjectShape(t, home, "home Directory", map[string]string{"hubKey": "string", "title": "string"})
	if home["hubKey"] != "/hubs" || home["title"] != "Home" {
		t.Errorf("home Directory = %#v", home)
	}

	section := requireObject(t, directories[1], "content Feature.Directory[1]")
	assertObjectShape(t, section, "section Directory", map[string]string{
		"agent": "string", "hubKey": "string", "id": "string", "key": "string", "language": "string",
		"refreshing": "boolean", "scannedAt": "integer", "scanner": "string", "title": "string", "type": "string",
		"updatedAt": "integer", "uuid": "string", "Preferences": "object", "Pivot": "array",
	})
	if section["id"] != libraryID || section["key"] != "/library/sections/1" || section["hubKey"] != "/hubs/sections/1" ||
		section["type"] != "movie" || section["uuid"] != libraryUUID || section["agent"] != "tv.plex.agents.movie" ||
		section["scanner"] != "Plex Movie" || section["language"] != "en-US" || section["refreshing"] != false ||
		section["updatedAt"] != json.Number("1790910137") || section["scannedAt"] != json.Number("1790910139") {
		t.Errorf("section content identity = %#v", section)
	}
	pivots := requireArray(t, section["Pivot"], "section Directory.Pivot", 3)
	wantPivots := []map[string]any{
		{"id": "recommended", "key": "/hubs/sections/1", "type": "hub", "title": "Recommended", "context": "content.discover", "symbol": "star"},
		{"id": "library", "key": "/library/sections/1/all?type=1", "type": "list", "title": "Library", "context": "content.library", "symbol": "library"},
		{"id": "categories", "key": "/library/sections/1/categories", "type": "list", "title": "Categories", "context": "content.categories", "symbol": "stack"},
	}
	for index, value := range pivots {
		pivot := requireObject(t, value, "section Directory.Pivot")
		if !reflect.DeepEqual(pivot, wantPivots[index]) {
			t.Errorf("Pivot[%d] = %#v, want %#v", index, pivot, wantPivots[index])
		}
	}

	preferences := requireObject(t, section["Preferences"], "section Directory.Preferences")
	assertObjectShape(t, preferences, "section Directory.Preferences", map[string]string{"Setting": "array"})
	settings := requireArray(t, preferences["Setting"], "section Directory.Preferences.Setting", 3)
	wantValues := map[string]string{"hidden": "0", "ratingsSource": "rottentomatoes", "collectionMode": "2"}
	for _, value := range settings {
		setting := requireObject(t, value, "embedded Setting")
		id, _ := setting["id"].(string)
		if setting["value"] != wantValues[id] || setting["hidden"] != false || setting["advanced"] != false {
			t.Errorf("embedded preference %q = %#v", id, setting)
		}
		delete(wantValues, id)
	}
	if len(wantValues) != 0 {
		t.Errorf("missing embedded preferences: %#v", wantValues)
	}

	withoutPreferences := httptest.NewRecorder()
	writeMediaProviders(withoutPreferences, httptest.NewRequest(http.MethodGet, "/media/providers", nil))
	withoutContainer := responseContainerObject(t, withoutPreferences)
	withoutProviders := requireArray(t, withoutContainer["MediaProvider"], "MediaContainer.MediaProvider", 1)
	withoutProvider := requireObject(t, withoutProviders[0], "MediaContainer.MediaProvider[0]")
	withoutFeatures := requireArray(t, withoutProvider["Feature"], "Feature", 5)
	for _, value := range withoutFeatures {
		feature := requireObject(t, value, "Feature")
		if feature["type"] != "content" {
			continue
		}
		directories := requireArray(t, feature["Directory"], "content Feature.Directory", 2)
		section := requireObject(t, directories[1], "content Feature.Directory[1]")
		if _, present := section["Preferences"]; present {
			t.Error("provider response without includePreferences unexpectedly embeds Preferences")
		}
	}
}

func TestRootTruthfullyAdvertisesMediaProviders(t *testing.T) {
	response := httptest.NewRecorder()
	writeRoot(response)
	container := responseContainerObject(t, response)
	if container["mediaProviders"] != true {
		t.Fatalf("root mediaProviders = %#v, want true", container["mediaProviders"])
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

func TestLibrarySectionsUsePMSSectionSemantics(t *testing.T) {
	response := httptest.NewRecorder()
	writeLibrarySections(response)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{
		"size": "integer", "allowSync": "boolean", "title1": "string", "Directory": "array",
	})
	if container["allowSync"] != false || container["title1"] != "Plex Library" {
		t.Errorf("section container semantics = %#v", container)
	}

	directories := requireArray(t, container["Directory"], "MediaContainer.Directory", 1)
	section := requireObject(t, directories[0], "MediaContainer.Directory[0]")
	assertObjectShape(t, section, "MediaContainer.Directory[0]", map[string]string{
		"agent": "string", "allowSync": "boolean", "content": "boolean", "createdAt": "integer",
		"directory": "boolean", "filters": "boolean", "hidden": "integer", "key": "string",
		"language": "string", "metadataAgentProviderGroupId": "integer", "refreshing": "boolean",
		"scannedAt": "integer", "scanner": "string", "title": "string", "type": "string",
		"updatedAt": "integer", "uuid": "string",
	})
	if section["key"] != "1" {
		t.Errorf("section key = %#v, want short key 1", section["key"])
	}
	for _, field := range []string{"allowSync", "content", "directory", "filters"} {
		if section[field] != true {
			t.Errorf("section %s = %#v, want true", field, section[field])
		}
	}
	if section["hidden"] != json.Number("0") || section["metadataAgentProviderGroupId"] != json.Number("1") || section["refreshing"] != false {
		t.Errorf("section visibility/provider fields = %#v", section)
	}
	if section["uuid"] != libraryUUID || section["title"] != libraryName || section["type"] != "movie" ||
		section["agent"] != "tv.plex.agents.movie" || section["scanner"] != "Plex Movie" || section["language"] != "en-US" {
		t.Errorf("protected section identity changed: %#v", section)
	}
}

func TestLibrarySectionNavigationMatchesPMSModel(t *testing.T) {
	response := httptest.NewRecorder()
	writeLibrarySection(response)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{
		"size": "integer", "allowSync": "boolean", "art": "string", "content": "string", "identifier": "string",
		"librarySectionID": "integer", "mediaTagPrefix": "string", "mediaTagVersion": "integer", "thumb": "string",
		"title1": "string", "viewGroup": "string", "viewMode": "integer", "Directory": "array",
	})
	if container["allowSync"] != false || container["content"] != "secondary" || container["identifier"] != "com.plexapp.plugins.library" ||
		container["librarySectionID"] != json.Number("1") || container["viewGroup"] != "secondary" || container["viewMode"] != json.Number("65592") {
		t.Errorf("navigation container semantics = %#v", container)
	}

	directoryValues := requireArray(t, container["Directory"], "MediaContainer.Directory", 12)
	wantKeys := []string{"all", "unwatched", "newest", "recentlyAdded", "recentlyViewed", "onDeck", "collection", "genre", "year", "director", "actor", "search?type=1"}
	for index, value := range directoryValues {
		directory := requireObject(t, value, "Directory")
		key, _ := directory["key"].(string)
		if key != wantKeys[index] {
			t.Errorf("Directory[%d].key = %q, want %q", index, key, wantKeys[index])
		}
		switch key {
		case "collection", "genre", "year", "director", "actor":
			assertObjectShape(t, directory, "secondary Directory", map[string]string{"key": "string", "secondary": "boolean", "title": "string"})
			if directory["secondary"] != true {
				t.Errorf("Directory %q secondary = %#v", key, directory["secondary"])
			}
		case "search?type=1":
			assertObjectShape(t, directory, "search Directory", map[string]string{"key": "string", "prompt": "string", "search": "boolean", "title": "string"})
			if directory["search"] != true {
				t.Errorf("search Directory = %#v", directory)
			}
		default:
			assertObjectShape(t, directory, "primary Directory", map[string]string{"key": "string", "title": "string"})
		}
	}
}

func TestLibrarySectionPrefsExposeCapturedClientSettings(t *testing.T) {
	response := httptest.NewRecorder()
	writeLibrarySectionPrefs(response)
	container := responseContainerObject(t, response)
	assertObjectShape(t, container, "MediaContainer", map[string]string{"size": "integer", "Setting": "array"})
	settings := requireArray(t, container["Setting"], "MediaContainer.Setting", 3)
	wantValues := map[string]string{"hidden": "0", "ratingsSource": "rottentomatoes", "collectionMode": "2"}
	seen := map[string]bool{}
	for _, value := range settings {
		setting := requireObject(t, value, "Setting")
		assertObjectShape(t, setting, "Setting", map[string]string{
			"advanced": "boolean", "default": "string", "enumValues": "string", "group": "string", "hidden": "boolean",
			"id": "string", "label": "string", "summary": "string", "type": "string", "value": "string",
		})
		id, _ := setting["id"].(string)
		seen[id] = true
		if setting["value"] != wantValues[id] || setting["hidden"] != false || setting["advanced"] != false {
			t.Errorf("setting %q = %#v", id, setting)
		}
	}
	for id := range wantValues {
		if !seen[id] {
			t.Errorf("missing preference %q", id)
		}
	}
}

func TestAdvertisedSectionNavigationStubsAreTypedContainers(t *testing.T) {
	for name, writer := range map[string]func(http.ResponseWriter){
		"movie list": writeEmptyMovieNavigation,
		"directory":  writeEmptyDirectoryNavigation,
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writer(response)
			container := responseContainerObject(t, response)
			if container["size"] != json.Number("0") || container["identifier"] != "com.plexapp.plugins.library" {
				t.Errorf("typed empty container = %#v", container)
			}
			if name == "movie list" {
				requireArray(t, container["Metadata"], "MediaContainer.Metadata", 0)
			} else {
				requireArray(t, container["Directory"], "MediaContainer.Directory", 0)
			}
		})
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
	assertXMLAttributeSet(t, "Video", video.Attrs, "ratingKey", "key", "guid", "type", "title", "titleSort", "slug", "thumb", "art", "studio", "summary", "tagline", "year", "duration", "addedAt", "updatedAt", "librarySectionID", "librarySectionKey", "librarySectionTitle", "contentRating", "audienceRating", "audienceRatingImage", "originallyAvailableAt")
	wantChildren := []string{"Media", "Image", "Image", "UltraBlurColors", "Guid", "Guid", "Guid", "Rating", "Rating", "Extras"}
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
	for index := 4; index <= 6; index++ {
		assertXMLAttributeSet(t, "Guid", video.Children[index].Attrs, "id")
	}
	for index := 7; index <= 8; index++ {
		assertXMLAttributeSet(t, "Rating", video.Children[index].Attrs, "image", "type", "value")
	}
	assertXMLAttributeSet(t, "Extras", video.Children[9].Attrs, "size")

	if got := attributeValue(video.Attrs, "audienceRating"); got != "6.1" {
		t.Errorf("XML audienceRating = %q, want 6.1", got)
	}
	if got := attributeValue(video.Children[7].Attrs, "value"); got != "6.1" {
		t.Errorf("XML Rating[0] value = %q, want 6.1", got)
	}
	if got := attributeValue(video.Children[8].Attrs, "value"); got != "6.4" {
		t.Errorf("XML Rating[1] value = %q, want 6.4", got)
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
	if len(detailItem.Guids) != 3 || len(detailItem.Ratings) != 2 || detailItem.Extras == nil {
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

func TestPhotoTranscodeReturnsFixtureArtwork(t *testing.T) {
	tests := []struct {
		name           string
		requestURL     string
		expectedWidth  int
		expectedHeight int
	}{
		{
			name:           "poster",
			requestURL:     "/photo/:/transcode?url=/library/metadata/1001/thumb/1790899200&width=240&height=360",
			expectedWidth:  240,
			expectedHeight: 360,
		},
		{
			name:           "details background",
			requestURL:     "/photo/:/transcode?url=/library/metadata/1001/art/1790899200&width=853&minSize=0&crop=center&upscale=1&height=480",
			expectedWidth:  853,
			expectedHeight: 480,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.requestURL, nil)
			response := httptest.NewRecorder()
			writePhotoTranscode(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "image/png" {
				t.Fatalf("Content-Type = %q", got)
			}
			if strings.Contains(response.Header().Get("Content-Type"), "json") || strings.Contains(response.Body.String(), "MediaContainer") {
				t.Fatal("photo transcode returned a JSON/root response")
			}
			configuration, format, err := image.DecodeConfig(bytes.NewReader(response.Body.Bytes()))
			if err != nil {
				t.Fatalf("invalid image: %v", err)
			}
			if format != "png" || configuration.Width != test.expectedWidth || configuration.Height != test.expectedHeight {
				t.Fatalf("image = %s %dx%d, want png %dx%d", format, configuration.Width, configuration.Height, test.expectedWidth, test.expectedHeight)
			}
			if response.Body.Len() == 0 {
				t.Fatal("empty photo transcode response")
			}
		})
	}
}

func TestPhotoTranscodeRejectsUnknownArtworkSource(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/photo/:/transcode?url=/library/metadata/9999/art/1&width=853&height=480", nil)
	response := httptest.NewRecorder()
	writePhotoTranscode(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if strings.Contains(response.Header().Get("Content-Type"), "json") || strings.Contains(response.Body.String(), "MediaContainer") {
		t.Fatalf("unknown transcode source fell through to root identity: %q", response.Body.String())
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
