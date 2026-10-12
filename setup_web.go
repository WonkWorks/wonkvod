package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"wonkvod/internal/xtream"
)

//go:embed assets/branding/*
var setupAssets embed.FS

type categoryOption struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}

type categoryResponse struct {
	Movies []categoryOption `json:"movies"`
	TV     []categoryOption `json:"tv"`
}

type providerStatusResponse struct {
	Configured bool   `json:"configured"`
	Reachable  bool   `json:"reachable"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type plexStatusResponse struct {
	IdentityReachable bool   `json:"identity_reachable"`
	Claimed           any    `json:"claimed"`
	MachineIdentifier string `json:"machine_identifier"`
	SecureURI         string `json:"secure_uri"`
	TLSStatus         string `json:"tls_status"`
	CloudPresence     string `json:"cloud_presence"`
	BackendStatus     string `json:"backend_status"`
	SetupState        string `json:"setup_state"`
	Ready             bool   `json:"ready"`
	SetupMode         string `json:"setup_mode"`
	ControlLibraries  string `json:"control_libraries"`
	SetupActions      bool   `json:"setup_actions_available"`
	Error             string `json:"error,omitempty"`
}

const (
	plexSetupWaitingForIdentity = "waiting_for_identity"
	plexSetupUnclaimed          = "unclaimed"
	plexSetupCertificatePending = "certificate_pending"
	plexSetupReady              = "ready"
)

func (h *realCatalogHandler) SetupHandler() http.Handler {
	mux := http.NewServeMux()
	auth, authErr := h.setupAuthManager()
	assets, _ := fs.Sub(setupAssets, "assets/branding")
	mux.Handle("GET /assets/branding/", http.StripPrefix("/assets/branding/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("GET /setup", h.bootstrapPage)
	mux.HandleFunc("GET /api/auth/status", h.apiAuthStatus)
	mux.HandleFunc("GET /api/auth/session", h.apiAuthSession)
	mux.HandleFunc("POST /api/auth/setup", h.apiAuthSetup)
	mux.HandleFunc("POST /api/auth/login", h.apiAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", h.apiAuthLogout)
	mux.HandleFunc("PUT /api/auth/password", h.apiAuthPassword)
	for _, page := range []string{"/", "/plex", "/provider", "/categories", "/libraries", "/quarantine", "/settings"} {
		page := page
		mux.HandleFunc("GET "+page, func(w http.ResponseWriter, r *http.Request) {
			if page == "/libraries" {
				writeSetupPage(w, "/categories")
				return
			}
			writeSetupPage(w, page)
		})
	}
	mux.HandleFunc("GET /api/setup/status", h.apiSetupStatus)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /api/config", h.apiConfig)
	mux.HandleFunc("GET /api/catalog/status", h.apiCatalogStatus)
	mux.HandleFunc("POST /api/catalog/refresh", h.apiCatalogRefresh)
	mux.HandleFunc("POST /api/catalog/rebuild", h.apiCatalogRebuild)
	mux.HandleFunc("POST /api/catalog/preview", h.apiCatalogPreview)
	mux.HandleFunc("GET /api/catalog/quarantine", h.apiCatalogQuarantine)
	mux.HandleFunc("POST /api/catalog/quarantine/retry", h.apiCatalogQuarantineRetry)
	mux.HandleFunc("POST /api/catalog/quarantine/{kind}/{id}/retry", h.apiCatalogQuarantineItemRetry)
	mux.HandleFunc("GET /api/provider/status", h.apiProviderStatus)
	mux.HandleFunc("POST /api/provider/test", h.apiProviderTest)
	mux.HandleFunc("GET /api/provider/categories", h.apiProviderCategories)
	mux.HandleFunc("PUT /api/config/provider", h.apiProviderConfig)
	mux.HandleFunc("PUT /api/config/libraries", h.apiLibrariesConfig)
	mux.HandleFunc("PUT /api/config/catalog", h.apiCatalogConfig)
	mux.HandleFunc("GET /api/plex/status", h.apiPlexStatus)
	mux.HandleFunc("GET /api/plex/setup", h.apiPlexStatus)
	if authErr != nil {
		log.Printf("WonkVOD Web Setup authentication unavailable: %v", authErr)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if r.URL.Path == "/healthz" {
				h.healthz(w, r)
				return
			}
			writeAPIError(w, http.StatusServiceUnavailable, errors.New("Web Setup authentication state is unavailable"))
		})
	}
	h.setupAuth = auth
	return auth.middleware(mux)
}

func (h *realCatalogHandler) healthz(w http.ResponseWriter, _ *http.Request) {
	status := h.catalog.catalogStatus()
	writeAPIJSON(w, statusForCatalog(status), map[string]any{"healthy": status.CatalogAvailable, "refresh_running": status.RefreshRunning})
}

func writeAPIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, err error) {
	writeAPIJSON(w, status, map[string]any{"error": sanitizeStatusError(err)})
}

func decodeAPIJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, errors.New("invalid request body"))
		return false
	}
	return true
}

func (h *realCatalogHandler) apiConfig(w http.ResponseWriter, _ *http.Request) {
	writeAPIJSON(w, http.StatusOK, h.catalog.config.public())
}

func (h *realCatalogHandler) apiCatalogStatus(w http.ResponseWriter, _ *http.Request) {
	writeAPIJSON(w, http.StatusOK, h.catalog.catalogStatus())
}

func (h *realCatalogHandler) apiSetupStatus(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, statusForCatalog(h.catalog.catalogStatus()), map[string]any{
		"catalog": h.catalog.catalogStatus(), "provider": h.providerStatus(r.Context()), "plex": h.plexStatus(r.Context()),
		"configuration": h.catalog.config.public(), "authentication": "single_admin",
		"version": appVersion, "revision": appRevision,
	})
}

func statusForCatalog(status catalogStatusResponse) int {
	if status.CatalogAvailable {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

type refreshRequest struct {
	Concurrency int `json:"concurrency,omitempty"`
}

func decodeRefreshRequest(w http.ResponseWriter, r *http.Request) (refreshRequest, bool) {
	var request refreshRequest
	if r.ContentLength != 0 && !decodeAPIJSON(w, r, &request) {
		return request, false
	}
	if request.Concurrency != 0 && !slices.Contains(allowedRefreshConcurrency, request.Concurrency) {
		writeAPIError(w, http.StatusBadRequest, errors.New("concurrency must be one of 1, 2, 4, 6, 8, or 10"))
		return request, false
	}
	return request, true
}

func (h *realCatalogHandler) apiCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeRefreshRequest(w, r)
	if !ok {
		return
	}
	effective := request.Concurrency
	if effective == 0 {
		effective = h.catalog.config.refreshConcurrency()
	}
	if !h.catalog.triggerRefreshWithConcurrency("manual", request.Concurrency) {
		writeAPIJSON(w, http.StatusConflict, map[string]any{"accepted": false, "reason": "refresh already running"})
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "effective_concurrency": effective})
}

func (h *realCatalogHandler) apiCatalogRebuild(w http.ResponseWriter, r *http.Request) {
	request, ok := decodeRefreshRequest(w, r)
	if !ok {
		return
	}
	effective := request.Concurrency
	if effective == 0 {
		effective = h.catalog.config.refreshConcurrency()
	}
	if !h.catalog.triggerRebuildWithConcurrency("manual_full_rebuild", request.Concurrency) {
		writeAPIJSON(w, http.StatusConflict, map[string]any{"accepted": false, "reason": "refresh already running"})
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "mode": "full_rebuild", "effective_concurrency": effective})
}

func (h *realCatalogHandler) apiCatalogPreview(w http.ResponseWriter, r *http.Request) {
	var request librariesConfig
	if !decodeAPIJSON(w, r, &request) {
		return
	}
	ctx, cancel := previewTimeoutContext(r.Context())
	defer cancel()
	preview, err := h.catalog.previewCategoryImpact(ctx, request)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, preview)
}

func (h *realCatalogHandler) apiCatalogQuarantine(w http.ResponseWriter, _ *http.Request) {
	writeAPIJSON(w, http.StatusOK, h.catalog.quarantineStatus())
}

func (h *realCatalogHandler) apiCatalogQuarantineRetry(w http.ResponseWriter, _ *http.Request) {
	if !h.catalog.triggerRefreshWithConcurrency("manual_quarantine_retry", 1) {
		writeAPIJSON(w, http.StatusConflict, map[string]any{"accepted": false, "reason": "refresh already running"})
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "scope": "all_retryable_quarantine", "effective_concurrency": 1})
}

func (h *realCatalogHandler) apiCatalogQuarantineItemRetry(w http.ResponseWriter, r *http.Request) {
	item, ok := h.catalog.hasQuarantineItem(r.PathValue("kind"), r.PathValue("id"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, errors.New("quarantine item not found"))
		return
	}
	if !quarantineRetryable(item) {
		writeAPIError(w, http.StatusConflict, errors.New("quarantine item is not classified as retryable"))
		return
	}
	if !h.catalog.triggerRefreshWithConcurrency("manual_quarantine_item_retry", 1) {
		writeAPIJSON(w, http.StatusConflict, map[string]any{"accepted": false, "reason": "refresh already running"})
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "requested_kind": item.Kind, "requested_source_id": item.SourceID, "scope": "atomic_incremental_refresh", "effective_concurrency": 1})
}

func (h *realCatalogHandler) providerClient() (*xtream.Client, error) {
	return h.catalog.clientForConfig(h.catalog.config.snapshot())
}

func (h *realCatalogHandler) providerStatus(ctx context.Context) providerStatusResponse {
	config := h.catalog.config.snapshot()
	configured := config.Provider.BaseURL != "" && config.Provider.Username != "" && config.Provider.Password != ""
	result := providerStatusResponse{Configured: configured, Status: "not_configured"}
	if !configured {
		return result
	}
	client, err := h.providerClient()
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err = client.VODCategories(ctx)
	}
	if err != nil {
		result.Status, result.Error = "unreachable", sanitizeStatusError(err)
		return result
	}
	result.Reachable, result.Status = true, "connected"
	return result
}

func (h *realCatalogHandler) apiProviderStatus(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, http.StatusOK, h.providerStatus(r.Context()))
}

func (h *realCatalogHandler) apiProviderTest(w http.ResponseWriter, r *http.Request) {
	config := h.catalog.config.snapshot()
	if r.ContentLength != 0 {
		var request struct {
			BaseURL  string  `json:"base_url"`
			Username string  `json:"username"`
			Password *string `json:"password,omitempty"`
		}
		if !decodeAPIJSON(w, r, &request) {
			return
		}
		config.Provider.BaseURL = strings.TrimSpace(request.BaseURL)
		config.Provider.Username = strings.TrimSpace(request.Username)
		if request.Password != nil && *request.Password != "" {
			config.Provider.Password = *request.Password
		}
	}
	status := providerStatusResponse{Configured: config.Provider.BaseURL != "" && config.Provider.Username != "" && config.Provider.Password != "", Status: "not_configured"}
	client, err := h.catalog.clientForConfig(config)
	if err == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		_, err = client.VODCategories(ctx)
	}
	if err == nil {
		status.Reachable, status.Status = true, "connected"
	} else {
		status.Status, status.Error = "unreachable", sanitizeStatusError(err)
	}
	code := http.StatusOK
	if !status.Reachable {
		code = http.StatusBadGateway
	}
	writeAPIJSON(w, code, status)
}

func (h *realCatalogHandler) discoverCategories(ctx context.Context) (categoryResponse, error) {
	client, err := h.providerClient()
	if err != nil {
		return categoryResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	movies, err := client.VODCategories(ctx)
	if err != nil {
		return categoryResponse{}, err
	}
	tv, err := client.SeriesCategories(ctx)
	if err != nil {
		return categoryResponse{}, err
	}
	config := h.catalog.config.snapshot()
	result := categoryResponse{}
	for _, item := range movies {
		result.Movies = append(result.Movies, categoryOption{ID: item.ID, Name: item.Name, Selected: slices.Contains(config.Libraries.MovieCategoryIDs, item.ID)})
	}
	for _, item := range tv {
		result.TV = append(result.TV, categoryOption{ID: item.ID, Name: item.Name, Selected: slices.Contains(config.Libraries.TVCategoryIDs, item.ID)})
	}
	return result, nil
}

func (h *realCatalogHandler) apiProviderCategories(w http.ResponseWriter, r *http.Request) {
	result, err := h.discoverCategories(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (h *realCatalogHandler) apiProviderConfig(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BaseURL  string  `json:"base_url"`
		Username string  `json:"username"`
		Password *string `json:"password,omitempty"`
	}
	if !decodeAPIJSON(w, r, &request) {
		return
	}
	err := h.catalog.config.update(func(config *appConfig) error {
		config.Provider.BaseURL = strings.TrimSpace(request.BaseURL)
		config.Provider.Username = strings.TrimSpace(request.Username)
		if request.Password != nil && *request.Password != "" {
			config.Provider.Password = *request.Password
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	client, _ := h.providerClient()
	h.catalog.mu.Lock()
	h.catalog.client = client
	h.catalog.sources = make(map[string]*xtream.VODSource)
	h.catalog.capabilities = make(map[string]xtream.CapabilityProbe)
	h.catalog.mu.Unlock()
	writeAPIJSON(w, http.StatusOK, h.catalog.config.public().Provider)
}

func (h *realCatalogHandler) apiLibrariesConfig(w http.ResponseWriter, r *http.Request) {
	var request librariesConfig
	if !decodeAPIJSON(w, r, &request) {
		return
	}
	discovered, err := h.discoverCategories(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, fmt.Errorf("category validation failed: %w", err))
		return
	}
	validMovies, validTV := map[string]bool{}, map[string]bool{}
	for _, item := range discovered.Movies {
		validMovies[item.ID] = true
	}
	for _, item := range discovered.TV {
		validTV[item.ID] = true
	}
	for _, id := range request.MovieCategoryIDs {
		if !validMovies[id] {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("unknown movie category %q", id))
			return
		}
	}
	for _, id := range request.TVCategoryIDs {
		if !validTV[id] {
			writeAPIError(w, http.StatusBadRequest, fmt.Errorf("unknown TV category %q", id))
			return
		}
	}
	err = h.catalog.config.update(func(config *appConfig) error { config.Libraries = request; return nil })
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, map[string]any{"libraries": h.catalog.config.public().Libraries, "refresh_required": true})
}

func (h *realCatalogHandler) apiCatalogConfig(w http.ResponseWriter, r *http.Request) {
	var request catalogConfig
	if !decodeAPIJSON(w, r, &request) {
		return
	}
	err := h.catalog.config.update(func(config *appConfig) error { config.Catalog = request; return nil })
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	select {
	case h.catalog.scheduleWake <- struct{}{}:
	default:
	}
	writeAPIJSON(w, http.StatusOK, h.catalog.config.public().Catalog)
}

func (h *realCatalogHandler) plexStatus(ctx context.Context) plexStatusResponse {
	result := plexStatusResponse{MachineIdentifier: machineID, SecureURI: strings.TrimSpace(getenvDefault("WONKVOD_SECURE_URI", "")), TLSStatus: "unknown", CloudPresence: "not_available", BackendStatus: "healthy", SetupMode: "externally_managed_sidecar", ControlLibraries: "manual_verification_required"}
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(h.upstreamURL, "/")+"/identity", nil)
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		defer response.Body.Close()
		var document map[string]any
		if response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&document) == nil {
			result.IdentityReachable = true
			identity := document
			if container, ok := document["MediaContainer"].(map[string]any); ok {
				identity = container
			}
			result.Claimed = identity["claimed"]
			if identifier, ok := identity["machineIdentifier"].(string); ok && identifier != "" {
				result.MachineIdentifier = identifier
			}
		}
	}
	if err != nil {
		result.Error = sanitizeStatusError(err)
	}
	if parsed, parseErr := url.Parse(result.SecureURI); parseErr == nil && parsed.Scheme == "https" {
		transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
		client := &http.Client{Timeout: 3 * time.Second, Transport: transport}
		probe, _ := http.NewRequestWithContext(ctx, http.MethodGet, result.SecureURI+"/identity", nil)
		if response, probeErr := client.Do(probe); probeErr == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				result.TLSStatus = "valid"
			}
		}
	}
	result.SetupState, result.Ready = plexSetupState(result)
	return result
}

func plexSetupState(status plexStatusResponse) (string, bool) {
	if !status.IdentityReachable {
		return plexSetupWaitingForIdentity, false
	}
	if !plexClaimed(status.Claimed) {
		return plexSetupUnclaimed, false
	}
	if strings.TrimSpace(status.MachineIdentifier) == "" {
		return plexSetupWaitingForIdentity, false
	}
	if status.TLSStatus != "valid" {
		return plexSetupCertificatePending, false
	}
	return plexSetupReady, true
}

func plexClaimed(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}

func (h *realCatalogHandler) apiPlexStatus(w http.ResponseWriter, r *http.Request) {
	writeAPIJSON(w, http.StatusOK, h.plexStatus(r.Context()))
}

func getenvDefault(name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

var getenv = os.Getenv

var setupTemplate = template.Must(template.New("setup").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>WonkVOD Setup</title><link rel="icon" href="/assets/branding/wonvod_favicon.png">
<script>(function(){var t=localStorage.getItem('wonkvod-theme')||'system';document.documentElement.dataset.theme=t})()</script>
<style>
:root{color-scheme:light;--bg:#f5f6f8;--panel:#fff;--panel-2:#eef1f5;--header:#eef1f5;--text:#18202b;--muted:#657080;--border:#d5dae1;--accent:#c93939;--accent-hover:#ad3030;--success:#176b3a;--success-bg:#e9f7ef;--warning:#765600;--warning-bg:#fff7d6;--error:#a52727;--error-bg:#fdecec;--focus:#276ef1}
:root[data-theme="dark"]{color-scheme:dark;--bg:#11151b;--panel:#1b222c;--panel-2:#252e3a;--header:#0d1117;--text:#f4f6f8;--muted:#a9b2bf;--border:#394555;--accent:#e25454;--accent-hover:#ef6b6b;--success:#8cdaa8;--success-bg:#173526;--warning:#f0d57a;--warning-bg:#3c3213;--error:#ff9a9a;--error-bg:#3d1e22;--focus:#79a8ff}
@media(prefers-color-scheme:dark){:root[data-theme="system"]{color-scheme:dark;--bg:#11151b;--panel:#1b222c;--panel-2:#252e3a;--header:#0d1117;--text:#f4f6f8;--muted:#a9b2bf;--border:#394555;--accent:#e25454;--accent-hover:#ef6b6b;--success:#8cdaa8;--success-bg:#173526;--warning:#f0d57a;--warning-bg:#3c3213;--error:#ff9a9a;--error-bg:#3d1e22;--focus:#79a8ff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px system-ui,sans-serif}header{background:var(--header);padding:18px 5vw;border-bottom:1px solid var(--border)}header img{display:block;width:210px;max-width:55vw}nav{display:flex;align-items:center;gap:18px;padding:12px 5vw;background:var(--panel);border-bottom:1px solid var(--border);flex-wrap:wrap}nav a{color:var(--text);text-decoration:none}nav a:hover{text-decoration:underline}.theme-control{display:flex;align-items:center;gap:8px;margin-left:auto}.theme-control select,.inline-select{width:auto;margin:0;padding:6px 28px 6px 8px}main{max-width:1050px;margin:28px auto;padding:0 22px}.app-footer{text-align:center;color:var(--muted);font-size:.85rem;padding:8px 22px 28px}.nav-button{padding:6px 10px}.card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:20px;margin:14px 0}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px}.metric{font-size:1.8rem;font-weight:700}.result-grid .card{margin:0;padding:14px}.result-grid .metric{font-size:1.3rem}.muted{color:var(--muted)}button{background:var(--accent);color:#fff;border:0;border-radius:7px;padding:10px 15px;cursor:pointer;font-weight:600}button.secondary{background:var(--panel-2);color:var(--text);border:1px solid var(--border)}button:hover:not(:disabled){background:var(--accent-hover)}button.secondary:hover:not(:disabled){background:var(--border)}button:focus-visible,input:focus-visible,select:focus-visible{outline:3px solid var(--focus);outline-offset:2px}button:disabled{opacity:.55;cursor:not-allowed}input,select{width:100%;padding:9px;margin:5px 0 13px;background:var(--panel);color:var(--text);border:1px solid var(--border);border-radius:6px}label.option{display:block;padding:6px}label.option input{width:auto;margin-right:8px}.notice{border:1px solid var(--border);border-left-width:5px;border-radius:8px;padding:12px 14px;margin:14px 0}.notice strong{display:block;margin-bottom:3px}.notice.success{color:var(--success);background:var(--success-bg)}.notice.warning,.notice.running{color:var(--warning);background:var(--warning-bg)}.notice.error{color:var(--error);background:var(--error-bg)}.actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.actions label{display:flex;gap:8px;align-items:center}.spinner{display:inline-block;width:1em;height:1em;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:spin .8s linear infinite;vertical-align:-.15em;margin-right:7px}.progress-card{border-left:5px solid var(--accent)}.progress-track{height:14px;overflow:hidden;border-radius:999px;background:var(--panel-2);border:1px solid var(--border);margin:12px 0}.progress-fill{height:100%;background:var(--accent);transition:width .25s ease}.progress-fill.indeterminate{width:35%;animation:progress-slide 1.2s ease-in-out infinite}.progress-meta{display:flex;gap:18px;flex-wrap:wrap}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:9px;border-bottom:1px solid var(--border);vertical-align:top}.badge{display:inline-block;border:1px solid var(--border);border-radius:999px;padding:2px 8px;margin:2px;background:var(--panel-2)}@keyframes spin{to{transform:rotate(360deg)}}@keyframes progress-slide{0%{transform:translateX(-110%)}50%{transform:translateX(95%)}100%{transform:translateX(285%)}}
</style></head>
<body data-page="{{.Page}}"><header><img id="brand-logo" src="/assets/branding/wonkvodlogo_light.png" alt="WonkVOD"></header>
<nav><a href="/">Overview</a><a href="/plex">Plex</a><a href="/provider">Provider</a><a href="/categories">Categories</a><a href="/quarantine">Quarantine</a><a href="/settings">Settings</a><label class="theme-control" for="theme">Theme <select id="theme"><option value="system">System</option><option value="light">Light</option><option value="dark">Dark</option></select></label><button class="secondary nav-button" id="logout">Logout</button></nav><main id="app"><div class="card"><span class="spinner"></span>Loading…</div></main><footer class="app-footer">WonkVOD v{{.Version}}</footer>
<script>
const app=document.querySelector('#app'),page=document.body.dataset.page,theme=document.querySelector('#theme'),logo=document.querySelector('#brand-logo');let passwordResult;
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const fmt=v=>v?new Date(v).toLocaleString():'Never',duration=ms=>{ms=Math.max(0,Number(ms)||0);let s=Math.round(ms/1000),m=Math.floor(s/60);return m?m+'m '+(s%60)+'s':s+'s'};
const notice=(kind,title,detail='')=>'<div class="notice '+kind+'" role="status" aria-live="polite"><strong>'+esc(title)+'</strong>'+(detail?'<div>'+esc(detail)+'</div>':'')+'</div>';
let csrf='';async function api(path,opt={}){let method=(opt.method||'GET').toUpperCase(),headers=new Headers(opt.headers||{});if(!['GET','HEAD','OPTIONS'].includes(method))headers.set('X-WonkVOD-CSRF',csrf);let r=await fetch(path,{...opt,headers}),j;try{j=await r.json()}catch(_){j={}}if(r.status===401){location.replace('/login');throw Error('Session expired')}if(!r.ok)throw Error(j.error||j.reason||r.statusText);return j}
const concurrencySelect=(id,current)=>'<label>This refresh: <select class="inline-select" id="'+id+'"><option value="0">Default ('+current+')</option>'+[1,2,4,6,8,10].map(v=>'<option value="'+v+'">'+v+'</option>').join('')+'</select></label>';
const refreshOptions=id=>{let n=Number(document.querySelector('#'+id)?.value||0);return n?{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({concurrency:n})}:{method:'POST'} };
function activeTheme(){let v=document.documentElement.dataset.theme;return v==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):v}
function applyTheme(value){document.documentElement.dataset.theme=value;localStorage.setItem('wonkvod-theme',value);theme.value=value;logo.src='/assets/branding/wonkvodlogo_'+(activeTheme()==='dark'?'dark':'light')+'.png'}
applyTheme(localStorage.getItem('wonkvod-theme')||'system');theme.onchange=()=>applyTheme(theme.value);matchMedia('(prefers-color-scheme: dark)').addEventListener('change',()=>{if(theme.value==='system')applyTheme('system')});
document.querySelector('#logout').onclick=async()=>{try{await api('/api/auth/logout',{method:'POST'})}finally{location.replace('/login')}};
let overviewTimer=null,refreshBaseline=null;
function countDelta(now,before,key){let n=(now[key]||0)-(before?.[key]||0);return n?(n>0?'+':'')+n+' '+key:''}
function refreshProgress(c,elapsed,copy){let p=c.progress||{},m=c.metrics||{},labels={category_indexes:'Fetching category indexes',comparing:'Comparing with current catalog',enriching_movies:'Enriching movie metadata',enriching_shows:'Enriching TV metadata',validating:'Validating snapshot',promoting:'Promoting snapshot'},label=labels[p.phase]||'Preparing refresh',known=Boolean(p.determinate&&p.total>0),percent=known?Math.min(99,Math.floor((p.completed/p.total)*100)):0,bar=known?'<div class="progress-fill" style="width:'+percent+'%"></div>':'<div class="progress-fill indeterminate"></div>',numbers=known?'<strong>'+p.completed+' / '+p.total+' items · '+percent+'%</strong>':'<strong>Working…</strong>';return '<div class="card progress-card" role="status" aria-live="polite"><h2>Refreshing catalog — '+esc(label)+'</h2><div class="progress-track" role="progressbar" '+(known?'aria-valuemin="0" aria-valuemax="'+p.total+'" aria-valuenow="'+p.completed+'"':'aria-valuetext="In progress"')+'>'+bar+'</div><div class="progress-meta"><span>'+numbers+'</span><span>Elapsed: '+duration(elapsed)+'</span><span>Concurrency: '+esc(m.effective_concurrency||'—')+'</span><span>Mode: '+esc(m.mode||'incremental')+'</span></div><p class="muted">'+esc(copy)+' Current catalog remains available while refresh runs.</p></div>'}
async function overview(){clearTimeout(overviewTimer);let s=await api('/api/setup/status'),c=s.catalog,m=c.metrics||{},elapsed=c.refresh_running?Date.now()-new Date(c.last_refresh_started).getTime():c.last_refresh_duration_ms||0,mode=m.mode||'incremental';
let runningCopy=mode==='full_rebuild'?'Every selected item is being re-fetched; a long run is expected.':mode==='bootstrap'?'New category items require detail enrichment; a longer run is expected.':'This is an incremental catalog check.';if(mode==='incremental'&&elapsed>300000&&(m.detail_requests||0)<10)runningCopy='This normal incremental refresh is unusually long with few detail requests; check provider/network health.';
let state='';if(c.refresh_running)state=refreshProgress(c,elapsed,runningCopy);else if(c.last_refresh_result==='failed')state=notice('error','Refresh failed.','Your previous catalog is still active. '+(c.last_refresh_error||''));else if(c.last_refresh_result==='success'){let deltas=refreshBaseline?['movies','shows','seasons','episodes'].map(k=>countDelta(c,refreshBaseline,k)).filter(Boolean).join(' · '):'';state=notice(c.quarantine?'warning':'success',c.quarantine?'Refresh completed with '+c.quarantine+' quarantined items.':'Refresh complete in '+duration(c.last_refresh_duration_ms)+'.',c.quarantine?'Your catalog was promoted successfully. Review Quarantine for details.':deltas)}
let results=c.last_refresh_result==='success'?'<div class="grid result-grid">'+[['Checked',(m.movie_candidates||0)+(m.show_candidates||0)],['Reused',m.reused_unchanged||0],['Added',m.new||0],['Updated',m.changed||0],['Membership',m.membership_only||0],['Retried',m.quarantine_retries||0],['Detail completed',m.detail_requests_completed||0],['Detail skipped',m.detail_requests_skipped||0],['Concurrency',m.effective_concurrency||'—']].map(x=>'<div class="card"><div class="metric">'+x[1]+'</div>'+x[0]+'</div>').join('')+'</div>':'';
let saved=s.configuration.catalog.refresh_concurrency;app.innerHTML='<h1>Overview</h1><div class="grid">'+[['Movies',c.movies],['Shows',c.shows],['Seasons',c.seasons],['Episodes',c.episodes],['Quarantine',c.quarantine]].map(x=>'<div class="card"><div class="metric">'+x[1]+'</div>'+x[0]+'</div>').join('')+'</div>'+state+'<div class="card"><h2>Catalog</h2><p>Last success: '+fmt(c.last_successful_refresh)+' · Duration: '+duration(c.last_refresh_duration_ms)+'</p>'+results+'<p>Next refresh: '+fmt(c.next_scheduled_refresh)+'</p><p class="muted">Normal Refresh checks additions, removals, changes, and quarantined items while reusing unchanged records. Category expansion enriches new items. Full Rebuild re-fetches every selected item and may take a long time. A one-time concurrency selection never changes Settings.</p><div class="actions">'+concurrencySelect('run-concurrency',saved)+'<button id="refresh" '+(c.refresh_running?'disabled':'')+'>'+(c.refresh_running?'<span class="spinner"></span>Refreshing…':'Refresh Now')+'</button><button class="secondary" id="rebuild" '+(c.refresh_running?'disabled':'')+'>Full Rebuild</button></div></div><div class="card">Provider: '+esc(s.provider.status)+' · Plex identity: '+(s.plex.identity_reachable?'reachable':'unavailable')+'</div>';
document.querySelector('#refresh').onclick=async e=>{let b=e.currentTarget;b.disabled=true;b.innerHTML='<span class="spinner"></span>Starting…';refreshBaseline={movies:c.movies,shows:c.shows,seasons:c.seasons,episodes:c.episodes};try{await api('/api/catalog/refresh',refreshOptions('run-concurrency'));await overview()}catch(err){app.insertAdjacentHTML('afterbegin',notice('error','Refresh could not start.',err.message));b.disabled=false;b.textContent='Refresh Now'}};document.querySelector('#rebuild').onclick=async e=>{if(!confirm('Full Rebuild re-fetches details for every selected item and may take a long time. Continue?'))return;let b=e.currentTarget;b.disabled=true;b.innerHTML='<span class="spinner"></span>Starting…';try{await api('/api/catalog/rebuild',refreshOptions('run-concurrency'));await overview()}catch(err){app.insertAdjacentHTML('afterbegin',notice('error','Full Rebuild could not start.',err.message));b.disabled=false;b.textContent='Full Rebuild'}};if(c.refresh_running)overviewTimer=setTimeout(overview,1500)}
async function plex(){let p=await api('/api/plex/setup'),ready=p.ready?notice('success','Ready','The claimed Plex identity and secure endpoint are ready.'):notice('warning','Plex setup needs attention.','Current state: '+p.setup_state.replaceAll('_',' ')),managed=p.ready&&!p.setup_actions_available?'<p class="muted">Plex identity is externally managed. No setup actions are required.</p>':'';app.innerHTML='<h1>Plex</h1>'+ready+'<div class="card"><p>Setup mode: '+esc(p.setup_mode)+'</p><p>Identity: '+(p.identity_reachable?'Reachable':'Unavailable')+'</p><p>Machine: '+esc(p.machine_identifier)+'</p><p>Claimed: '+esc(p.claimed??'Unknown')+'</p><p>Secure URI: '+esc(p.secure_uri)+'</p><p>TLS: '+esc(p.tls_status)+'</p><p>Cloud presence: '+esc(p.cloud_presence)+'</p><p>Control libraries: '+esc(p.control_libraries)+'</p><p>Backend: '+esc(p.backend_status)+'</p>'+managed+'</div>'}
async function provider(){let c=await api('/api/config'),p=c.provider;app.innerHTML='<h1>Provider</h1><div class="card"><label>Server URL<input id="url" value="'+esc(p.base_url)+'"></label><label>Username<input id="user" value="'+esc(p.username)+'"></label><label>Password<input id="pass" type="password" value="" placeholder="'+(p.password_configured?'Configured — leave blank to keep':'Required')+'"></label><div class="actions"><button id="test">Test Connection</button><button id="save">Save</button></div><div id="result"></div></div>';let body=()=>({base_url:url.value,username:user.value,...(pass.value?{password:pass.value}:{})}),run=async(button,label,fn)=>{button.disabled=true;button.innerHTML='<span class="spinner"></span>'+label;result.innerHTML='';try{await fn();result.innerHTML=notice('success',label==='Testing…'?'Connected.':'Provider settings saved.')}catch(e){result.innerHTML=notice('error',label==='Testing…'?'Connection failed.':'Save failed.',e.message)}finally{button.disabled=false;button.textContent=label==='Testing…'?'Test Connection':'Save'}};test.onclick=()=>run(test,'Testing…',()=>api('/api/provider/test',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())}));save.onclick=()=>run(save,'Saving…',async()=>{await api('/api/config/provider',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())});pass.value=''})}
async function categories(){let [c,cfg]=await Promise.all([api('/api/provider/categories'),api('/api/config')]);let group=(name,x,k)=>'<div class="card"><h2>'+name+'</h2>'+x.map(v=>'<label class="option"><input type="checkbox" data-kind="'+k+'" value="'+esc(v.id)+'" '+(v.selected?'checked':'')+'>'+esc(v.name)+' <span class="muted">('+esc(v.id)+')</span></label>').join('')+'</div>';app.innerHTML='<h1>Categories</h1>'+group('Movie Categories',c.movies,'movie')+group('TV Categories',c.tv,'tv')+'<p class="muted">A catalog refresh is required after categories are saved.</p><div class="actions"><button class="secondary" id="preview">Preview Impact</button><button id="apply">Apply Categories</button></div><div id="impact"></div><div id="result"></div>';let ids=k=>[...document.querySelectorAll('[data-kind="'+k+'"]:checked')].map(x=>x.value),body=()=>({movie_category_ids:ids('movie'),tv_category_ids:ids('tv')});let render=p=>{let detail='Movies '+p.current_movies+' → '+p.result_movies+' (+'+p.new_movies+', -'+p.removed_movies+'); Shows '+p.current_shows+' → '+p.result_shows+' (+'+p.new_shows+', -'+p.removed_shows+'); '+p.membership_only+' membership-only changes; '+(p.movie_overlaps+p.show_overlaps)+' overlaps.';impact.innerHTML=notice(p.large_expansion?'warning':'success',p.large_expansion?'Large category expansion':'Category impact preview',detail+(p.large_expansion?' New items require detail enrichment. Suggested one-time concurrency: '+p.suggested_concurrency+'.':' No detail requests were used for this preview.'))};let doPreview=async()=>{preview.disabled=true;preview.innerHTML='<span class="spinner"></span>Checking…';try{let p=await api('/api/catalog/preview',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())});render(p);return p}finally{preview.disabled=false;preview.textContent='Preview Impact'}};preview.onclick=()=>doPreview().catch(e=>impact.innerHTML=notice('error','Preview failed.',e.message));apply.onclick=async()=>{apply.disabled=true;apply.innerHTML='<span class="spinner"></span>Checking…';result.innerHTML='';try{let p=await doPreview();if(p.large_expansion&&!confirm('This expansion adds approximately '+p.new_movies+' movies and '+p.new_shows+' shows. New items require detail enrichment and may take longer. Apply?'))return;await api('/api/config/libraries',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())});let suggested=p.suggested_concurrency||0;result.innerHTML=notice('success','Categories saved.','A catalog refresh is required before these changes appear in Plex. The active catalog remains unchanged.')+'<div class="card"><div class="actions">'+concurrencySelect('category-concurrency',cfg.catalog.refresh_concurrency)+'<button id="category-refresh">Refresh Now</button></div><p class="muted">'+(suggested?'Suggested for this expansion: '+suggested+'. ':'')+'The override applies once and does not change Settings.</p></div>';document.querySelector('#category-refresh').onclick=async e=>{let b=e.currentTarget;b.disabled=true;b.innerHTML='<span class="spinner"></span>Starting…';try{await api('/api/catalog/refresh',refreshOptions('category-concurrency'));result.innerHTML=notice('running','Refreshing expanded catalog…','New items require detail enrichment, so a longer run is expected. The current catalog remains live.')}catch(err){result.insertAdjacentHTML('afterbegin',notice('error','Refresh could not start.',err.message));b.disabled=false;b.textContent='Refresh Now'}};apply.textContent='Saved'}catch(e){result.innerHTML=notice('error','Categories were not saved.',e.message);apply.textContent='Apply Categories'}finally{apply.disabled=false}}}
async function quarantine(){let q=await api('/api/catalog/quarantine');if(!q.total){app.innerHTML='<h1>Quarantine</h1>'+notice('success','No quarantined items','The current catalog has no isolated metadata failures.');return}let buckets=Object.entries(q.reason_buckets).map(x=>'<span class="badge">'+esc(x[0])+': '+x[1]+'</span>').join(''),rows=q.items.map(x=>'<tr><td>'+esc(x.title||'Untitled')+'<br><span class="muted">'+esc(x.kind)+' · '+esc(x.source_id)+'</span></td><td>'+(x.categories||[]).map(c=>'<span class="badge">'+esc(c.name||c.id)+'</span>').join('')+'</td><td>'+esc(x.reason)+'<br><span class="muted">'+esc(x.reason_class||'legacy')+(x.http_status?' · HTTP '+x.http_status:'')+'</span></td><td>'+fmt(x.last_failure_at)+'</td><td>'+(x.retryable?'<button class="retry-one" data-kind="'+esc(x.kind)+'" data-id="'+esc(x.source_id)+'">Retry</button>':'<span class="muted">Not retryable</span>')+'</td></tr>').join(''),bulk=q.retryable?'<button id="retry-all">Retry retryable items</button>':'';app.innerHTML='<h1>Quarantine</h1><div class="grid">'+[['Total',q.total],['Movies',q.movies],['Shows',q.shows],['Episodes',q.episodes]].map(x=>'<div class="card"><div class="metric">'+x[1]+'</div>'+x[0]+'</div>').join('')+'</div><div class="card"><h2>Reasons</h2>'+buckets+'</div><div class="card"><div class="actions">'+bulk+'</div><p class="muted">Retry runs a provider-safe atomic incremental refresh at concurrency 1. The live catalog remains unchanged unless the complete candidate snapshot succeeds.</p><div id="retry-result"></div><table><thead><tr><th>Item</th><th>Categories</th><th>Failure</th><th>Last failure</th><th>Action</th></tr></thead><tbody>'+rows+'</tbody></table></div>';let run=async(path)=>{let r=document.querySelector('#retry-result');try{await api(path,{method:'POST'});r.innerHTML=notice('running','Retry refresh started.','All currently retryable quarantine entries are checked through the normal atomic promotion path at concurrency 1.')}catch(e){r.innerHTML=notice('error','Retry could not start.',e.message)}};let retryAll=document.querySelector('#retry-all');if(retryAll)retryAll.onclick=()=>run('/api/catalog/quarantine/retry');document.querySelectorAll('.retry-one').forEach(b=>b.onclick=()=>run('/api/catalog/quarantine/'+encodeURIComponent(b.dataset.kind)+'/'+encodeURIComponent(b.dataset.id)+'/retry'))}
async function settings(){let c=await api('/api/config');app.innerHTML='<h1>Settings</h1><div class="card"><h2>Catalog</h2><label>Catalog refresh interval<select id="interval">'+['1h0m0s','3h0m0s','6h0m0s','12h0m0s','24h0m0s'].map(v=>'<option '+(v===c.catalog.refresh_interval?'selected':'')+'>'+v+'</option>').join('')+'</select></label><label>Catalog refresh concurrency<select id="concurrency">'+[1,2,4,6,8,10].map(v=>'<option value="'+v+'" '+(v===c.catalog.refresh_concurrency?'selected':'')+'>'+v+'</option>').join('')+'</select></label><p class="muted">Controls simultaneous provider metadata requests during catalog refresh. Lower values are friendlier to providers. This does not control simultaneous video playback.</p><button id="save">Save</button><div id="result"></div></div><div class="card"><h2>Change Admin Password</h2><label>Current password<input id="current-password" type="password" autocomplete="current-password"></label><label>New password<input id="new-password" type="password" autocomplete="new-password" minlength="12"></label><label>Confirm new password<input id="confirm-password" type="password" autocomplete="new-password" minlength="12"></label><button id="change-password">Change Password</button><div id="password-result"></div><p class="muted">Changing the password invalidates all other sessions. Local recovery requires removing auth.json and restarting WonkVOD.</p></div>';save.onclick=async()=>{save.disabled=true;save.innerHTML='<span class="spinner"></span>Saving…';result.innerHTML='';try{await api('/api/config/catalog',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({refresh_interval:interval.value,refresh_concurrency:Number(concurrency.value)})});result.innerHTML=notice('success','Settings saved.','The next scheduled refresh has been recalculated. New refreshes will use the selected concurrency.');save.textContent='Saved'}catch(e){result.innerHTML=notice('error','Settings were not saved.',e.message);save.textContent='Save'}finally{save.disabled=false}};document.querySelector('#change-password').onclick=async e=>{let b=e.currentTarget;b.disabled=true;passwordResult=document.querySelector('#password-result');passwordResult.innerHTML='';try{let changed=await api('/api/auth/password',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({current_password:document.querySelector('#current-password').value,new_password:document.querySelector('#new-password').value,confirm_password:document.querySelector('#confirm-password').value})});csrf=changed.csrf_token;document.querySelectorAll('#current-password,#new-password,#confirm-password').forEach(x=>x.value='');passwordResult.innerHTML=notice('success','Administrator password changed.','Other sessions were invalidated.')}catch(err){passwordResult.innerHTML=notice('error','Password was not changed.',err.message)}finally{b.disabled=false}}}
(async()=>{try{let session=await fetch('/api/auth/session').then(r=>r.json());if(!session.authenticated){location.replace(session.setup_required?'/setup':'/login');return}csrf=session.csrf_token;if(page==='/')await overview();else if(page==='/plex')await plex();else if(page==='/provider')await provider();else if(page==='/categories')await categories();else if(page==='/quarantine')await quarantine();else await settings()}catch(e){app.innerHTML=notice('error','Unable to load this page.',e.message)}})();
</script></body></html>`))

type setupPageData struct {
	Page    string
	Version string
}

func writeSetupPage(w http.ResponseWriter, page string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = setupTemplate.Execute(w, setupPageData{Page: page, Version: appVersion})
}
