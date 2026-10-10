package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestProviderFeatureSubvariantsChangeOnlyFeatureArray(t *testing.T) {
	tests := []struct {
		variant string
		remove  []string
	}{
		{providerFeatureVariantReal, nil},
		{providerFeatureVariantTimelineKeys, nil},
		{providerFeatureVariantDiscovery, []string{"search", "universalsearch", "queryParser"}},
		{providerFeatureVariantPresentation, []string{"imagetranscoder", "continuewatching"}},
		{providerFeatureVariantLibraryActions, []string{"match", "rate", "actions", "playlist", "collection", "manage"}},
		{providerFeatureVariantSubscribe, []string{"subscribe"}},
		{providerFeatureVariantWonkVOD, []string{"search", "match", "rate", "imagetranscoder", "continuewatching", "actions", "playlist", "collection", "manage", "queryParser", "subscribe", "universalsearch"}},
		{providerFeatureVariantWonkVODFixed, []string{"search", "match", "rate", "imagetranscoder", "continuewatching", "actions", "playlist", "collection", "manage", "queryParser", "subscribe", "universalsearch"}},
	}
	for _, includePreferences := range []bool{false, true} {
		realBody, _ := realProviderFixture(includePreferences, true)
		realDocument, err := decodeProviderDocument(realBody)
		if err != nil {
			t.Fatal(err)
		}
		realProvider := primaryProvider(providerContainer(realDocument))
		realFeatures := append([]any(nil), realProvider["Feature"].([]any)...)
		delete(realProvider, "Feature")

		for _, test := range tests {
			t.Run(fmt.Sprintf("%s/preferences=%t", test.variant, includePreferences), func(t *testing.T) {
				document, err := providerFeatureVariantDocument(test.variant, includePreferences)
				if err != nil {
					t.Fatal(err)
				}
				provider := primaryProvider(providerContainer(document))
				features := provider["Feature"].([]any)
				delete(provider, "Feature")
				if !reflect.DeepEqual(document, realDocument) {
					t.Fatal("feature subvariant changed a non-Feature provider field")
				}

				remove := map[string]bool{}
				for _, featureType := range test.remove {
					remove[featureType] = true
				}
				var expectedTypes []string
				for _, value := range realFeatures {
					featureType := value.(map[string]any)["type"].(string)
					if !remove[featureType] {
						expectedTypes = append(expectedTypes, featureType)
					}
				}
				if got := providerFeatureTypes(features); !reflect.DeepEqual(got, expectedTypes) {
					t.Fatalf("retained feature order = %v, want %v", got, expectedTypes)
				}

				for _, value := range features {
					feature := value.(map[string]any)
					featureType := feature["type"].(string)
					realFeature := featureByType(realFeatures, featureType)
					if test.variant == providerFeatureVariantTimelineKeys && featureType == "timeline" ||
						test.variant == providerFeatureVariantWonkVOD && featureType == "timeline" {
						withoutKeys := cloneMap(realFeature)
						delete(withoutKeys, "scrobbleKey")
						delete(withoutKeys, "unscrobbleKey")
						if !reflect.DeepEqual(feature, withoutKeys) {
							t.Fatalf("timeline feature = %#v, want %#v", feature, withoutKeys)
						}
					} else if !reflect.DeepEqual(feature, realFeature) {
						t.Fatalf("retained feature %q changed", featureType)
					}
				}
			})
		}
	}
}

func TestProviderFeatureTimelineKeysHasExactRecursiveDiff(t *testing.T) {
	for _, includePreferences := range []bool{false, true} {
		realBody, _ := realProviderFixture(includePreferences, true)
		realDocument, err := decodeProviderDocument(realBody)
		if err != nil {
			t.Fatal(err)
		}
		got, err := providerFeatureVariantDocument(providerFeatureVariantTimelineKeys, includePreferences)
		if err != nil {
			t.Fatal(err)
		}
		realTimeline := providerFeature(primaryProvider(providerContainer(realDocument)), "timeline")
		gotTimeline := providerFeature(primaryProvider(providerContainer(got)), "timeline")
		if realTimeline["scrobbleKey"] != "/:/scrobble" || realTimeline["unscrobbleKey"] != "/:/unscrobble" {
			t.Fatalf("unexpected real timeline fixture: %#v", realTimeline)
		}
		delete(realTimeline, "scrobbleKey")
		delete(realTimeline, "unscrobbleKey")
		if !reflect.DeepEqual(got, realDocument) || !reflect.DeepEqual(gotTimeline, realTimeline) {
			t.Fatal("timeline-keys has a difference beyond the two removed timeline keys")
		}
	}
}

