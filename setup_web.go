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
	Error             string `json:"error,omitempty"`
}

func (h *realCatalogHandler) SetupHandler() http.Handler {
	mux := http.NewServeMux()
	assets, _ := fs.Sub(setupAssets, "assets/branding")
	mux.Handle("GET /assets/branding/", http.StripPrefix("/assets/branding/", http.FileServer(http.FS(assets))))
	for _, page := range []string{"/", "/plex", "/provider", "/categories", "/libraries", "/settings"} {
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
	mux.HandleFunc("GET /api/provider/status", h.apiProviderStatus)
	mux.HandleFunc("POST /api/provider/test", h.apiProviderTest)
	mux.HandleFunc("GET /api/provider/categories", h.apiProviderCategories)
	mux.HandleFunc("PUT /api/config/provider", h.apiProviderConfig)
	mux.HandleFunc("PUT /api/config/libraries", h.apiLibrariesConfig)
	mux.HandleFunc("PUT /api/config/catalog", h.apiCatalogConfig)
	mux.HandleFunc("GET /api/plex/status", h.apiPlexStatus)
	return setupAccessMiddleware(mux)
}

func (h *realCatalogHandler) healthz(w http.ResponseWriter, _ *http.Request) {
	status := h.catalog.catalogStatus()
	writeAPIJSON(w, statusForCatalog(status), map[string]any{"healthy": status.CatalogAvailable, "refresh_running": status.RefreshRunning})
}

// setupAccessMiddleware is the single future authentication boundary. v0 is
// deliberately unauthenticated and must only be exposed on a trusted LAN.
func setupAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
		"configuration": h.catalog.config.public(), "authentication": "disabled_trusted_network_only",
	})
}

