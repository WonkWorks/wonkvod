package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
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
	handler := &realCatalogHandler{catalog: runtime, upstreamURL: upstream.URL, httpClient: http.DefaultClient}
	handler.setupAuth = configuredTestAuth(t)
	return handler, runtime
}

func configuredTestAuth(t *testing.T) *adminAuth {
	t.Helper()
	parameters := argon2Parameters{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	path := filepath.Join(t.TempDir(), "auth.json")
	hash, err := encodeArgon2Hash("test-password-long", parameters)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveAuthState(path, authState{Version: authStateVersion, PasswordHash: hash, PasswordSetAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	auth, err := newAdminAuthWithParameters(path, parameters, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func authenticatedSetupHandler(t *testing.T, handler *realCatalogHandler) http.Handler {
	t.Helper()
	if handler.setupAuth == nil {
		handler.setupAuth = configuredTestAuth(t)
	}
	token, session, err := handler.setupAuth.createSession()
	if err != nil {
		t.Fatal(err)
	}
	inner := handler.SetupHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
		if stateChangingMethod(r.Method) {
			r.Header.Set("X-WonkVOD-CSRF", session.CSRF)
		}
		inner.ServeHTTP(w, r)
	})
}

func TestSetupPagesAssetsAndSecrets(t *testing.T) {
	handler, _ := setupFixture(t)
	server := authenticatedSetupHandler(t, handler)
	for _, path := range []string{"/", "/plex", "/provider", "/categories", "/libraries", "/quarantine", "/settings", "/assets/branding/wonkvodlogo_dark.png", "/assets/branding/wonkvodlogo_light.png"} {
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

func TestQuarantineAPIIsBackwardCompatibleAndSanitized(t *testing.T) {
	handler, runtime := setupFixture(t)
	runtime.mu.Lock()
	runtime.snapshot.Quarantine = []quarantineEntry{
		{Kind: "movie", SourceID: "m1", Reason: "metadata request failed"},
		{Kind: "show", SourceID: "s1", Title: "Show", Reason: "series traversal failed", ReasonClass: "http", HTTPStatus: 503},
	}
	runtime.mu.Unlock()
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/catalog/quarantine?X-Plex-Token=secret", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("unsafe quarantine response status=%d body=%s", response.Code, response.Body.String())
	}
	var got quarantineResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Movies != 1 || got.Shows != 1 || got.ReasonBuckets["legacy"] != 1 || !got.Items[0].Retryable {
		t.Fatalf("quarantine response=%#v", got)
	}
}

func TestQuarantineZeroStateAndThemeContract(t *testing.T) {
	handler, runtime := setupFixture(t)
	runtime.mu.Lock()
	runtime.snapshot.Quarantine = nil
	runtime.mu.Unlock()
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/quarantine", nil))
	body := response.Body.String()
	for _, required := range []string{"No quarantined items", "--header:#eef1f5", "--header:#0d1117", "background:var(--header)", "activeTheme()==='dark'?'dark':'light'"} {
		if !strings.Contains(body, required) {
			t.Fatalf("UI missing %q", required)
		}
	}
}

func TestRefreshConcurrencyOverrideIsValidatedAndDoesNotPersist(t *testing.T) {
	handler, runtime := setupFixture(t)
	seen := make(chan int, 1)
	block := make(chan struct{})
	runtime.fetcher = func(_ context.Context, config appConfig) (canonicalSnapshot, *xtream.Client, error) {
		seen <- config.Catalog.RefreshConcurrency
		<-block
		return runtime.snapshotCopy(), nil, nil
	}
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/catalog/refresh", strings.NewReader(`{"concurrency":8}`)))
	if response.Code != http.StatusAccepted || <-seen != 8 {
		t.Fatalf("override response=%d body=%s", response.Code, response.Body.String())
	}
	if runtime.config.snapshot().Catalog.RefreshConcurrency != defaultCatalogRefreshConcurrency {
		t.Fatal("one-time override persisted")
	}
	close(block)
	deadline := time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	invalid := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/api/catalog/refresh", strings.NewReader(`{"concurrency":3}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid override status=%d", invalid.Code)
	}
	seen = make(chan int, 1)
	runtime.fetcher = func(_ context.Context, config appConfig) (canonicalSnapshot, *xtream.Client, error) {
		seen <- config.Catalog.RefreshConcurrency
		return runtime.snapshotCopy(), nil, nil
	}
	if !runtime.triggerRefresh("scheduled") || <-seen != defaultCatalogRefreshConcurrency {
		t.Fatal("scheduled refresh did not use persisted default")
	}
	deadline = time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestCategoryImpactPreviewUsesOnlyBulkLists(t *testing.T) {
	provider := newIncrementalProvider()
	provider.movies["b"] = []string{"m1", "m2"}
	provider.movieName["m2"], provider.movieRevision["m2"] = "Movie Two", "2"
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	config := incrementalConfig(server.URL, "a")
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json"), config: config}
	runtime := &catalogRuntime{config: store, snapshot: canonicalSnapshot{Version: realCatalogVersion, Movies: []canonicalMovie{{SourceID: "m1", Category: catalogCategory{ID: "a", Name: "A"}, Categories: []canonical.Category{{ID: "a", Name: "A"}}}, {SourceID: "gone"}}, Shows: []canonicalShow{{SourceID: "s1", Category: catalogCategory{ID: "t", Name: "TV"}, Categories: []canonical.Category{{ID: "t", Name: "TV"}}}}}}
	preview, err := runtime.previewCategoryImpact(t.Context(), librariesConfig{MovieCategoryIDs: []string{"b"}, TVCategoryIDs: []string{"t"}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.NewMovies != 1 || preview.RemovedMovies != 1 || preview.MembershipOnly != 1 || preview.ResultMovies != 2 {
		t.Fatalf("preview=%#v", preview)
	}
	if preview.Warning || preview.SuggestedConcurrency != 0 {
		t.Fatalf("small expansion warned excessively: %#v", preview)
	}
	if provider.movieDetails != 0 || provider.showDetails != 0 {
		t.Fatalf("preview fetched details %d/%d", provider.movieDetails, provider.showDetails)
	}
}

func TestCategoryExpansionGuidanceThreshold(t *testing.T) {
	small := categoryImpactPreview{NewTopLevel: categoryExpansionWarningThreshold}
	applyExpansionGuidance(&small)
	if small.Warning || small.SuggestedConcurrency != 0 {
		t.Fatalf("threshold should not warn: %#v", small)
	}
	large := categoryImpactPreview{NewTopLevel: 4000}
	applyExpansionGuidance(&large)
	if !large.Warning || large.SuggestedConcurrency != 10 {
		t.Fatalf("large guidance=%#v", large)
	}
}

func TestQuarantineItemRetryUsesAtomicRefreshAndSafeConcurrency(t *testing.T) {
	provider := newIncrementalProvider()
	server := httptest.NewServer(http.HandlerFunc(provider.handler))
	defer server.Close()
	config := incrementalConfig(server.URL, "a")
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json"), config: config}
	runtime := &catalogRuntime{
		config: store, statePath: filepath.Join(t.TempDir(), "catalog.json"), startedAt: time.Now(),
		snapshot:     canonicalSnapshot{Version: realCatalogVersion, GeneratedAt: time.Now().UTC(), Quarantine: []quarantineEntry{{Kind: "movie", SourceID: "m1", Reason: "metadata request failed"}}},
		scheduleWake: make(chan struct{}, 1), sources: map[string]*xtream.VODSource{}, capabilities: map[string]xtream.CapabilityProbe{},
	}
	runtime.status = catalogRefreshStatus{CatalogAvailable: true}
	handler := &realCatalogHandler{catalog: runtime}
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/catalog/quarantine/movie/m1/retry", nil))
	if response.Code != http.StatusAccepted || runtime.catalogStatus().Metrics.EffectiveConcurrency != 1 {
		t.Fatalf("retry status=%d body=%s metrics=%#v", response.Code, response.Body.String(), runtime.catalogStatus().Metrics)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.snapshotCopy(); len(got.Movies) != 1 || len(got.Quarantine) != 0 {
		t.Fatalf("retry did not promote recovered item: %#v", got)
	}

	provider.movieRevision["m1"] = "changed"
	provider.failMovies["m1"] = true
	runtime.mu.Lock()
	runtime.snapshot.Quarantine = []quarantineEntry{{Kind: "movie", SourceID: "m1", Reason: "metadata request failed"}}
	beforeTitle := runtime.snapshot.Movies[0].Title
	runtime.mu.Unlock()
	failed := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(failed, httptest.NewRequest(http.MethodPost, "/api/catalog/quarantine/movie/m1/retry", nil))
	deadline = time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := runtime.snapshotCopy()
	if len(got.Movies) != 1 || got.Movies[0].Title != beforeTitle || len(got.Quarantine) != 1 || got.Quarantine[0].LastRetryResult != "failed" {
		t.Fatalf("failed retry damaged live item: %#v", got)
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
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/categories", nil))
	body := response.Body.String()
	for _, required := range []string{
		`href="/categories">Categories`, `<h1>Categories</h1>`, `Movie Categories`, `TV Categories`,
		`Categories saved.`, `A catalog refresh is required`, `Refresh Now`, `Saving…`,
		`Refreshing catalog — `, `Your previous catalog is still active.`, `setTimeout(overview,1500)`,
		`progress-fill indeterminate`, `p.completed+' / '+p.total`, `Math.min(99`, `c.refresh_running?'disabled'`,
		`Fetching category indexes`, `Comparing with current catalog`, `Enriching movie metadata`, `Enriching TV metadata`, `Validating snapshot`, `Promoting snapshot`,
		`wonkvod-theme`, `<option value="system">System</option>`, `<option value="light">Light</option>`, `<option value="dark">Dark</option>`,
		`wonkvodlogo_`, `wonkvodlogo_light.png`, `Connection failed.`, `Provider settings saved.`, `Settings saved.`,
		`Catalog refresh concurrency`, `Lower values are friendlier to providers.`, `This does not control simultaneous video playback.`,
		`Full Rebuild`, `Normal Refresh checks additions, removals, changes, and quarantined items`, `/api/catalog/rebuild`,
		`The claimed Plex identity and secure endpoint are ready.`, `Plex identity is externally managed. No setup actions are required.`,
		`Change Admin Password`, `X-WonkVOD-CSRF`, `id="logout"`, `WonkVOD v1.0.1`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("setup UI missing %q", required)
		}
	}
	for _, forbidden := range []string{`href="/libraries">Libraries`, `<h1>Libraries</h1>`, `<pre id="result">`, `JSON.stringify(await api`, `.value=String(suggested)`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("setup UI retains raw/legacy surface %q", forbidden)
		}
	}
}

func TestRefreshProgressStatusPhases(t *testing.T) {
	_, runtime := setupFixture(t)
	runtime.refreshMu.Lock()
	runtime.refreshing = true
	runtime.status.RefreshRunning = true
	runtime.status.Progress = catalogRefreshProgress{Phase: "category_indexes"}
	runtime.refreshMu.Unlock()
	status := runtime.catalogStatus()
	if status.Progress.Phase != "category_indexes" || status.Progress.Determinate {
		t.Fatalf("early progress=%#v", status.Progress)
	}
	runtime.updateRefreshProgress("enriching_movies", 238, 376, true)
	status = runtime.catalogStatus()
	if !status.Progress.Determinate || status.Progress.Completed != 238 || status.Progress.Total != 376 {
		t.Fatalf("enrichment progress=%#v", status.Progress)
	}
	runtime.updateRefreshProgress("validating", 0, 0, false)
	status = runtime.catalogStatus()
	if status.Progress.Phase != "validating" || status.Progress.Determinate || status.Progress.Total != 0 {
		t.Fatalf("validation progress=%#v", status.Progress)
	}
}

func TestCatalogRebuildEndpointIsExplicit(t *testing.T) {
	handler, runtime := setupFixture(t)
	block := make(chan struct{})
	runtime.fetcher = func(context.Context, appConfig) (canonicalSnapshot, *xtream.Client, error) {
		<-block
		return runtime.snapshotCopy(), nil, nil
	}
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/catalog/rebuild", nil))
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"mode":"full_rebuild"`) {
		t.Fatalf("rebuild status=%d body=%s", response.Code, response.Body.String())
	}
	if got := runtime.catalogStatus().Metrics.Mode; got != "full_rebuild" {
		t.Fatalf("rebuild mode=%q", got)
	}
	close(block)
	deadline := time.Now().Add(time.Second)
	for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestScheduledAndStartupRefreshUseIncrementalMode(t *testing.T) {
	for _, reason := range []string{"scheduled", "startup"} {
		t.Run(reason, func(t *testing.T) {
			_, runtime := setupFixture(t)
			block := make(chan struct{})
			runtime.fetcher = func(context.Context, appConfig) (canonicalSnapshot, *xtream.Client, error) {
				<-block
				return runtime.snapshotCopy(), nil, nil
			}
			if !runtime.triggerRefresh(reason) {
				t.Fatal("refresh was rejected")
			}
			if got := runtime.catalogStatus().Metrics.Mode; got != "incremental" {
				t.Fatalf("mode=%q", got)
			}
			close(block)
			deadline := time.Now().Add(time.Second)
			for runtime.catalogStatus().RefreshRunning && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestProviderUpdateKeepsOmittedPassword(t *testing.T) {
	handler, runtime := setupFixture(t)
	request := httptest.NewRequest(http.MethodPut, "/api/config/provider", strings.NewReader(`{"base_url":"https://new.example","username":"new-user"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, request)
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
	authenticatedSetupHandler(t, handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"refresh_required":true`) {
		t.Fatalf("valid category update status=%d body=%s", response.Code, response.Body.String())
	}
	before := runtime.config.snapshot().Libraries
	bad := httptest.NewRequest(http.MethodPut, "/api/config/libraries", strings.NewReader(`{"movie_category_ids":["missing"],"tv_category_ids":["887"]}`))
	badResponse := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(badResponse, bad)
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
	if status.RefreshRunning || status.Progress.Phase != "failed" {
		t.Fatalf("failed refresh retained running progress: %#v", status.Progress)
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
	if status := runtime.catalogStatus(); status.RefreshRunning || status.Progress.Phase != "complete" || status.Movies != 2 {
		t.Fatalf("successful refresh did not transition/update counts: %#v", status)
	}
	if _, ok := runtime.playable(movieObjectID(runtime.snapshotCopy().Movies[1])); !ok {
		t.Fatal("successful refresh did not publish matching read indexes")
	}
	if persisted, err := loadCanonicalSnapshot(runtime.statePath); err != nil || len(persisted.Movies) != 2 {
		t.Fatalf("promoted snapshot was not persisted atomically: movies=%d err=%v", len(persisted.Movies), err)
	}
}

func TestCatalogStatusJSON(t *testing.T) {
	handler, _ := setupFixture(t)
	response := httptest.NewRecorder()
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/catalog/status", nil))
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
	authenticatedSetupHandler(t, handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
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