func TestProviderFeatureWonkVODTimelineFixedAddsOnlyTimelineKeys(t *testing.T) {
	for _, includePreferences := range []bool{false, true} {
		crashing, err := providerFeatureVariantDocument(providerFeatureVariantWonkVOD, includePreferences)
		if err != nil {
			t.Fatal(err)
		}
		fixed, err := providerFeatureVariantDocument(providerFeatureVariantWonkVODFixed, includePreferences)
		if err != nil {
			t.Fatal(err)
		}
		crashingTimeline := providerFeature(primaryProvider(providerContainer(crashing)), "timeline")
		fixedProvider := primaryProvider(providerContainer(fixed))
		fixedTimeline := providerFeature(fixedProvider, "timeline")
		if fixedTimeline["key"] != "/:/timeline" || fixedTimeline["type"] != "timeline" ||
			fixedTimeline["scrobbleKey"] != "/:/scrobble" || fixedTimeline["unscrobbleKey"] != "/:/unscrobble" {
			t.Fatalf("timeline-fixed feature = %#v", fixedTimeline)
		}
		if got := providerFeatureTypes(fixedProvider["Feature"].([]any)); !reflect.DeepEqual(got, []string{"content", "metadata", "promoted", "playqueue", "timeline"}) {
			t.Fatalf("timeline-fixed feature order = %v", got)
		}
		delete(fixedTimeline, "scrobbleKey")
		delete(fixedTimeline, "unscrobbleKey")
		if !reflect.DeepEqual(fixed, crashing) || !reflect.DeepEqual(fixedTimeline, crashingTimeline) {
			t.Fatal("timeline-fixed differs from wonkvod beyond the two restored timeline keys")
		}
	}
}

func TestProviderFeatureSelectorValidationAndNegotiation(t *testing.T) {
	for variant := range providerFeatureVariants {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("WONKVOD_PROVIDER_FEATURE_VARIANT", variant)
			handler, err := newProviderBisect(providerVariantFeaturesBisect)
			if err != nil {
				t.Fatal(err)
			}
			for _, accept := range []string{"application/json", "text/xml"} {
				response := providerRequest(t, handler, "/media/providers?includePreferences=1", accept)
				if accept == "application/json" && !json.Valid(response.Body.Bytes()) {
					t.Fatal("feature variant returned invalid JSON")
				}
				if accept == "text/xml" && !strings.HasPrefix(response.Body.String(), "<?xml") {
					t.Fatal("feature variant returned invalid XML representation")
				}
			}
		})
	}
	t.Setenv("WONKVOD_PROVIDER_FEATURE_VARIANT", "invalid")
	if _, err := newProviderBisect(providerVariantFeaturesBisect); err == nil {
		t.Fatal("invalid feature subvariant was accepted")
	}
	if _, err := newProviderBisect(providerVariantReal); err != nil {
		t.Fatalf("feature selector affected an unrelated provider variant: %v", err)
	}
}

func TestProviderFeatureBisectKeepsStage3Routing(t *testing.T) {
	t.Setenv("WONKVOD_PROVIDER_FEATURE_VARIANT", providerFeatureVariantTimelineKeys)
	mediaBytes := bytes.Repeat([]byte("feature-bisect-media-"), 100)
	mediaPath := t.TempDir() + "/test2.mp4"
	if err := os.WriteFile(mediaPath, mediaBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"source":"upstream","path":%q}`, r.URL.Path)
	}))
	defer upstream.Close()
	handler, err := pmsPassthroughProviderBisectHandler(upstream.URL, mediaPath, providerVariantFeaturesBisect)
	if err != nil {
		t.Fatal(err)
	}

	var trace bytes.Buffer
	traced := traceRequests(handler, log.New(&trace, "", 0))
	providerRequest(t, traced, "/media/providers", "application/json")
	if !strings.Contains(trace.String(), `source="wonkvod"`) {
		t.Fatalf("provider source changed: %s", trace.String())
	}
	for _, target := range []string{"/hubs", "/hubs/promoted", "/identity", "/library/metadata/1/related", "/playQueues", "/:/timeline", "/:/scrobble", "/:/unscrobble"} {
		trace.Reset()
		response := providerRequest(t, traced, target, "application/json")
		if !strings.Contains(response.Body.String(), `"source":"upstream"`) || !strings.Contains(trace.String(), `source="plex-identity"`) {
			t.Fatalf("control route %q changed: body=%q trace=%s", target, response.Body.String(), trace.String())
		}
	}
	for _, target := range []string{"/library/sections", "/library/sections/1/all", "/library/metadata/1"} {
		trace.Reset()
		providerRequest(t, traced, target, "application/json")
		if !strings.Contains(trace.String(), `source="wonkvod"`) {
			t.Fatalf("Stage 3 route %q changed: %s", target, trace.String())
		}
	}
}

func providerFeatureTypes(features []any) []string {
	result := make([]string, 0, len(features))
	for _, value := range features {
		result = append(result, value.(map[string]any)["type"].(string))
	}
	return result
}

func featureByType(features []any, featureType string) map[string]any {
	for _, value := range features {
		feature := value.(map[string]any)
		if feature["type"] == featureType {
			return feature
		}
	}
	return nil
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
