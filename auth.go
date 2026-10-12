package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	defaultAuthStatePath = "/var/lib/wonkvod/auth.json"
	authStateVersion     = 1
	adminSessionCookie   = "wonkvod_admin_session"
	adminSessionLifetime = 12 * time.Hour
	minimumPasswordRunes = 12
)

type argon2Parameters struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

var productionArgon2Parameters = argon2Parameters{
	Memory: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32,
}

type authState struct {
	Version       int       `json:"version"`
	PasswordHash  string    `json:"password_hash"`
	PasswordSetAt time.Time `json:"password_set_at"`
}

type adminSession struct {
	CSRF      string
	ExpiresAt time.Time
}

type authAttempt struct {
	Failures     int
	WindowStart  time.Time
	BlockedUntil time.Time
}

type adminAuth struct {
	mu                 sync.Mutex
	path               string
	parameters         argon2Parameters
	state              authState
	setupTokenVerifier [sha256.Size]byte
	sessions           map[[sha256.Size]byte]adminSession
	attempts           map[string]authAttempt
	now                func() time.Time
}

func authStatePath() string {
	if value := strings.TrimSpace(os.Getenv("WONKVOD_AUTH_STATE")); value != "" {
		return value
	}
	return defaultAuthStatePath
}

func newAdminAuth(path string, logger *log.Logger) (*adminAuth, error) {
	return newAdminAuthWithParameters(path, productionArgon2Parameters, logger)
}

func newAdminAuthWithParameters(path string, parameters argon2Parameters, logger *log.Logger) (*adminAuth, error) {
	auth := &adminAuth{
		path: path, parameters: parameters, sessions: make(map[[sha256.Size]byte]adminSession),
		attempts: make(map[string]authAttempt), now: time.Now,
	}
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, fmt.Errorf("stat auth state: %w", statErr)
		}
		if info.Mode().Perm() != 0o600 {
			return nil, fmt.Errorf("auth state %s must have mode 0600", path)
		}
		if err := json.Unmarshal(data, &auth.state); err != nil {
			return nil, errors.New("auth state is invalid JSON")
		}
		if auth.state.Version != authStateVersion || auth.state.PasswordHash == "" {
			return nil, errors.New("auth state has an unsupported or incomplete format")
		}
		if _, _, err := decodeArgon2Hash(auth.state.PasswordHash); err != nil {
			return nil, fmt.Errorf("auth state password hash is invalid: %w", err)
		}
		return auth, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read auth state: %w", err)
	}

	setupToken, err := randomToken(32)
	if err != nil {
		return nil, fmt.Errorf("generate setup token: %w", err)
	}
	auth.setupTokenVerifier = sha256.Sum256([]byte(setupToken))
	if logger != nil {
		logger.Printf("WonkVOD Web Setup one-time setup token: %s", setupToken)
		logger.Printf("Use this token once at /setup to establish the administrator password")
	}
	return auth, nil
}

func (a *adminAuth) configured() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.PasswordHash != ""
}

func (a *adminAuth) establishPassword(setupToken, password, confirmation string) error {
	if err := validateNewPassword(password, confirmation); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state.PasswordHash != "" {
		return errors.New("administrator password is already configured")
	}
	candidate := sha256.Sum256([]byte(strings.TrimSpace(setupToken)))
	if subtle.ConstantTimeCompare(candidate[:], a.setupTokenVerifier[:]) != 1 {
		return errors.New("invalid setup token")
	}
	encoded, err := encodeArgon2Hash(password, a.parameters)
	if err != nil {
		return err
	}
	state := authState{Version: authStateVersion, PasswordHash: encoded, PasswordSetAt: a.now().UTC()}
	if err := saveAuthState(a.path, state); err != nil {
		return fmt.Errorf("persist auth state: %w", err)
	}
	a.state = state
	a.setupTokenVerifier = [sha256.Size]byte{}
	a.sessions = make(map[[sha256.Size]byte]adminSession)
	return nil
}

func (a *adminAuth) verifyPassword(password string) bool {
	a.mu.Lock()
	encoded := a.state.PasswordHash
	a.mu.Unlock()
	if encoded == "" {
		return false
	}
	parameters, expected, err := decodeArgon2Hash(encoded)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), parameters.salt, parameters.Iterations, parameters.Memory, parameters.Parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func (a *adminAuth) changePassword(current, password, confirmation string) error {
	if err := validateNewPassword(password, confirmation); err != nil {
		return err
	}
	if !a.verifyPassword(current) {
		return errors.New("current password is incorrect")
	}
	encoded, err := encodeArgon2Hash(password, a.parameters)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	state := authState{Version: authStateVersion, PasswordHash: encoded, PasswordSetAt: a.now().UTC()}
	if err := saveAuthState(a.path, state); err != nil {
		return fmt.Errorf("persist auth state: %w", err)
	}
	a.state = state
	a.sessions = make(map[[sha256.Size]byte]adminSession)
	return nil
}

