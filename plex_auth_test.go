package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wonkvod/internal/xtream"
)

func TestPlexTokenExtraction(t *testing.T) {
	tests := []struct {
		name, header, query, want string
	}{
		{"header only", "header-token", "", "header-token"},
		{"query only", "", "query-token", "query-token"},
		{"both header wins", "header-token", "query-token", "header-token"},
		{"valid header invalid query", "valid", "invalid", "valid"},
		{"invalid header valid query", "invalid", "valid", "invalid"},
		{"empty header uses query", "   ", "query-token", "query-token"},
		{"neither", "", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/library/sections?X-Plex-Token="+url.QueryEscape(test.query), nil)
			if test.header != "" {
				r.Header.Set("X-Plex-Token", test.header)
			}
			if got := plexToken(r); got != test.want {
				t.Fatalf("plexToken()=%q, want %q", got, test.want)
			}
		})
	}
}

func TestPlexAuthorizerUsesDirectTransportAndParsesSections(t *testing.T) {
	var requests atomic.Int32
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/library/sections" || r.Header.Get("X-Plex-Token") != "owner-secret" {
			t.Errorf("unexpected validation request path=%q token-present=%t", r.URL.Path, r.Header.Get("X-Plex-Token") != "")
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<MediaContainer size="2"><Directory key="1"/><Directory key="2"/></MediaContainer>`))
	}))
	defer identity.Close()

	t.Setenv(providerProxyEnv, "http://127.0.0.1:1")
	authorizer, err := newPlexAuthorizer(identity.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := authorizer.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatalf("authorization transport must be direct, got %#v", authorizer.client.Transport)
	}
	got, err := authorizer.authorize(context.Background(), "owner-secret")
	if err != nil || !got.Valid || !got.MoviesAllowed || !got.TVAllowed || requests.Load() != 1 {
		t.Fatalf("authorization=%+v err=%v requests=%d", got, err, requests.Load())
	}
}

func TestPlexAuthorizerResponseSemantics(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    plexAuthorization
		wantErr bool
	}{
		{"invalid", http.StatusUnauthorized, "", plexAuthorization{}, false},
		{"movie only", http.StatusOK, `<MediaContainer><Directory key="1"/></MediaContainer>`, plexAuthorization{Valid: true, MoviesAllowed: true}, false},
		{"tv only", http.StatusOK, `<MediaContainer><Directory key="2"/></MediaContainer>`, plexAuthorization{Valid: true, TVAllowed: true}, false},
		{"neither", http.StatusOK, `<MediaContainer/>`, plexAuthorization{Valid: true}, false},
		{"malformed", http.StatusOK, `<MediaContainer>`, plexAuthorization{}, true},
		{"unexpected", http.StatusForbidden, "", plexAuthorization{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			authorizer, err := newPlexAuthorizer(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			got, err := authorizer.authorize(context.Background(), "secret-not-logged")
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("got=%+v err=%v, want=%+v wantErr=%t", got, err, test.want, test.wantErr)
			}
			if strings.Contains(fmt.Sprint(err), "secret-not-logged") {
				t.Fatal("raw token appeared in error")
			}
		})
	}
}

func TestPlexAuthCacheTTLExpiryEvictionAndNoRawTokenRetention(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newPlexAuthCache()
	cache.maxEntries = 2
	cache.validTTL = 2 * time.Minute
	cache.invalidTTL = 30 * time.Second
	cache.now = func() time.Time { return now }
	var loads int
	grant := plexAuthorization{Valid: true, MoviesAllowed: true}
	loader := func(context.Context, string) (plexAuthorization, error) { loads++; return grant, nil }

	if _, err := cache.authorize(context.Background(), "raw-secret-a", loader); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.authorize(context.Background(), "raw-secret-a", loader); err != nil {
		t.Fatal(err)
	}
	if loads != 1 {
		t.Fatalf("valid cache loads=%d, want 1", loads)
	}
	now = now.Add(2*time.Minute + time.Nanosecond)
	grant = plexAuthorization{Valid: true, TVAllowed: true}
	got, err := cache.authorize(context.Background(), "raw-secret-a", loader)
	if err != nil || loads != 2 || !got.TVAllowed || got.MoviesAllowed {
		t.Fatalf("grant revalidation=%+v loads=%d err=%v", got, loads, err)
	}

	invalidLoads := 0
	invalid := func(context.Context, string) (plexAuthorization, error) {
		invalidLoads++
		return plexAuthorization{}, nil
	}
	if _, err := cache.authorize(context.Background(), "raw-secret-b", invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.authorize(context.Background(), "raw-secret-b", invalid); err != nil {
		t.Fatal(err)
	}
	if invalidLoads != 1 {
		t.Fatalf("invalid cache loads=%d, want 1", invalidLoads)
	}
	now = now.Add(30*time.Second + time.Nanosecond)
	if _, err := cache.authorize(context.Background(), "raw-secret-b", invalid); err != nil {
		t.Fatal(err)
	}
	if invalidLoads != 2 {
		t.Fatalf("expired invalid loads=%d, want 2", invalidLoads)
	}

	for _, token := range []string{"raw-secret-c", "raw-secret-d", "raw-secret-e"} {
		if _, err := cache.authorize(context.Background(), token, invalid); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cacheDump := fmt.Sprintf("%#v", cache.entries)
	cache.mu.Unlock()
	if entryCount != 2 {
		t.Fatalf("bounded cache entries=%d, want 2", entryCount)
	}
	if strings.Contains(cacheDump, "raw-secret") {
		t.Fatal("cache retained a raw token")
	}
}

func TestPlexAuthCacheCoalescesAndDoesNotCacheFailures(t *testing.T) {
	cache := newPlexAuthCache()
	var calls atomic.Int32
	loader := func(context.Context, string) (plexAuthorization, error) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return plexAuthorization{Valid: true, MoviesAllowed: true}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.authorize(context.Background(), "same-secret", loader); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("coalesced loads=%d, want 1", calls.Load())
	}

	failingCache := newPlexAuthCache()
	failCalls := 0
	failing := func(context.Context, string) (plexAuthorization, error) {
		failCalls++
		return plexAuthorization{}, errPlexAuthUnavailable
	}
	_, _ = failingCache.authorize(context.Background(), "failed-secret", failing)
	_, _ = failingCache.authorize(context.Background(), "failed-secret", failing)
	if failCalls != 2 {
		t.Fatalf("failures were cached: calls=%d", failCalls)
	}
}

func testPlexAuthorizer(grants map[string]plexAuthorization, failures map[string]bool) *plexAuthorizer {
	a := &plexAuthorizer{cache: newPlexAuthCache()}
	a.load = func(_ context.Context, token string) (plexAuthorization, error) {
		if failures[token] {
			return plexAuthorization{}, errPlexAuthUnavailable
		}
		return grants[token], nil
	}
	return a
}

func authenticatedRequest(handler http.Handler, method, target, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if token != "" {
		r.Header.Set("X-Plex-Token", token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestPlexRouteAuthorizationAndAggregateFiltering(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	handler.authorizer = testPlexAuthorizer(map[string]plexAuthorization{
		"owner": {Valid: true, MoviesAllowed: true, TVAllowed: true},
		"movie": {Valid: true, MoviesAllowed: true},
		"tv":    {Valid: true, TVAllowed: true},
		"none":  {Valid: true},
	}, map[string]bool{"unavailable": true})

	if got := authenticatedRequest(handler, http.MethodGet, "/library/sections", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("no token=%d", got)
	}
	if got := authenticatedRequest(handler, http.MethodGet, "/library/sections", "invalid").Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid=%d", got)
	}
	if got := authenticatedRequest(handler, http.MethodGet, "/library/sections", "unavailable").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("unavailable=%d", got)
	}
	for _, configure := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer owner") },
		func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "X-Plex-Token", Value: "owner"}) },
	} {
		r := httptest.NewRequest(http.MethodGet, "/library/sections", nil)
		configure(r)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unsupported token transport status=%d", w.Code)
		}
	}

	for _, test := range []struct {
		token string
		want  int
	}{{"owner", 2}, {"movie", 1}, {"tv", 1}, {"none", 0}} {
		response := authenticatedRequest(handler, http.MethodGet, "/library/sections", test.token)
		container := decodeContainer(t, response)
		if got := len(container["Directory"].([]any)); got != test.want {
			t.Fatalf("token=%s directories=%d want=%d", test.token, got, test.want)
		}
	}
	movieHubs := authenticatedRequest(handler, http.MethodGet, "/hubs/promoted", "movie")
	if movieHubs.Code != http.StatusOK || strings.Contains(movieHubs.Body.String(), "Fixture Show") || !strings.Contains(movieHubs.Body.String(), "Fixture Movie") {
		t.Fatalf("movie-only hubs leaked or omitted content: status=%d", movieHubs.Code)
	}
	movieSearch := authenticatedRequest(handler, http.MethodGet, "/library/search?query=Fixture", "movie")
	if movieSearch.Code != http.StatusOK || strings.Contains(movieSearch.Body.String(), "Fixture Show") || !strings.Contains(movieSearch.Body.String(), "Fixture Movie") {
		t.Fatalf("movie-only search leaked or omitted content: status=%d", movieSearch.Code)
	}
	neitherSearch := authenticatedRequest(handler, http.MethodGet, "/library/search?query=Fixture", "none")
	if neitherSearch.Code != http.StatusOK || strings.Contains(neitherSearch.Body.String(), "Fixture Movie") || strings.Contains(neitherSearch.Body.String(), "Fixture Show") {
		t.Fatalf("no-grant search leaked content: status=%d", neitherSearch.Code)
	}

	movieID := movieObjectID(snapshot.Movies[0])
	showID := showObjectID(snapshot.Shows[0])
	episodeID := episodeObjectID(snapshot.Shows[0].Seasons[0].Episodes[0])
	moviePart := partObjectID("movie", snapshot.Movies[0].SourceID)
	episodePart := partObjectID("episode", snapshot.Shows[0].Seasons[0].Episodes[0].SourceID)
	checks := []struct {
		token, target string
		want          int
	}{
		{"movie", "/library/metadata/" + movieID, http.StatusOK},
		{"tv", "/library/metadata/" + movieID, http.StatusNotFound},
		{"tv", "/library/metadata/" + showID, http.StatusOK},
		{"movie", "/library/metadata/" + episodeID, http.StatusNotFound},
		{"tv", "/library/parts/" + fmt.Sprint(episodePart) + "/file.mp4", http.StatusOK},
		{"movie", "/library/parts/" + fmt.Sprint(episodePart) + "/file.mp4", http.StatusNotFound},
		{"movie", "/library/parts/" + fmt.Sprint(moviePart) + "/file.mp4", http.StatusOK},
		{"tv", "/library/parts/" + fmt.Sprint(moviePart) + "/file.mp4", http.StatusNotFound},
		{"tv", "/photo/:/transcode?url=" + url.QueryEscape("/library/metadata/"+movieID+"/art/1"), http.StatusNotFound},
		{"tv", "/video/:/transcode/universal/decision?path=" + url.QueryEscape("/library/metadata/"+movieID), http.StatusNotFound},
	}
	for _, check := range checks {
		if got := authenticatedRequest(handler, http.MethodGet, check.target, check.token).Code; got != check.want {
			t.Errorf("token=%s target=%s status=%d want=%d", check.token, check.target, got, check.want)
		}
	}

	queueTarget := "/playQueues?uri=" + url.QueryEscape("server://fixture/com.plexapp.plugins.library/library/metadata/"+movieID)
	if got := authenticatedRequest(handler, http.MethodPost, queueTarget, "tv").Code; got != http.StatusNotFound {
		t.Fatalf("unauthorized queue=%d", got)
	}
	if got := authenticatedRequest(handler, http.MethodPost, queueTarget, "owner").Code; got != http.StatusOK {
		t.Fatalf("owner queue=%d", got)
	}

	if got := authenticatedRequest(handler, http.MethodGet, "/identity", "").Code; got != http.StatusTeapot {
		t.Fatalf("public identity passthrough=%d", got)
	}
	if got := authenticatedRequest(handler, http.MethodGet, "/unknown/pms/route", "").Code; got != http.StatusTeapot {
		t.Fatalf("unknown passthrough=%d", got)
	}
	if got := authenticatedRequest(handler, http.MethodOptions, "/library/sections", "").Code; got != http.StatusNoContent {
		t.Fatalf("OPTIONS=%d", got)
	}
}

func TestUnauthorizedMediaAndArtworkNeverReachProvider(t *testing.T) {
	handler, snapshot, _ := realCatalogTestHandler(t)
	handler.authorizer = testPlexAuthorizer(map[string]plexAuthorization{"tv": {Valid: true, TVAllowed: true}}, nil)
	var originRequests atomic.Int32
	handler.httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		originRequests.Add(1)
		return nil, errors.New("provider access must not occur")
	})}
	providerClient, err := xtream.NewClient(&http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		originRequests.Add(1)
		return nil, errors.New("provider media access must not occur")
	})}, "http://provider.invalid", "user", "password")
	if err != nil {
		t.Fatal(err)
	}
	handler.catalog.mu.Lock()
	handler.catalog.client = providerClient
	handler.catalog.sources = make(map[string]*xtream.VODSource)
	handler.catalog.mu.Unlock()
	movieID := movieObjectID(snapshot.Movies[0])
	moviePart := partObjectID("movie", snapshot.Movies[0].SourceID)
	for _, target := range []string{
		"/library/metadata/" + movieID + "/thumb/1",
		"/photo/:/transcode?url=" + url.QueryEscape("/library/metadata/"+movieID+"/art/1"),
		"/library/parts/" + fmt.Sprint(moviePart) + "/file.mp4",
	} {
		if got := authenticatedRequest(handler, http.MethodGet, target, "tv").Code; got != http.StatusNotFound {
			t.Fatalf("target=%s status=%d", target, got)
		}
	}
	if originRequests.Load() != 0 {
		t.Fatalf("unauthorized requests reached provider %d times", originRequests.Load())
	}
}

func TestInvalidTokenFloodIsBounded(t *testing.T) {
	cache := newPlexAuthCache()
	cache.maxEntries = 64
	invalid := func(context.Context, string) (plexAuthorization, error) { return plexAuthorization{}, nil }
	for i := 0; i < 500; i++ {
		if _, err := cache.authorize(context.Background(), fmt.Sprintf("invalid-%d", i), invalid); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.entries) != 64 || cache.lru.Len() != 64 {
		t.Fatalf("cache entries=%d lru=%d", len(cache.entries), cache.lru.Len())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPlexAuthorizationResponsesNeverExposeToken(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	handler.authorizer = testPlexAuthorizer(nil, map[string]bool{"very-private-token": true})
	for _, token := range []string{"very-private-token", "invalid-private-token"} {
		response := authenticatedRequest(handler, http.MethodGet, "/library/sections", token)
		if strings.Contains(response.Body.String(), token) || strings.Contains(fmt.Sprint(response.Header()), token) {
			t.Fatal("authorization response exposed raw token")
		}
	}
}

func TestFilteredProviderContentDirectories(t *testing.T) {
	handler, _, _ := realCatalogTestHandler(t)
	handler.authorizer = testPlexAuthorizer(map[string]plexAuthorization{"movie": {Valid: true, MoviesAllowed: true}}, nil)
	response := authenticatedRequest(handler, http.MethodGet, "/media/providers?includePreferences=1", "movie")
	var document struct {
		MediaContainer struct {
			MediaProvider []struct {
				Feature []struct {
					Type      string           `json:"type"`
					Directory []map[string]any `json:"Directory"`
				} `json:"Feature"`
			} `json:"MediaProvider"`
		} `json:"MediaContainer"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	var directories []map[string]any
	for _, feature := range document.MediaContainer.MediaProvider[0].Feature {
		if feature.Type == "content" {
			directories = feature.Directory
		}
	}
	if len(directories) != 2 || directories[0]["title"] != "Home" || directories[1]["key"] != "/library/sections/1" {
		t.Fatalf("filtered provider directories=%#v", directories)
	}
}