func statusForCatalog(status catalogStatusResponse) int {
	if status.CatalogAvailable {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

func (h *realCatalogHandler) apiCatalogRefresh(w http.ResponseWriter, _ *http.Request) {
	if !h.catalog.triggerRefresh("manual") {
		writeAPIJSON(w, http.StatusConflict, map[string]any{"accepted": false, "reason": "refresh already running"})
		return
	}
	writeAPIJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
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
	result := plexStatusResponse{MachineIdentifier: machineID, SecureURI: strings.TrimSpace(getenvDefault("WONKVOD_SECURE_URI", "https://192-0-2-22.fixture.example.invalid:32499")), TLSStatus: "unknown", CloudPresence: "not_available", BackendStatus: "healthy"}
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
	return result
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
:root{color-scheme:light;--bg:#f5f6f8;--panel:#fff;--panel-2:#eef1f5;--text:#18202b;--muted:#657080;--border:#d5dae1;--accent:#c93939;--accent-hover:#ad3030;--success:#176b3a;--success-bg:#e9f7ef;--warning:#765600;--warning-bg:#fff7d6;--error:#a52727;--error-bg:#fdecec;--focus:#276ef1}
:root[data-theme="dark"]{color-scheme:dark;--bg:#11151b;--panel:#1b222c;--panel-2:#252e3a;--text:#f4f6f8;--muted:#a9b2bf;--border:#394555;--accent:#e25454;--accent-hover:#ef6b6b;--success:#8cdaa8;--success-bg:#173526;--warning:#f0d57a;--warning-bg:#3c3213;--error:#ff9a9a;--error-bg:#3d1e22;--focus:#79a8ff}
@media(prefers-color-scheme:dark){:root[data-theme="system"]{color-scheme:dark;--bg:#11151b;--panel:#1b222c;--panel-2:#252e3a;--text:#f4f6f8;--muted:#a9b2bf;--border:#394555;--accent:#e25454;--accent-hover:#ef6b6b;--success:#8cdaa8;--success-bg:#173526;--warning:#f0d57a;--warning-bg:#3c3213;--error:#ff9a9a;--error-bg:#3d1e22;--focus:#79a8ff}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px system-ui,sans-serif}header{background:#0d1117;padding:18px 5vw}header img{display:block;width:210px;max-width:55vw}nav{display:flex;align-items:center;gap:18px;padding:12px 5vw;background:var(--panel);border-bottom:1px solid var(--border);flex-wrap:wrap}nav a{color:var(--text);text-decoration:none}nav a:hover{text-decoration:underline}.theme-control{display:flex;align-items:center;gap:8px;margin-left:auto}.theme-control select{width:auto;margin:0;padding:6px 28px 6px 8px}main{max-width:1050px;margin:28px auto;padding:0 22px}.card{background:var(--panel);border:1px solid var(--border);border-radius:12px;padding:20px;margin:14px 0}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px}.metric{font-size:1.8rem;font-weight:700}.muted{color:var(--muted)}button{background:var(--accent);color:#fff;border:0;border-radius:7px;padding:10px 15px;cursor:pointer;font-weight:600}button:hover:not(:disabled){background:var(--accent-hover)}button:focus-visible,input:focus-visible,select:focus-visible{outline:3px solid var(--focus);outline-offset:2px}button:disabled{opacity:.55;cursor:not-allowed}input,select{width:100%;padding:9px;margin:5px 0 13px;background:var(--panel);color:var(--text);border:1px solid var(--border);border-radius:6px}label.option{display:block;padding:6px}label.option input{width:auto;margin-right:8px}.notice{border:1px solid var(--border);border-left-width:5px;border-radius:8px;padding:12px 14px;margin:14px 0}.notice strong{display:block;margin-bottom:3px}.notice.success{color:var(--success);background:var(--success-bg)}.notice.warning,.notice.running{color:var(--warning);background:var(--warning-bg)}.notice.error{color:var(--error);background:var(--error-bg)}.actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}.spinner{display:inline-block;width:1em;height:1em;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:spin .8s linear infinite;vertical-align:-.15em;margin-right:7px}@keyframes spin{to{transform:rotate(360deg)}}
</style></head>
<body data-page="{{.}}"><header><img id="brand-logo" src="/assets/branding/wonkvodlogo_light.png" alt="WonkVOD"></header>
<nav><a href="/">Overview</a><a href="/plex">Plex</a><a href="/provider">Provider</a><a href="/categories">Categories</a><a href="/settings">Settings</a><label class="theme-control" for="theme">Theme <select id="theme"><option value="system">System</option><option value="light">Light</option><option value="dark">Dark</option></select></label></nav><main id="app"><div class="card"><span class="spinner"></span>Loading…</div></main>
<script>
const app=document.querySelector('#app'),page=document.body.dataset.page,theme=document.querySelector('#theme'),logo=document.querySelector('#brand-logo');
const esc=s=>String(s??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const fmt=v=>v?new Date(v).toLocaleString():'Never',duration=ms=>{ms=Math.max(0,Number(ms)||0);let s=Math.round(ms/1000),m=Math.floor(s/60);return m?m+'m '+(s%60)+'s':s+'s'};
const notice=(kind,title,detail='')=>'<div class="notice '+kind+'" role="status" aria-live="polite"><strong>'+esc(title)+'</strong>'+(detail?'<div>'+esc(detail)+'</div>':'')+'</div>';
async function api(path,opt){let r=await fetch(path,opt),j;try{j=await r.json()}catch(_){j={}}if(!r.ok)throw Error(j.error||j.reason||r.statusText);return j}
function activeTheme(){let v=document.documentElement.dataset.theme;return v==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):v}
function applyTheme(value){document.documentElement.dataset.theme=value;localStorage.setItem('wonkvod-theme',value);theme.value=value;logo.src='/assets/branding/wonkvodlogo_'+(activeTheme()==='dark'?'dark':'light')+'.png'}
applyTheme(localStorage.getItem('wonkvod-theme')||'system');theme.onchange=()=>applyTheme(theme.value);matchMedia('(prefers-color-scheme: dark)').addEventListener('change',()=>{if(theme.value==='system')applyTheme('system')});
let overviewTimer=null,refreshBaseline=null;
function countDelta(now,before,key){let n=(now[key]||0)-(before?.[key]||0);return n?(n>0?'+':'')+n+' '+key:''}
async function overview(){clearTimeout(overviewTimer);let s=await api('/api/setup/status'),c=s.catalog,elapsed=c.refresh_running?Date.now()-new Date(c.last_refresh_started).getTime():c.last_refresh_duration_ms||0;
let state='';if(c.refresh_running)state=notice('running','Refreshing catalog…','Elapsed '+duration(elapsed)+'. Your current catalog remains available while refresh runs.');else if(c.last_refresh_result==='failed')state=notice('error','Refresh failed.','Your previous catalog is still active. '+(c.last_refresh_error||''));else if(c.last_refresh_result==='success'){let deltas=refreshBaseline?['movies','shows','seasons','episodes'].map(k=>countDelta(c,refreshBaseline,k)).filter(Boolean).join(' · '):'';state=notice('success','Refresh complete in '+duration(c.last_refresh_duration_ms)+'.',deltas)}
app.innerHTML='<h1>Overview</h1><div class="grid">'+[['Movies',c.movies],['Shows',c.shows],['Seasons',c.seasons],['Episodes',c.episodes]].map(x=>'<div class="card"><div class="metric">'+x[1]+'</div>'+x[0]+'</div>').join('')+'</div>'+state+'<div class="card"><h2>Catalog</h2><p>Last success: '+fmt(c.last_successful_refresh)+' · Duration: '+duration(c.last_refresh_duration_ms)+'</p><p>Next refresh: '+fmt(c.next_scheduled_refresh)+'</p><button id="refresh" '+(c.refresh_running?'disabled':'')+'>'+(c.refresh_running?'<span class="spinner"></span>Refreshing…':'Refresh Now')+'</button></div><div class="card">Provider: '+esc(s.provider.status)+' · Plex identity: '+(s.plex.identity_reachable?'reachable':'unavailable')+'</div>';
document.querySelector('#refresh').onclick=async e=>{let b=e.currentTarget;b.disabled=true;b.innerHTML='<span class="spinner"></span>Starting…';refreshBaseline={movies:c.movies,shows:c.shows,seasons:c.seasons,episodes:c.episodes};try{await api('/api/catalog/refresh',{method:'POST'});await overview()}catch(err){app.insertAdjacentHTML('afterbegin',notice('error','Refresh could not start.',err.message));b.disabled=false;b.textContent='Refresh Now'}};if(c.refresh_running)overviewTimer=setTimeout(overview,3000)}
async function plex(){let p=await api('/api/plex/status');app.innerHTML='<h1>Plex</h1><div class="card"><p>Identity: '+(p.identity_reachable?'Reachable':'Unavailable')+'</p><p>Machine: '+esc(p.machine_identifier)+'</p><p>Claimed: '+esc(p.claimed??'Unknown')+'</p><p>Secure URI: '+esc(p.secure_uri)+'</p><p>TLS: '+esc(p.tls_status)+'</p><p>Cloud presence: '+esc(p.cloud_presence)+'</p><p>Backend: '+esc(p.backend_status)+'</p></div>'}
async function provider(){let c=await api('/api/config'),p=c.provider;app.innerHTML='<h1>Provider</h1><div class="card"><label>Server URL<input id="url" value="'+esc(p.base_url)+'"></label><label>Username<input id="user" value="'+esc(p.username)+'"></label><label>Password<input id="pass" type="password" value="" placeholder="'+(p.password_configured?'Configured — leave blank to keep':'Required')+'"></label><div class="actions"><button id="test">Test Connection</button><button id="save">Save</button></div><div id="result"></div></div>';let body=()=>({base_url:url.value,username:user.value,...(pass.value?{password:pass.value}:{})}),run=async(button,label,fn)=>{button.disabled=true;button.innerHTML='<span class="spinner"></span>'+label;result.innerHTML='';try{await fn();result.innerHTML=notice('success',label==='Testing…'?'Connected.':'Provider settings saved.')}catch(e){result.innerHTML=notice('error',label==='Testing…'?'Connection failed.':'Save failed.',e.message)}finally{button.disabled=false;button.textContent=label==='Testing…'?'Test Connection':'Save'}};test.onclick=()=>run(test,'Testing…',()=>api('/api/provider/test',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())}));save.onclick=()=>run(save,'Saving…',async()=>{await api('/api/config/provider',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body())});pass.value=''})}
async function categories(){let c=await api('/api/provider/categories');let group=(name,x,k)=>'<div class="card"><h2>'+name+'</h2>'+x.map(v=>'<label class="option"><input type="checkbox" data-kind="'+k+'" value="'+esc(v.id)+'" '+(v.selected?'checked':'')+'>'+esc(v.name)+' <span class="muted">('+esc(v.id)+')</span></label>').join('')+'</div>';app.innerHTML='<h1>Categories</h1>'+group('Movie Categories',c.movies,'movie')+group('TV Categories',c.tv,'tv')+'<div class="actions"><button id="apply">Apply Categories</button><button id="category-refresh" hidden>Refresh Now</button></div><div id="result"></div>';let ids=k=>[...document.querySelectorAll('[data-kind="'+k+'"]:checked')].map(x=>x.value);apply.onclick=async()=>{apply.disabled=true;apply.innerHTML='<span class="spinner"></span>Saving…';result.innerHTML='';try{await api('/api/config/libraries',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({movie_category_ids:ids('movie'),tv_category_ids:ids('tv')})});result.innerHTML=notice('success','Categories saved.','A catalog refresh is required before these changes appear in Plex.');document.querySelector('#category-refresh').hidden=false;apply.textContent='Saved'}catch(e){result.innerHTML=notice('error','Categories were not saved.',e.message);apply.textContent='Apply Categories'}finally{apply.disabled=false}};document.querySelector('#category-refresh').onclick=async e=>{let b=e.currentTarget;b.disabled=true;b.innerHTML='<span class="spinner"></span>Starting…';try{await api('/api/catalog/refresh',{method:'POST'});result.innerHTML=notice('running','Refreshing catalog…','Your current catalog remains available. Follow progress on Overview.');b.textContent='Refreshing…'}catch(err){result.innerHTML=notice('error','Refresh could not start.',err.message);b.disabled=false;b.textContent='Refresh Now'}}}
async function settings(){let c=await api('/api/config');app.innerHTML='<h1>Settings</h1><div class="card"><label>Catalog refresh interval<select id="interval">'+['1h0m0s','3h0m0s','6h0m0s','12h0m0s','24h0m0s'].map(v=>'<option '+(v===c.catalog.refresh_interval?'selected':'')+'>'+v+'</option>').join('')+'</select></label><button id="save">Save</button><div id="result"></div></div>';save.onclick=async()=>{save.disabled=true;save.innerHTML='<span class="spinner"></span>Saving…';result.innerHTML='';try{await api('/api/config/catalog',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({refresh_interval:interval.value})});result.innerHTML=notice('success','Settings saved.','The next scheduled refresh has been recalculated.');save.textContent='Saved'}catch(e){result.innerHTML=notice('error','Settings were not saved.',e.message);save.textContent='Save'}finally{save.disabled=false}}}
(async()=>{try{if(page==='/')await overview();else if(page==='/plex')await plex();else if(page==='/provider')await provider();else if(page==='/categories')await categories();else await settings()}catch(e){app.innerHTML=notice('error','Unable to load this page.',e.message)}})();
</script></body></html>`))

func writeSetupPage(w http.ResponseWriter, page string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = setupTemplate.Execute(w, page)
}
