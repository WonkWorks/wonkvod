package main

import (
	_ "embed"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	providerVariantReal               = "real"
	providerVariantPresentation       = "presentation"
	providerVariantContent            = "content"
	providerVariantPreferences        = "preferences"
	providerVariantTypesProtocols     = "types-protocols"
	providerVariantFeatures           = "features"
	providerVariantFeaturesBisect     = "features-bisect"
	providerVariantServerCapabilities = "server-capabilities"
	providerVariantWonkVOD            = "wonkvod"
)

var providerVariants = map[string]bool{
	providerVariantReal:               true,
	providerVariantPresentation:       true,
	providerVariantContent:            true,
	providerVariantPreferences:        true,
	providerVariantTypesProtocols:     true,
	providerVariantFeatures:           true,
	providerVariantFeaturesBisect:     true,
	providerVariantServerCapabilities: true,
	providerVariantWonkVOD:            true,
}

const (
	providerFeatureVariantReal           = "real"
	providerFeatureVariantTimelineKeys   = "timeline-keys"
	providerFeatureVariantDiscovery      = "discovery"
	providerFeatureVariantPresentation   = "presentation"
	providerFeatureVariantLibraryActions = "library-actions"
	providerFeatureVariantSubscribe      = "subscribe"
	providerFeatureVariantWonkVOD        = "wonkvod"
)

var providerFeatureVariants = map[string]bool{
	providerFeatureVariantReal:           true,
	providerFeatureVariantTimelineKeys:   true,
	providerFeatureVariantDiscovery:      true,
	providerFeatureVariantPresentation:   true,
	providerFeatureVariantLibraryActions: true,
	providerFeatureVariantSubscribe:      true,
	providerFeatureVariantWonkVOD:        true,
}

// These token-free fixtures were captured directly from the sacrificial PMS
// for the exact library used by the Stage 3 Roku control.
//
//go:embed fixtures/provider/providers.json
var realProvidersJSON []byte

//go:embed fixtures/provider/providers-preferences.json
var realProvidersPreferencesJSON []byte

//go:embed fixtures/provider/providers.xml
var realProvidersXML []byte

//go:embed fixtures/provider/providers-preferences.xml
var realProvidersPreferencesXML []byte

type providerBisect struct {
	variant        string
	featureVariant string
}

func newProviderBisect(variant string) (http.Handler, error) {
	variant = strings.ToLower(strings.TrimSpace(variant))
	if variant == "" {
		variant = providerVariantReal
	}
	if !providerVariants[variant] {
		return nil, fmt.Errorf("unsupported WONKVOD_PROVIDER_VARIANT %q", variant)
	}
	featureVariant := strings.ToLower(strings.TrimSpace(os.Getenv("WONKVOD_PROVIDER_FEATURE_VARIANT")))
	if featureVariant == "" {
		featureVariant = providerFeatureVariantReal
	}
	if variant == providerVariantFeaturesBisect && !providerFeatureVariants[featureVariant] {
		return nil, fmt.Errorf("unsupported WONKVOD_PROVIDER_FEATURE_VARIANT %q", featureVariant)
	}
	return providerBisect{variant: variant, featureVariant: featureVariant}, nil
}

func (handler providerBisect) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	includePreferences := r.URL.Query().Get("includePreferences") == "1"
	if handler.variant == providerVariantReal {
		body, contentType := realProviderFixture(includePreferences, acceptsJSON(r))
		writeExactProviderFixture(w, r, contentType, body)
		return
	}

	var document map[string]any
	var err error
	if handler.variant == providerVariantFeaturesBisect {
		document, err = providerFeatureVariantDocument(handler.featureVariant, includePreferences)
	} else {
		document, err = providerVariantDocument(handler.variant, includePreferences)
	}
	if err != nil {
		http.Error(w, "provider fixture unavailable", http.StatusInternalServerError)
		return
	}
	if acceptsJSON(r) {
		writeJSON(w, document)
		return
	}
	writeProviderDocumentXML(w, document)
}

func providerFeatureVariantDocument(variant string, includePreferences bool) (map[string]any, error) {
	realBody, _ := realProviderFixture(includePreferences, true)
	document, err := decodeProviderDocument(realBody)
	if err != nil {
		return nil, err
	}
	provider := primaryProvider(providerContainer(document))

	switch variant {
	case providerFeatureVariantReal:
		return document, nil
	case providerFeatureVariantTimelineKeys:
		timeline := providerFeature(provider, "timeline")
		delete(timeline, "scrobbleKey")
		delete(timeline, "unscrobbleKey")
	case providerFeatureVariantDiscovery:
		removeProviderFeatures(provider, "search", "universalsearch", "queryParser")
	case providerFeatureVariantPresentation:
		removeProviderFeatures(provider, "imagetranscoder", "continuewatching")
	case providerFeatureVariantLibraryActions:
		removeProviderFeatures(provider, "match", "rate", "actions", "playlist", "collection", "manage")
	case providerFeatureVariantSubscribe:
		removeProviderFeatures(provider, "subscribe")
	case providerFeatureVariantWonkVOD:
		wonkDocument, variantErr := providerVariantDocument(providerVariantFeatures, includePreferences)
		if variantErr != nil {
			return nil, variantErr
		}
		provider["Feature"] = primaryProvider(providerContainer(wonkDocument))["Feature"]
	default:
		return nil, fmt.Errorf("unsupported provider feature variant %q", variant)
	}
	return document, nil
}

func removeProviderFeatures(provider map[string]any, featureTypes ...string) {
	remove := make(map[string]bool, len(featureTypes))
	for _, featureType := range featureTypes {
		remove[featureType] = true
	}
	features := provider["Feature"].([]any)
	retained := make([]any, 0, len(features))
	for _, value := range features {
		feature := value.(map[string]any)
		if !remove[feature["type"].(string)] {
			retained = append(retained, feature)
		}
	}
	provider["Feature"] = retained
}

