package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	canonical "wonkvod/internal/catalog"
	"wonkvod/internal/xtream"
)

func setupFixture(t *testing.T) (*realCatalogHandler, *catalogRuntime) {
	t.Helper()
	t.Setenv("WONKVOD_SECURE_URI", "https://127.0.0.1:1")
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: "https://provider.example", Username: "user", Password: "never-render-this"}
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json"), config: config}
	if err := saveAppConfig(store.path, config); err != nil {
		t.Fatal(err)
	}
	snapshot := canonicalSnapshot{Version: realCatalogVersion, GeneratedAt: time.Now().UTC(), Movies: []canonicalMovie{{SourceID: "movie", MediaFacts: canonical.MediaFacts{State: "inspected"}}}}
	runtime := &catalogRuntime{config: store, statePath: filepath.Join(t.TempDir(), "catalog.json"), snapshot: snapshot, scheduleWake: make(chan struct{}, 1), sources: map[string]*xtream.VODSource{}, capabilities: map[string]xtream.CapabilityProbe{}}
	runtime.status = catalogRefreshStatus{CatalogAvailable: true, LastRefreshResult: "loaded_snapshot"}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAPIJSON(w, http.StatusOK, map[string]any{"MediaContainer": map[string]any{"claimed": true, "machineIdentifier": machineID, "token": "must-not-leak"}})
	}))
	t.Cleanup(upstream.Close)
	return &realCatalogHandler{catalog: runtime, upstreamURL: upstream.URL, httpClient: http.DefaultClient}, runtime
}

func TestSetupPagesAssetsAndSecrets(t *testing.T) {
	handler, _ := setupFixture(t)
	server := handler.SetupHandler()
	for _, path := range []string{"/", "/plex", "/provider", "/categories", "/libraries", "/settings", "/assets/branding/wonkvodlogo_dark.png", "/assets/branding/wonkvodlogo_light.png"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		if strings.Contains(response.Body.String(), "never-render-this") {
			t.Fatalf("%s rendered provider password", path)
		}
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if strings.Contains(response.Body.String(), "never-render-this") || !strings.Contains(response.Body.String(), `"password_configured":true`) {
		t.Fatalf("unsafe config response: %s", response.Body.String())
	}
	plex := httptest.NewRecorder()
	server.ServeHTTP(plex, httptest.NewRequest(http.MethodGet, "/api/plex/status", nil))
	if strings.Contains(plex.Body.String(), "must-not-leak") {
		t.Fatalf("Plex status leaked upstream secret: %s", plex.Body.String())
	}
	if !strings.Contains(plex.Body.String(), `"claimed":true`) {
		t.Fatalf("Plex claimed status was not projected: %s", plex.Body.String())
	}
	if !strings.Contains(plex.Body.String(), `"setup_state":`) || !strings.Contains(plex.Body.String(), `"setup_actions_available":false`) {
		t.Fatalf("Plex setup seam was not safely projected: %s", plex.Body.String())
	}
	setup := httptest.NewRecorder()
	server.ServeHTTP(setup, httptest.NewRequest(http.MethodGet, "/api/plex/setup", nil))
	if setup.Code != http.StatusOK || !strings.Contains(setup.Body.String(), `"setup_mode":"externally_managed_sidecar"`) {
		t.Fatalf("Plex setup endpoint status=%d body=%s", setup.Code, setup.Body.String())
	}
	mutation := httptest.NewRecorder()
	server.ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "/api/plex/setup", nil))
	if mutation.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Plex setup mutation unexpectedly available: %d", mutation.Code)
	}
}

func TestPlexSetupStateTransitions(t *testing.T) {
	tests := []struct {
		name   string
		status plexStatusResponse
		state  string
		ready  bool
	}{
		{"identity unavailable", plexStatusResponse{}, plexSetupWaitingForIdentity, false},
		{"unclaimed", plexStatusResponse{IdentityReachable: true, Claimed: false}, plexSetupUnclaimed, false},
		{"machine pending", plexStatusResponse{IdentityReachable: true, Claimed: true}, plexSetupWaitingForIdentity, false},
		{"certificate pending", plexStatusResponse{IdentityReachable: true, Claimed: true, MachineIdentifier: "machine"}, plexSetupCertificatePending, false},
		{"ready", plexStatusResponse{IdentityReachable: true, Claimed: "1", MachineIdentifier: "machine", TLSStatus: "valid"}, plexSetupReady, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, ready := plexSetupState(test.status)
			if state != test.state || ready != test.ready {
				t.Fatalf("state=%q ready=%t, want %q/%t", state, ready, test.state, test.ready)
			}
		})
	}
}

func TestSetupUIHardeningContract(t *testing.T) {
	handler, _ := setupFixture(t)
	response := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/categories", nil))
	body := response.Body.String()
	for _, required := range []string{
		`href="/categories">Categories`, `<h1>Categories</h1>`, `Movie Categories`, `TV Categories`,
		`Categories saved.`, `A catalog refresh is required`, `Refresh Now`, `Saving…`,
		`Refreshing catalog…`, `Your previous catalog is still active.`, `setTimeout(overview,3000)`,
		`wonkvod-theme`, `<option value="system">System</option>`, `<option value="light">Light</option>`, `<option value="dark">Dark</option>`,
		`wonkvodlogo_`, `wonkvodlogo_light.png`, `Connection failed.`, `Provider settings saved.`, `Settings saved.`,
		`Catalog refresh concurrency`, `Lower values are friendlier to providers.`, `This does not control simultaneous video playback.`,
		`The claimed Plex identity and secure endpoint are ready.`, `Destructive setup actions are intentionally unavailable.`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("setup UI missing %q", required)
		}
	}
	for _, forbidden := range []string{`href="/libraries">Libraries`, `<h1>Libraries</h1>`, `<pre id="result">`, `JSON.stringify(await api`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("setup UI retains raw/legacy surface %q", forbidden)
		}
	}
}

