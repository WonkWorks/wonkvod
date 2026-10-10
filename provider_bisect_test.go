package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestProviderBisectRealFixturesAreByteExact(t *testing.T) {
	handler, err := newProviderBisect("real")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		target      string
		accept      string
		contentType string
		want        []byte
	}{
		{"json", "/media/providers", "application/json", "application/json", realProvidersJSON},
		{"json preferences", "/media/providers?includePreferences=1", "application/json", "application/json", realProvidersPreferencesJSON},
		{"xml default", "/media/providers", "", "text/xml;charset=utf-8", realProvidersXML},
		{"xml preferences", "/media/providers?includePreferences=1", "text/xml", "text/xml;charset=utf-8", realProvidersPreferencesXML},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			request.Header.Set("Accept", test.accept)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != test.contentType {
				t.Fatalf("response = status %d content-type %q", response.Code, response.Header().Get("Content-Type"))
			}
			if !bytes.Equal(response.Body.Bytes(), test.want) {
				t.Fatalf("real fixture was not byte-identical: got %d bytes want %d", response.Body.Len(), len(test.want))
			}
		})
	}
}

func TestProviderBisectRealFixturePreferencesAreQuerySpecific(t *testing.T) {
	without, err := decodeProviderDocument(realProvidersJSON)
	if err != nil {
		t.Fatal(err)
	}
	with, err := decodeProviderDocument(realProvidersPreferencesJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := providerLibraryDirectory(primaryProvider(providerContainer(without)))["Preferences"]; ok {
		t.Fatal("no-preferences fixture unexpectedly contains Preferences")
	}
	settings := providerLibraryDirectory(primaryProvider(providerContainer(with)))["Preferences"].(map[string]any)["Setting"].([]any)
	if len(settings) != 19 {
		t.Fatalf("real preference count = %d, want 19", len(settings))
	}
}

func TestProviderBisectVariantsAreDeterministicAndValid(t *testing.T) {
	variants := []string{
		providerVariantPresentation,
		providerVariantContent,
		providerVariantPreferences,
		providerVariantTypesProtocols,
		providerVariantFeatures,
		providerVariantServerCapabilities,
		providerVariantWonkVOD,
	}
	for _, variant := range variants {
		t.Run(variant, func(t *testing.T) {
			handler, err := newProviderBisect(variant)
			if err != nil {
				t.Fatal(err)
			}
			for _, includePreferences := range []bool{false, true} {
				target := "/media/providers"
				if includePreferences {
					target += "?includePreferences=1"
				}
				first := providerRequest(t, handler, target, "application/json")
				second := providerRequest(t, handler, target, "application/json")
				if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) || !json.Valid(first.Body.Bytes()) {
					t.Fatalf("JSON variant is invalid or nondeterministic: %q", first.Body.String())
				}
				xmlResponse := providerRequest(t, handler, target, "text/xml")
				var value any
				if err := xml.Unmarshal(xmlResponse.Body.Bytes(), &value); err != nil {
					t.Fatalf("XML variant is invalid: %v", err)
				}
			}
		})
	}
	if _, err := newProviderBisect("not-a-variant"); err == nil {
		t.Fatal("invalid provider variant was accepted")
	}
}

func TestProviderBisectVariantsChangeOnlyTheirDeclaredGroup(t *testing.T) {
	realDocument, err := decodeProviderDocument(realProvidersPreferencesJSON)
	if err != nil {
		t.Fatal(err)
	}
	realContainer := providerContainer(realDocument)
	realProvider := primaryProvider(realContainer)

	presentation, _ := providerVariantDocument(providerVariantPresentation, true)
	presentationContainer := providerContainer(presentation)
	if presentationContainer["friendlyName"] != "WonkVOD" {
		t.Fatalf("presentation friendlyName = %#v", presentationContainer["friendlyName"])
	}
	delete(presentationContainer, "friendlyName")
	delete(realContainer, "friendlyName")
	if !reflect.DeepEqual(presentationContainer, realContainer) {
		t.Fatal("presentation variant changed a non-presentation field")
	}

	typesProtocols, _ := providerVariantDocument(providerVariantTypesProtocols, true)
	tpProvider := primaryProvider(providerContainer(typesProtocols))
	if tpProvider["types"] != "video" || tpProvider["protocols"] != "stream" {
		t.Fatalf("types/protocols = %#v/%#v", tpProvider["types"], tpProvider["protocols"])
	}

	preferences, _ := providerVariantDocument(providerVariantPreferences, true)
	prefs := providerLibraryDirectory(primaryProvider(providerContainer(preferences)))["Preferences"].(map[string]any)["Setting"].([]any)
	if len(prefs) != 3 {
		t.Fatalf("WonkVOD preference count = %d, want 3", len(prefs))
	}

	features, _ := providerVariantDocument(providerVariantFeatures, true)
	featureProvider := primaryProvider(providerContainer(features))
	if len(providerFeatures(featureProvider)) != 5 {
		t.Fatalf("WonkVOD feature count = %d, want 5", len(providerFeatures(featureProvider)))
	}
	if !reflect.DeepEqual(providerFeature(featureProvider, "content"), providerFeature(realProvider, "content")) {
		t.Fatal("features variant changed the independently bisected content feature")
	}

	capabilities, _ := providerVariantDocument(providerVariantServerCapabilities, true)
	capabilityContainer := providerContainer(capabilities)
	if len(capabilityContainer) != 9 || capabilityContainer["friendlyName"] != "WonkPlex_Identity_Test" {
		t.Fatalf("server-capabilities top-level fields = %d friendlyName=%#v", len(capabilityContainer), capabilityContainer["friendlyName"])
	}
}