func realProviderFixture(includePreferences, jsonResponse bool) ([]byte, string) {
	if jsonResponse {
		if includePreferences {
			return realProvidersPreferencesJSON, "application/json"
		}
		return realProvidersJSON, "application/json"
	}
	if includePreferences {
		return realProvidersPreferencesXML, "text/xml;charset=utf-8"
	}
	return realProvidersXML, "text/xml;charset=utf-8"
}

func writeExactProviderFixture(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	addCommonHeaders(w)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func decodeProviderDocument(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	return document, nil
}

func providerVariantDocument(variant string, includePreferences bool) (map[string]any, error) {
	realBody, _ := realProviderFixture(includePreferences, true)
	document, err := decodeProviderDocument(realBody)
	if err != nil {
		return nil, err
	}
	if variant == providerVariantWonkVOD {
		return sacrificialMediaProvidersDocument(includePreferences), nil
	}

	wonk := sacrificialMediaProvidersDocument(includePreferences)
	realContainer := providerContainer(document)
	wonkContainer := providerContainer(wonk)
	realProvider := primaryProvider(realContainer)
	wonkProvider := primaryProvider(wonkContainer)

	switch variant {
	case providerVariantPresentation:
		realContainer["friendlyName"] = wonkContainer["friendlyName"]
		realProvider["title"] = wonkProvider["title"]
		realSection := providerLibraryDirectory(realProvider)
		wonkSection := providerLibraryDirectory(wonkProvider)
		realSection["title"] = wonkSection["title"]
	case providerVariantContent:
		wonkContent := providerFeature(wonkProvider, "content")
		if includePreferences {
			realPreferences := providerLibraryDirectory(realProvider)["Preferences"]
			providerLibraryDirectory(wonkProvider)["Preferences"] = realPreferences
		}
		replaceProviderFeature(realProvider, "content", wonkContent)
	case providerVariantPreferences:
		if includePreferences {
			realSection := providerLibraryDirectory(realProvider)
			wonkSection := providerLibraryDirectory(wonkProvider)
			realSection["Preferences"] = wonkSection["Preferences"]
		}
	case providerVariantTypesProtocols:
		realProvider["types"] = wonkProvider["types"]
		realProvider["protocols"] = wonkProvider["protocols"]
	case providerVariantFeatures:
		realContent := providerFeature(realProvider, "content")
		features := providerFeatures(wonkProvider)
		for index, feature := range features {
			if feature["type"] == "content" {
				features[index] = realContent
			}
		}
		realProvider["Feature"] = mapsToAny(features)
	case providerVariantServerCapabilities:
		friendlyName := realContainer["friendlyName"]
		provider := realContainer["MediaProvider"]
		for key := range realContainer {
			delete(realContainer, key)
		}
		for key, value := range wonkContainer {
			if key != "MediaProvider" && key != "friendlyName" {
				realContainer[key] = value
			}
		}
		realContainer["friendlyName"] = friendlyName
		realContainer["MediaProvider"] = provider
	default:
		return nil, fmt.Errorf("unsupported provider variant %q", variant)
	}
	return document, nil
}

func providerContainer(document map[string]any) map[string]any {
	return document["MediaContainer"].(map[string]any)
}

func primaryProvider(container map[string]any) map[string]any {
	return container["MediaProvider"].([]any)[0].(map[string]any)
}

func providerFeatures(provider map[string]any) []map[string]any {
	raw := provider["Feature"].([]any)
	features := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		features = append(features, value.(map[string]any))
	}
	return features
}

func providerFeature(provider map[string]any, featureType string) map[string]any {
	for _, feature := range providerFeatures(provider) {
		if feature["type"] == featureType {
			return feature
		}
	}
	panic("provider fixture missing feature " + featureType)
}

func replaceProviderFeature(provider map[string]any, featureType string, replacement map[string]any) {
	features := provider["Feature"].([]any)
	for index, value := range features {
		if value.(map[string]any)["type"] == featureType {
			features[index] = replacement
			return
		}
	}
	panic("provider fixture missing feature " + featureType)
}

func providerLibraryDirectory(provider map[string]any) map[string]any {
	directories := providerFeature(provider, "content")["Directory"].([]any)
	return directories[1].(map[string]any)
}

func mapsToAny(values []map[string]any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func writeProviderDocumentXML(w http.ResponseWriter, document map[string]any) {
	addCommonHeaders(w)
	w.Header().Set("Content-Type", "text/xml;charset=utf-8")
	_, _ = io.WriteString(w, xml.Header)
	encoder := xml.NewEncoder(w)
	_ = encodeProviderElement(encoder, "MediaContainer", providerContainer(document))
	_ = encoder.Flush()
}

func encodeProviderElement(encoder *xml.Encoder, name string, object map[string]any) error {
	start := xml.StartElement{Name: xml.Name{Local: name}}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if value, ok := providerScalar(object[key]); ok {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: key}, Value: value})
		}
	}
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	for _, key := range keys {
		switch value := object[key].(type) {
		case map[string]any:
			if err := encodeProviderElement(encoder, key, value); err != nil {
				return err
			}
		case []any:
			for _, item := range value {
				child, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if err := encodeProviderElement(encoder, key, child); err != nil {
					return err
				}
			}
		}
	}
	return encoder.EncodeToken(start.End())
}

func providerScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case json.Number:
		return typed.String(), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case bool:
		if typed {
			return "1", true
		}
		return "0", true
	default:
		return "", false
	}
}
