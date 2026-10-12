package main

import (
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
)

func (h *realCatalogHandler) setupAuthManager() (*adminAuth, error) {
	h.setupAuthMu.Lock()
	defer h.setupAuthMu.Unlock()
	if h.setupAuth != nil || h.setupAuthErr != nil {
		return h.setupAuth, h.setupAuthErr
	}
	h.setupAuth, h.setupAuthErr = newAdminAuth(authStatePath(), log.Default())
	return h.setupAuth, h.setupAuthErr
}

func (a *adminAuth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		path := r.URL.Path
		if path == "/healthz" || strings.HasPrefix(path, "/assets/branding/") || path == "/login" || path == "/setup" || isPublicAuthAPI(path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.configured() {
			if strings.HasPrefix(path, "/api/") {
				writeAPIError(w, http.StatusServiceUnavailable, errors.New("administrator setup required"))
				return
			}
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		_, session, authenticated := a.session(r)
		if !authenticated {
			if strings.HasPrefix(path, "/api/") {
				writeAPIError(w, http.StatusUnauthorized, errors.New("authentication required"))
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if stateChangingMethod(r.Method) && subtleStringCompare(r.Header.Get("X-WonkVOD-CSRF"), session.CSRF) == false {
			writeAPIError(w, http.StatusForbidden, errors.New("invalid CSRF token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicAuthAPI(path string) bool {
	switch path {
	case "/api/auth/status", "/api/auth/session", "/api/auth/login", "/api/auth/setup":
		return true
	default:
		return false
	}
}

func stateChangingMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func subtleStringCompare(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}

func (h *realCatalogHandler) apiAuthStatus(w http.ResponseWriter, r *http.Request) {
	_, _, authenticated := h.setupAuth.session(r)
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"setup_required": !h.setupAuth.configured(), "authenticated": authenticated,
		"version": appVersion, "revision": appRevision,
	})
}

func (h *realCatalogHandler) apiAuthSession(w http.ResponseWriter, r *http.Request) {
	_, session, authenticated := h.setupAuth.session(r)
	response := map[string]any{
		"setup_required": !h.setupAuth.configured(), "authenticated": authenticated,
		"version": appVersion, "revision": appRevision,
	}
	if authenticated {
		response["csrf_token"] = session.CSRF
		response["expires_at"] = session.ExpiresAt.UTC()
	}
	writeAPIJSON(w, http.StatusOK, response)
}

func (h *realCatalogHandler) apiAuthSetup(w http.ResponseWriter, r *http.Request) {
	if h.setupAuth.configured() {
		writeAPIError(w, http.StatusConflict, errors.New("administrator password is already configured"))
		return
	}
	key := authClientKey(r, "setup")
	if allowed, retry := h.setupAuth.allowAttempt(key); !allowed {
		writeRateLimit(w, retry)
		return
	}
	var request struct {
		SetupToken string `json:"setup_token"`
		Password   string `json:"password"`
		Confirm    string `json:"confirm_password"`
	}
	if !decodeAPIJSON(w, r, &request) {
		h.setupAuth.recordFailure(key)
		return
	}
	if err := h.setupAuth.establishPassword(request.SetupToken, request.Password, request.Confirm); err != nil {
		h.setupAuth.recordFailure(key)
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	token, session, err := h.setupAuth.createSession()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("could not create session"))
		return
	}
	h.setupAuth.clearFailures(key)
	setAdminSessionCookie(w, r, token)
	writeAPIJSON(w, http.StatusCreated, map[string]any{"authenticated": true, "csrf_token": session.CSRF})
}

func (h *realCatalogHandler) apiAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !h.setupAuth.configured() {
		writeAPIError(w, http.StatusConflict, errors.New("administrator setup required"))
		return
	}
	key := authClientKey(r, "login")
	if allowed, retry := h.setupAuth.allowAttempt(key); !allowed {
		writeRateLimit(w, retry)
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if !decodeAPIJSON(w, r, &request) {
		h.setupAuth.recordFailure(key)
		return
	}
	if !h.setupAuth.verifyPassword(request.Password) {
		h.setupAuth.recordFailure(key)
		writeAPIError(w, http.StatusUnauthorized, errors.New("invalid credentials"))
		return
	}
	token, session, err := h.setupAuth.createSession()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("could not create session"))
		return
	}
	h.setupAuth.clearFailures(key)
	setAdminSessionCookie(w, r, token)
	writeAPIJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": session.CSRF})
}

func (h *realCatalogHandler) apiAuthLogout(w http.ResponseWriter, r *http.Request) {
	key, _, authenticated := h.setupAuth.session(r)
	if authenticated {
		h.setupAuth.deleteSession(key)
	}
	clearAdminSessionCookie(w, r)
	writeAPIJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (h *realCatalogHandler) apiAuthPassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Current  string `json:"current_password"`
		Password string `json:"new_password"`
		Confirm  string `json:"confirm_password"`
	}
	if !decodeAPIJSON(w, r, &request) {
		return
	}
	if err := h.setupAuth.changePassword(request.Current, request.Password, request.Confirm); err != nil {
		writeAPIError(w, http.StatusBadRequest, err)
		return
	}
	token, session, err := h.setupAuth.createSession()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errors.New("password changed; sign in again"))
		return
	}
	setAdminSessionCookie(w, r, token)
	writeAPIJSON(w, http.StatusOK, map[string]any{
		"changed": true, "other_sessions_invalidated": true, "csrf_token": session.CSRF,
	})
}

