package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fastAuthParameters() argon2Parameters {
	return argon2Parameters{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
}

func setupTokenFromLog(t *testing.T, output string) string {
	t.Helper()
	const marker = "one-time setup token: "
	index := strings.Index(output, marker)
	if index < 0 {
		t.Fatalf("setup token was not logged locally: %q", output)
	}
	return strings.TrimSpace(strings.SplitN(output[index+len(marker):], "\n", 2)[0])
}

func TestAdminAuthBootstrapPersistsOnlyArgon2Verifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	var logs bytes.Buffer
	auth, err := newAdminAuthWithParameters(path, fastAuthParameters(), log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if auth.configured() {
		t.Fatal("new auth state was unexpectedly configured")
	}
	token := setupTokenFromLog(t, logs.String())
	password := "correct horse battery staple"
	if err := auth.establishPassword(token, password, password); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth state mode=%o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, password) || strings.Contains(text, token) || !strings.Contains(text, "$argon2id$") {
		t.Fatalf("unsafe auth state: %s", text)
	}
	if err := auth.establishPassword(token, password, password); err == nil {
		t.Fatal("one-time setup token remained usable")
	}
	reloaded, err := newAdminAuthWithParameters(path, fastAuthParameters(), log.New(io.Discard, "", 0))
	if err != nil || !reloaded.verifyPassword(password) {
		t.Fatalf("persisted password did not verify: %v", err)
	}
}

func pendingAuthHandler(t *testing.T) (http.Handler, string, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	auth, err := newAdminAuthWithParameters(filepath.Join(t.TempDir(), "auth.json"), fastAuthParameters(), log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	handler := &realCatalogHandler{setupAuth: auth}
	return handler.SetupHandler(), setupTokenFromLog(t, logs.String()), &logs
}

func TestBootstrapPasswordConfirmationSubmission(t *testing.T) {
	const password = "matching-password-long"
	server, token, logs := pendingAuthHandler(t)
	payload, err := json.Marshal(map[string]string{"setup_token": token, "password": password, "confirm_password": password})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("matching confirmation status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(logs.String(), password) || strings.Contains(response.Body.String(), password) {
		t.Fatal("bootstrap password appeared in runtime output")
	}
}

func TestBootstrapRejectsDifferingPasswordConfirmation(t *testing.T) {
	const password = "matching-password-long"
	const confirmation = "different-password-long"
	server, token, logs := pendingAuthHandler(t)
	payload, err := json.Marshal(map[string]string{"setup_token": token, "password": password, "confirm_password": confirmation})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "password confirmation does not match") {
		t.Fatalf("differing confirmation status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(logs.String(), password) || strings.Contains(logs.String(), confirmation) || strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), confirmation) {
		t.Fatal("rejected bootstrap password appeared in runtime output")
	}
}

func TestBootstrapPageBuildsConfirmationPayloadFromExplicitInputs(t *testing.T) {
	server, _, _ := pendingAuthHandler(t)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setup", nil))
	page := response.Body.String()
	for _, expected := range []string{
		`tokenInput=document.querySelector('#token')`,
		`passwordInput=document.querySelector('#password')`,
		`confirmInput=document.querySelector('#confirm')`,
		`setup_token:tokenInput.value`,
		`password:passwordInput.value`,
		`confirm_password:confirmInput.value`,
	} {
		if !strings.Contains(page, expected) {
			t.Fatalf("setup page missing explicit payload binding %q", expected)
		}
	}
	if strings.Contains(page, "confirm_password:confirm.value") {
		t.Fatal("setup page still reads the window.confirm collision")
	}
}

func TestWebAuthBoundaryLoginCSRFAndLogout(t *testing.T) {
	handler, _ := setupFixture(t)
	server := handler.SetupHandler()

	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status=%d", unauthorized.Code)
	}
	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("public health status=%d", health.Code)
	}

	login := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password-long"}`))
	request.Header.Set("Content-Type", "application/json")
	server.ServeHTTP(login, request)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var loginBody struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginBody); err != nil || loginBody.CSRF == "" {
		t.Fatalf("login response=%s err=%v", login.Body.String(), err)
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure {
		t.Fatalf("HTTP cookie flags=%#v", cookie)
	}

	config := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.AddCookie(cookie)
	server.ServeHTTP(config, request)
	if config.Code != http.StatusOK {
		t.Fatalf("authenticated API status=%d", config.Code)
	}
	missingCSRF := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(cookie)
	server.ServeHTTP(missingCSRF, request)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", missingCSRF.Code)
	}
	logout := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(cookie)
	request.Header.Set("X-WonkVOD-CSRF", loginBody.CSRF)
	server.ServeHTTP(logout, request)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status=%d body=%s", logout.Code, logout.Body.String())
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	handler, _ := setupFixture(t)
	server := handler.SetupHandler()
	firstToken, firstSession, err := handler.setupAuth.createSession()
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := handler.setupAuth.createSession()
	if err != nil {
		t.Fatal(err)
	}
	change := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/auth/password", strings.NewReader(`{"current_password":"test-password-long","new_password":"new-password-longer","confirm_password":"new-password-longer"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-WonkVOD-CSRF", firstSession.CSRF)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: firstToken})
	server.ServeHTTP(change, request)
	if change.Code != http.StatusOK || !handler.setupAuth.verifyPassword("new-password-longer") {
		t.Fatalf("password change status=%d body=%s", change.Code, change.Body.String())
	}
	oldSession := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: secondToken})
	server.ServeHTTP(oldSession, request)
	if oldSession.Code != http.StatusUnauthorized {
		t.Fatalf("other session survived password change: %d", oldSession.Code)
	}
}

func TestLoginRateLimitAndSecureCookie(t *testing.T) {
	handler, _ := setupFixture(t)
	server := handler.SetupHandler()
	for attempt := 0; attempt < 5; attempt++ {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"wrong-password"}`))
		request.RemoteAddr = "192.0.2.1:1234"
		server.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d", attempt, response.Code)
		}
	}
	blocked := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"password":"test-password-long"}`))
	request.RemoteAddr = "192.0.2.1:1234"
	server.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit status=%d headers=%v", blocked.Code, blocked.Header())
	}

	secure := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "https://example.test/api/auth/login", strings.NewReader(`{"password":"test-password-long"}`))
	request.RemoteAddr = "192.0.2.2:1234"
	server.ServeHTTP(secure, request)
	if secure.Code != http.StatusOK || len(secure.Result().Cookies()) != 1 || !secure.Result().Cookies()[0].Secure {
		t.Fatalf("secure login status=%d cookies=%#v", secure.Code, secure.Result().Cookies())
	}
}

func TestSessionExpiration(t *testing.T) {
	auth := configuredTestAuth(t)
	now := time.Now()
	auth.now = func() time.Time { return now }
	token, _, err := auth.createSession()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	if _, _, ok := auth.session(request); !ok {
		t.Fatal("new session was not valid")
	}
	auth.now = func() time.Time { return now.Add(adminSessionLifetime + time.Second) }
	if _, _, ok := auth.session(request); ok {
		t.Fatal("expired session remained valid")
	}
}