func validateNewPassword(password, confirmation string) error {
	if password != confirmation {
		return errors.New("password confirmation does not match")
	}
	if utf8.RuneCountInString(password) < minimumPasswordRunes {
		return fmt.Errorf("password must be at least %d characters", minimumPasswordRunes)
	}
	return nil
}

type decodedArgon2Hash struct {
	argon2Parameters
	salt []byte
}

func encodeArgon2Hash(password string, parameters argon2Parameters) (string, error) {
	salt := make([]byte, parameters.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, parameters.Iterations, parameters.Memory, parameters.Parallelism, parameters.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, parameters.Memory, parameters.Iterations, parameters.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func decodeArgon2Hash(encoded string) (decodedArgon2Hash, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return decodedArgon2Hash{}, nil, errors.New("unexpected hash encoding")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return decodedArgon2Hash{}, nil, errors.New("unsupported Argon2 version")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return decodedArgon2Hash{}, nil, errors.New("invalid Argon2 parameters")
	}
	if memory < 8*1024 || memory > 256*1024 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 8 {
		return decodedArgon2Hash{}, nil, errors.New("Argon2 parameters outside safe bounds")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return decodedArgon2Hash{}, nil, errors.New("invalid Argon2 salt")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return decodedArgon2Hash{}, nil, errors.New("invalid Argon2 hash")
	}
	return decodedArgon2Hash{argon2Parameters: argon2Parameters{Memory: memory, Iterations: iterations, Parallelism: parallelism, SaltLength: uint32(len(salt)), KeyLength: uint32(len(hash))}, salt: salt}, hash, nil
}

func saveAuthState(path string, state authState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (a *adminAuth) createSession() (string, adminSession, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", adminSession{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return "", adminSession{}, err
	}
	now := a.now()
	session := adminSession{CSRF: csrf, ExpiresAt: now.Add(adminSessionLifetime)}
	key := sha256.Sum256([]byte(token))
	a.mu.Lock()
	defer a.mu.Unlock()
	for existing, value := range a.sessions {
		if !value.ExpiresAt.After(now) {
			delete(a.sessions, existing)
		}
	}
	a.sessions[key] = session
	return token, session, nil
}

func (a *adminAuth) session(r *http.Request) ([sha256.Size]byte, adminSession, bool) {
	var empty [sha256.Size]byte
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil || cookie.Value == "" {
		return empty, adminSession{}, false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[key]
	if !ok || !session.ExpiresAt.After(a.now()) {
		delete(a.sessions, key)
		return empty, adminSession{}, false
	}
	return key, session, true
}

func (a *adminAuth) deleteSession(key [sha256.Size]byte) {
	a.mu.Lock()
	delete(a.sessions, key)
	a.mu.Unlock()
}

func (a *adminAuth) allowAttempt(key string) (bool, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	attempt := a.attempts[key]
	if now.Sub(attempt.WindowStart) > 10*time.Minute {
		attempt = authAttempt{WindowStart: now}
	}
	if attempt.WindowStart.IsZero() {
		attempt.WindowStart = now
	}
	a.attempts[key] = attempt
	if attempt.BlockedUntil.After(now) {
		return false, attempt.BlockedUntil.Sub(now)
	}
	return true, 0
}

func (a *adminAuth) recordFailure(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	attempt := a.attempts[key]
	if attempt.WindowStart.IsZero() || now.Sub(attempt.WindowStart) > 10*time.Minute {
		attempt = authAttempt{WindowStart: now}
	}
	attempt.Failures++
	if attempt.Failures >= 5 {
		backoff := time.Duration(attempt.Failures-4) * 30 * time.Second
		if backoff > 5*time.Minute {
			backoff = 5 * time.Minute
		}
		attempt.BlockedUntil = now.Add(backoff)
	}
	a.attempts[key] = attempt
}

func (a *adminAuth) clearFailures(key string) {
	a.mu.Lock()
	delete(a.attempts, key)
	a.mu.Unlock()
}

func authClientKey(r *http.Request, action string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	return action + ":" + host
}

func writeRateLimit(w http.ResponseWriter, retry time.Duration) {
	seconds := int(retry.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeAPIError(w, http.StatusTooManyRequests, errors.New("too many failed attempts; try again later"))
}

func setAdminSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: token, Path: "/", MaxAge: int(adminSessionLifetime / time.Second),
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}

func clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: adminSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}