func (h *realCatalogHandler) loginPage(w http.ResponseWriter, r *http.Request) {
	if !h.setupAuth.configured() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if _, _, authenticated := h.setupAuth.session(r); authenticated {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeAuthPage(w, "login")
}

func (h *realCatalogHandler) bootstrapPage(w http.ResponseWriter, r *http.Request) {
	if h.setupAuth.configured() {
		if _, _, authenticated := h.setupAuth.session(r); authenticated {
			http.Redirect(w, r, "/", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
		return
	}
	writeAuthPage(w, "setup")
}

type authPageData struct {
	Mode    string
	Version string
}

var authPageTemplate = template.Must(template.New("auth").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>WonkVOD {{if eq .Mode "setup"}}Admin Setup{{else}}Login{{end}}</title><link rel="icon" href="/assets/branding/wonvod_favicon.png">
<style>:root{color-scheme:light dark}*{box-sizing:border-box}body{margin:0;background:#11151b;color:#f4f6f8;font:15px system-ui,sans-serif;min-height:100vh;display:grid;place-items:center}.panel{width:min(440px,calc(100% - 32px));background:#1b222c;border:1px solid #394555;border-radius:12px;padding:28px}.panel img{display:block;width:220px;max-width:80%;margin:0 auto 24px}label{display:block;margin-top:12px}input{width:100%;padding:10px;margin-top:5px;background:#11151b;color:#fff;border:1px solid #526174;border-radius:6px}button{width:100%;margin-top:18px;padding:11px;background:#e25454;color:#fff;border:0;border-radius:7px;font-weight:700;cursor:pointer}.muted{color:#a9b2bf}.error{background:#3d1e22;color:#ffb3b3;border-left:4px solid #e25454;padding:10px;margin-top:14px;border-radius:5px}footer{text-align:center;margin-top:20px;color:#a9b2bf;font-size:.85rem}</style></head>
<body data-mode="{{.Mode}}"><div class="panel"><img src="/assets/branding/wonkvodlogo_dark.png" alt="WonkVOD">
{{if eq .Mode "setup"}}<h1>Set up administrator</h1><p class="muted">Enter the one-time setup token from the local WonkVOD container logs, then choose an administrator password of at least 12 characters.</p><form id="auth"><label>Setup token<input id="token" autocomplete="one-time-code" required></label><label>New password<input id="password" type="password" autocomplete="new-password" minlength="12" required></label><label>Confirm password<input id="confirm" type="password" autocomplete="new-password" minlength="12" required></label><button>Set administrator password</button></form>
{{else}}<h1>Administrator login</h1><form id="auth"><label>Password<input id="password" type="password" autocomplete="current-password" required autofocus></label><button>Login</button></form>{{end}}<div id="result"></div><footer>WonkVOD v{{.Version}}</footer></div>
<script>const mode=document.body.dataset.mode,form=document.querySelector('#auth'),result=document.querySelector('#result');form.onsubmit=async e=>{e.preventDefault();result.textContent='';let body=mode==='setup'?{setup_token:token.value,password:password.value,confirm_password:confirm.value}:{password:password.value};try{let r=await fetch('/api/auth/'+mode,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}),j={};try{j=await r.json()}catch(_){}if(!r.ok)throw Error(j.error||r.statusText);location.replace('/')}catch(err){result.className='error';result.textContent=err.message}}</script></body></html>`))

func writeAuthPage(w http.ResponseWriter, mode string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = authPageTemplate.Execute(w, authPageData{Mode: mode, Version: appVersion})
}
