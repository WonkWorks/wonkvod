package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestDirectPlayDecisionMatchesMediaMetadata(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/video/:/transcode/universal/decision?X-Plex-Session-Identifier=session-1", nil)
	response := httptest.NewRecorder()

	writeDirectPlayDecisionXML(response, req)
	body := response.Body.String()

	for _, want := range []string{
		`mdeDecisionCode="1000"`,
		`decision="directplay"`,
		`videoFrameRate="30p"`,
		`frameRate="30.000"`,
		`audioChannels="2"`,
		`audioChannelLayout="stereo"`,
		`channels="2"`,
		`samplingRate="48000"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("decision response missing %s", want)
		}
	}

	for _, stale := range []string{
		`videoFrameRate="15p"`,
		`frameRate="15.000"`,
		`audioChannelLayout="mono"`,
		`samplingRate="8000"`,
	} {
		if strings.Contains(body, stale) {
			t.Errorf("decision response retained stale metadata %s", stale)
		}
	}
}