func TestProviderBisectFullWonkVODPreservesFrozenCrashingSerializer(t *testing.T) {
	for _, includePreferences := range []bool{false, true} {
		got, err := providerVariantDocument(providerVariantWonkVOD, includePreferences)
		if err != nil {
			t.Fatal(err)
		}
		want := sacrificialMediaProvidersDocument(includePreferences)
		removeProviderTimelineKeys(primaryProvider(providerContainer(want)))
		if !reflect.DeepEqual(got, want) {
			t.Fatal("full WonkVOD diagnostic variant drifted from its frozen crashing serializer")
		}
	}
}

func TestNormalProviderAddsOnlyRequiredTimelineKeysToFrozenWonkVOD(t *testing.T) {
	for _, includePreferences := range []bool{false, true} {
		normal := sacrificialMediaProvidersDocument(includePreferences)
		frozen, err := providerVariantDocument(providerVariantWonkVOD, includePreferences)
		if err != nil {
			t.Fatal(err)
		}
		normalProvider := primaryProvider(providerContainer(normal))
		features := normalProvider["Feature"].([]any)
		if got := providerFeatureTypes(features); !reflect.DeepEqual(got, []string{"content", "metadata", "promoted", "playqueue", "timeline"}) {
			t.Fatalf("normal provider feature order = %v", got)
		}
		timeline := providerFeature(normalProvider, "timeline")
		if timeline["key"] != "/:/timeline" || timeline["type"] != "timeline" ||
			timeline["scrobbleKey"] != "/:/scrobble" || timeline["unscrobbleKey"] != "/:/unscrobble" || len(timeline) != 4 {
			t.Fatalf("normal provider timeline = %#v", timeline)
		}
		delete(timeline, "scrobbleKey")
		delete(timeline, "unscrobbleKey")
		if !reflect.DeepEqual(normal, frozen) {
			t.Fatal("normal provider changed beyond the two required timeline keys")
		}
	}
}

func TestProviderBisectKeepsExactStage3Ownership(t *testing.T) {
	mediaBytes := bytes.Repeat([]byte("provider-bisect-media-"), 100)
	mediaPath := t.TempDir() + "/test2.mp4"
	if err := os.WriteFile(mediaPath, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"source":"upstream","path":%q}`, r.URL.Path)
	}))
	defer upstream.Close()
	handler, err := pmsPassthroughProviderBisectHandler(upstream.URL, mediaPath, "real")
	if err != nil {
		t.Fatal(err)
	}

	var trace bytes.Buffer
	traced := traceRequests(handler, log.New(&trace, "", 0))
	provider := providerRequest(t, traced, "/media/providers", "application/json")
	if !bytes.Equal(provider.Body.Bytes(), realProvidersJSON) || !strings.Contains(trace.String(), `source="wonkvod"`) {
		t.Fatalf("provider route not locally owned: trace=%s", trace.String())
	}

	for _, target := range []string{"/hubs", "/hubs/promoted", "/hubs/sections/1", "/identity", "/library/metadata/1/related", "/photo/:/transcode", "/playQueues", "/video/:/transcode/universal/decision", "/:/timeline"} {
		trace.Reset()
		response := providerRequest(t, traced, target, "application/json")
		if !strings.Contains(response.Body.String(), `"source":"upstream"`) || !strings.Contains(trace.String(), `source="plex-identity"`) {
			t.Errorf("control route %q was not upstream: body=%q trace=%s", target, response.Body.String(), trace.String())
		}
	}

	for _, target := range []string{"/library/sections", "/library/sections/1", "/library/sections/1/all", "/library/metadata/1"} {
		trace.Reset()
		response := providerRequest(t, traced, target, "application/json")
		if response.Code != http.StatusOK || !json.Valid(response.Body.Bytes()) || !strings.Contains(trace.String(), `source="wonkvod"`) {
			t.Errorf("Stage 3 route %q changed: status=%d trace=%s", target, response.Code, trace.String())
		}
	}

	request := httptest.NewRequest(http.MethodGet, sacrificialPartPath, nil)
	request.Header.Set("Range", "bytes=10-29")
	response := httptest.NewRecorder()
	traced.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), mediaBytes[10:30]) {
		t.Fatalf("Stage 3 media Range changed: status=%d body=%q", response.Code, response.Body.Bytes())
	}
}

func TestProviderBisectModeSelectorDefaultsToReal(t *testing.T) {
	t.Setenv("WONKVOD_PROVIDER_VARIANT", "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"source":"upstream"}`))
	}))
	defer upstream.Close()
	handler, err := handlerForPMSMode("passthrough-provider-bisect", upstream.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	response := providerRequest(t, handler, "/media/providers", "application/json")
	if !bytes.Equal(response.Body.Bytes(), realProvidersJSON) {
		t.Fatal("provider-bisect default is not the exact real fixture")
	}
}

func providerRequest(t *testing.T, handler http.Handler, target, accept string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s = status %d body %q", target, response.Code, response.Body.String())
	}
	return response
}