func TestProviderUpdateKeepsOmittedPassword(t *testing.T) {
	handler, runtime := setupFixture(t)
	request := httptest.NewRequest(http.MethodPut, "/api/config/provider", strings.NewReader(`{"base_url":"https://new.example","username":"new-user"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if runtime.config.snapshot().Provider.Password != "never-render-this" {
		t.Fatal("password changed when omitted")
	}
}

func TestLibraryCategoryConfigurationIsValidatedBeforePromotion(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("action") {
		case "get_vod_categories":
			_, _ = w.Write([]byte(`[{"category_id":"917","category_name":"Black Cinema"}]`))
		case "get_series_categories":
			_, _ = w.Write([]byte(`[{"category_id":"887","category_name":"BET"},{"category_id":"926","category_name":"A&E"},{"category_id":"779","category_name":"REALITY"}]`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	handler, runtime := setupFixture(t)
	runtime.config.mu.Lock()
	runtime.config.config.Provider.BaseURL = provider.URL
	runtime.config.mu.Unlock()
	body := `{"movie_category_ids":["917"],"tv_category_ids":["887","926","779"]}`
	request := httptest.NewRequest(http.MethodPut, "/api/config/libraries", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"refresh_required":true`) {
		t.Fatalf("valid category update status=%d body=%s", response.Code, response.Body.String())
	}
	before := runtime.config.snapshot().Libraries
	bad := httptest.NewRequest(http.MethodPut, "/api/config/libraries", strings.NewReader(`{"movie_category_ids":["missing"],"tv_category_ids":["887"]}`))
	badResponse := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest || strings.Join(runtime.config.snapshot().Libraries.MovieCategoryIDs, ",") != strings.Join(before.MovieCategoryIDs, ",") {
		t.Fatalf("invalid category update was promoted: status=%d", badResponse.Code)
	}
}

func TestCatalogRefreshSerializationAndFallback(t *testing.T) {
	_, runtime := setupFixture(t)
	block := make(chan struct{})
	runtime.fetcher = func(context.Context, appConfig) (canonicalSnapshot, *xtream.Client, error) {
		<-block
		return canonicalSnapshot{}, nil, errors.New("provider unavailable")
	}
	if !runtime.triggerRefresh("test") {
		t.Fatal("first refresh rejected")
	}
	if runtime.triggerRefresh("duplicate") {
		t.Fatal("duplicate refresh accepted")
	}
	close(block)
	deadline := time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := runtime.catalogStatus()
	if status.LastRefreshResult != "failed" || !status.CatalogAvailable || len(runtime.snapshotCopy().Movies) != 1 {
		t.Fatalf("failed refresh status/snapshot = %#v", status)
	}

	runtime.fetcher = func(context.Context, appConfig) (canonicalSnapshot, *xtream.Client, error) {
		return canonicalSnapshot{Version: realCatalogVersion, GeneratedAt: time.Now().UTC(), Movies: []canonicalMovie{{SourceID: "movie"}, {SourceID: "new"}}}, nil, nil
	}
	if !runtime.triggerRefresh("test-success") {
		t.Fatal("successful refresh rejected")
	}
	deadline = time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.snapshotCopy(); len(got.Movies) != 2 || got.Movies[0].MediaFacts.State != "inspected" {
		t.Fatalf("successful atomic promotion/media facts = %#v", got.Movies)
	}
	if persisted, err := loadCanonicalSnapshot(runtime.statePath); err != nil || len(persisted.Movies) != 2 {
		t.Fatalf("promoted snapshot was not persisted atomically: movies=%d err=%v", len(persisted.Movies), err)
	}
}

func TestCatalogStatusJSON(t *testing.T) {
	handler, _ := setupFixture(t)
	response := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))
	var status catalogStatusResponse
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.CatalogAvailable || status.Movies != 1 || status.MediaFactsInspected != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestHealthDistinguishesColdStart(t *testing.T) {
	handler, runtime := setupFixture(t)
	runtime.refreshMu.Lock()
	runtime.status = catalogRefreshStatus{RefreshRunning: true, LastRefreshResult: "running"}
	runtime.refreshMu.Unlock()
	runtime.mu.Lock()
	runtime.snapshot = canonicalSnapshot{}
	runtime.mu.Unlock()
	response := httptest.NewRecorder()
	handler.SetupHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("cold health status = %d", response.Code)
	}
}

func TestExistingSnapshotLoadsWithoutProviderWait(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	catalogPath := filepath.Join(directory, "catalog.json")
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: "http://127.0.0.1:1", Username: "user", Password: "secret"}
	if err := saveAppConfig(configPath, config); err != nil {
		t.Fatal(err)
	}
	snapshot := canonicalSnapshot{Version: realCatalogVersion, GeneratedAt: time.Now().UTC(), Movies: []canonicalMovie{{SourceID: "cached"}}}
	if err := saveCanonicalSnapshot(catalogPath, snapshot); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WONKVOD_CONFIG_STATE", configPath)
	t.Setenv("WONKVOD_CATALOG_STATE", catalogPath)
	started := time.Now()
	runtime, err := newCatalogRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second || !runtime.catalogStatus().CatalogAvailable || len(runtime.snapshotCopy().Movies) != 1 {
		t.Fatalf("persisted snapshot did not become available immediately: %#v", runtime.catalogStatus())
	}
}
