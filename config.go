package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	configVersion                    = 1
	defaultConfigStatePath           = "/var/lib/wonkvod/config.json"
	defaultCatalogRefreshConcurrency = 2
)

var allowedRefreshIntervals = []time.Duration{
	time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

var allowedRefreshConcurrency = []int{1, 2, 4, 6, 8, 10}

type providerConfig struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type librariesConfig struct {
	MovieCategoryIDs []string `json:"movie_category_ids"`
	TVCategoryIDs    []string `json:"tv_category_ids"`
}

type catalogConfig struct {
	RefreshInterval    string `json:"refresh_interval"`
	RefreshConcurrency int    `json:"refresh_concurrency"`
}

type appConfig struct {
	Version   int             `json:"version"`
	Provider  providerConfig  `json:"provider"`
	Libraries librariesConfig `json:"libraries"`
	Catalog   catalogConfig   `json:"catalog"`
}

type publicProviderConfig struct {
	BaseURL            string `json:"base_url"`
	Username           string `json:"username"`
	PasswordConfigured bool   `json:"password_configured"`
}

type publicConfig struct {
	Version   int                  `json:"version"`
	Provider  publicProviderConfig `json:"provider"`
	Libraries librariesConfig      `json:"libraries"`
	Catalog   catalogConfig        `json:"catalog"`
	LoadError string               `json:"load_error,omitempty"`
}

type configStore struct {
	mu      sync.RWMutex
	path    string
	config  appConfig
	loadErr string
}

func bootstrapConfig() appConfig {
	return appConfig{
		Version: configVersion,
		Provider: providerConfig{
			BaseURL:  strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_URL")),
			Username: strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_USER")),
			Password: strings.TrimSpace(os.Getenv("WONKVOD_XTREAM_PASS")),
		},
		Libraries: librariesConfig{
			MovieCategoryIDs: []string{"917"},
			TVCategoryIDs:    []string{"887", "926", "779"},
		},
		Catalog: catalogConfig{RefreshInterval: defaultCatalogRefresh.String(), RefreshConcurrency: defaultCatalogRefreshConcurrency},
	}
}

func configStatePath() string {
	if value := strings.TrimSpace(os.Getenv("WONKVOD_CONFIG_STATE")); value != "" {
		return value
	}
	return defaultConfigStatePath
}

func loadConfigStore(path string) (*configStore, error) {
	defaults := bootstrapConfig()
	store := &configStore{path: path, config: defaults}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := validateAppConfig(defaults, true); err != nil {
			store.loadErr = sanitizeStatusError(err)
			return store, nil
		}
		if err := validateAppConfig(defaults, false); err != nil {
			store.loadErr = "configuration has not been saved: " + sanitizeStatusError(err)
			return store, nil
		}
		if err := saveAppConfig(path, defaults); err != nil {
			return nil, fmt.Errorf("persist bootstrap configuration: %w", err)
		}
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	var persisted appConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		store.loadErr = "persisted configuration is invalid JSON; bootstrap defaults are active"
		return store, nil
	}
	migrated := normalizeAppConfig(&persisted)
	if err := validateAppConfig(persisted, false); err != nil {
		store.loadErr = "persisted configuration is invalid: " + sanitizeStatusError(err)
		return store, nil
	}
	if migrated {
		if err := saveAppConfig(path, persisted); err != nil {
			return nil, fmt.Errorf("persist migrated configuration: %w", err)
		}
	}
	store.config = persisted
	return store, nil
}

func normalizeAppConfig(config *appConfig) bool {
	if config.Catalog.RefreshConcurrency != 0 {
		return false
	}
	config.Catalog.RefreshConcurrency = defaultCatalogRefreshConcurrency
	return true
}

func validateAppConfig(config appConfig, allowIncompleteProvider bool) error {
	if config.Version != configVersion {
		return fmt.Errorf("unsupported configuration version %d", config.Version)
	}
	if config.Provider.BaseURL != "" {
		parsed, err := url.Parse(config.Provider.BaseURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("provider base URL must be an absolute HTTP(S) URL")
		}
	}
	if !allowIncompleteProvider && (config.Provider.BaseURL == "" || config.Provider.Username == "" || config.Provider.Password == "") {
		return errors.New("provider configuration is incomplete")
	}
	if len(config.Libraries.MovieCategoryIDs) == 0 || len(config.Libraries.TVCategoryIDs) == 0 {
		return errors.New("at least one movie and TV category must be enabled")
	}
	if hasBlankOrDuplicate(config.Libraries.MovieCategoryIDs) || hasBlankOrDuplicate(config.Libraries.TVCategoryIDs) {
		return errors.New("library category IDs must be non-empty and unique")
	}
	interval, err := time.ParseDuration(config.Catalog.RefreshInterval)
	if err != nil || !slices.Contains(allowedRefreshIntervals, interval) {
		return errors.New("refresh interval must be one of 1h, 3h, 6h, 12h, or 24h")
	}
	if !slices.Contains(allowedRefreshConcurrency, config.Catalog.RefreshConcurrency) {
		return errors.New("refresh concurrency must be one of 1, 2, 4, 6, 8, or 10")
	}
	return nil
}

func hasBlankOrDuplicate(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func saveAppConfig(path string, config appConfig) error {
	if err := validateAppConfig(config, false); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".config-*")
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

func (s *configStore) snapshot() appConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := s.config
	result.Libraries.MovieCategoryIDs = append([]string(nil), s.config.Libraries.MovieCategoryIDs...)
	result.Libraries.TVCategoryIDs = append([]string(nil), s.config.Libraries.TVCategoryIDs...)
	return result
}

func (s *configStore) public() publicConfig {
	config := s.snapshot()
	s.mu.RLock()
	loadErr := s.loadErr
	s.mu.RUnlock()
	return publicConfig{
		Version:   config.Version,
		Provider:  publicProviderConfig{BaseURL: config.Provider.BaseURL, Username: config.Provider.Username, PasswordConfigured: config.Provider.Password != ""},
		Libraries: config.Libraries, Catalog: config.Catalog, LoadError: loadErr,
	}
}

func (s *configStore) update(mutator func(*appConfig) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.config
	next.Libraries.MovieCategoryIDs = append([]string(nil), s.config.Libraries.MovieCategoryIDs...)
	next.Libraries.TVCategoryIDs = append([]string(nil), s.config.Libraries.TVCategoryIDs...)
	if err := mutator(&next); err != nil {
		return err
	}
	if err := validateAppConfig(next, false); err != nil {
		return err
	}
	if err := saveAppConfig(s.path, next); err != nil {
		return err
	}
	s.config = next
	s.loadErr = ""
	return nil
}

func (s *configStore) refreshInterval() time.Duration {
	interval, err := time.ParseDuration(s.snapshot().Catalog.RefreshInterval)
	if err != nil {
		return defaultCatalogRefresh
	}
	return interval
}

func (s *configStore) refreshConcurrency() int {
	concurrency := s.snapshot().Catalog.RefreshConcurrency
	if !slices.Contains(allowedRefreshConcurrency, concurrency) {
		return defaultCatalogRefreshConcurrency
	}
	return concurrency
}

func sanitizeStatusError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}
