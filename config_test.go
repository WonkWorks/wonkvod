package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigBootstrapPersistenceAndSecretRedaction(t *testing.T) {
	t.Setenv("WONKVOD_XTREAM_URL", "https://provider.example")
	t.Setenv("WONKVOD_XTREAM_USER", "viewer")
	t.Setenv("WONKVOD_XTREAM_PASS", "super-secret")
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := loadConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.snapshot().Libraries; len(got.MovieCategoryIDs) != 1 || got.MovieCategoryIDs[0] != "917" || strings.Join(got.TVCategoryIDs, ",") != "887,926,779" {
		t.Fatalf("bootstrap categories = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	publicJSON, _ := json.Marshal(store.public())
	if strings.Contains(string(publicJSON), "super-secret") || !store.public().Provider.PasswordConfigured {
		t.Fatalf("public configuration leaked or lost password state: %s", publicJSON)
	}
	if err := store.update(func(config *appConfig) error { config.Provider.Username = "updated"; return nil }); err != nil {
		t.Fatal(err)
	}
	if store.snapshot().Provider.Password != "super-secret" {
		t.Fatal("omitted password was not retained")
	}
}

func TestConfigRejectsInvalidUpdateWithoutPromotion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	config := bootstrapConfig()
	config.Provider = providerConfig{BaseURL: "https://provider.example", Username: "user", Password: "secret"}
	if err := saveAppConfig(path, config); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	store, err := loadConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(func(config *appConfig) error { config.Catalog.RefreshInterval = "10s"; return nil }); err == nil {
		t.Fatal("invalid interval accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid configuration was promoted")
	}
}

func TestInvalidPersistedConfigFallsBackWithDiagnostic(t *testing.T) {
	t.Setenv("WONKVOD_XTREAM_URL", "https://provider.example")
	t.Setenv("WONKVOD_XTREAM_USER", "viewer")
	t.Setenv("WONKVOD_XTREAM_PASS", "secret")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"provider":`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := loadConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.public().LoadError == "" || store.snapshot().Provider.Username != "viewer" {
		t.Fatalf("invalid persisted configuration did not produce safe diagnostic fallback: %#v", store.public())
	}
}
