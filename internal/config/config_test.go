package config

import (
	"strings"
	"testing"
)

// The values the Shopify CLI injects must beat the ones left in .env.
//
// This is the rule most likely to break silently: a stale APP_URL from
// yesterday's tunnel still looks perfectly valid, and the only symptom is
// Shopify refusing the OAuth callback with nothing that points at the cause.
func TestCLIEnvironmentBeatsDotEnv(t *testing.T) {
	setRequired(t)

	t.Setenv("APP_URL", "https://yesterday.trycloudflare.com")
	t.Setenv("HOST", "https://today.trycloudflare.com")
	t.Setenv("ADDRESS", ":8080")
	t.Setenv("BACKEND_PORT", "9123")
	t.Setenv("SHOPIFY_SCOPES", "read_products")
	t.Setenv("SCOPES", "read_products,write_inventory")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("config refused: %v", err)
	}

	if cfg.AppURL != "https://today.trycloudflare.com" {
		t.Errorf("AppURL = %q, want the tunnel the CLI just opened", cfg.AppURL)
	}
	if cfg.Address != ":9123" {
		t.Errorf("Address = %q, want the port the CLI tunnels to", cfg.Address)
	}
	if cfg.Scopes != "read_products,write_inventory" {
		t.Errorf("Scopes = %q, want the CLI's", cfg.Scopes)
	}
}

// Without the CLI, the ordinary settings are what count.
func TestWithoutTheCLIOrdinarySettingsApply(t *testing.T) {
	setRequired(t)

	t.Setenv("APP_URL", "https://tunnel.example.com/")
	t.Setenv("ADDRESS", ":8080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("config refused: %v", err)
	}

	// The trailing slash is stripped here so CallbackURL cannot produce a
	// double slash — an address Shopify would not recognise.
	if cfg.AppURL != "https://tunnel.example.com" {
		t.Errorf("AppURL = %q, trailing slash not stripped", cfg.AppURL)
	}
	if want := "https://tunnel.example.com/auth/callback"; cfg.CallbackURL() != want {
		t.Errorf("CallbackURL() = %q, want %q", cfg.CallbackURL(), want)
	}
	if cfg.Address != ":8080" {
		t.Errorf("Address = %q, want :8080", cfg.Address)
	}
}

// A short sync token is refused by name. That address can rewrite a store's
// whole inventory, so the service must not start pretending it is guarded.
func TestShortSyncTokenStopsStartup(t *testing.T) {
	setRequired(t)
	t.Setenv("SYNC_TOKEN", "too-short")

	_, err := Load()
	if err == nil {
		t.Fatal("a short SYNC_TOKEN was accepted")
	}
	if !strings.Contains(err.Error(), "SYNC_TOKEN") {
		t.Errorf("the error does not name what is wrong: %v", err)
	}
}

// setRequired fills everything Load insists on, so each test can vary only the
// one thing it is about.
func setRequired(t *testing.T) {
	t.Helper()

	for key, value := range map[string]string{
		"DATABASE_URL":       "postgres://localhost/test",
		"SHOPIFY_API_KEY":    "key",
		"SHOPIFY_API_SECRET": "secret",
		"APP_URL":            "https://tunnel.example.com",
		"SYNC_TOKEN":         strings.Repeat("t", 32),

		// Cleared so a value in the developer's own environment cannot change
		// what these tests see.
		"HOST":                "",
		"BACKEND_PORT":        "",
		"PORT":                "",
		"SCOPES":              "",
		"SHOPIFY_SCOPES":      "",
		"ADDRESS":             "",
		"SHOPIFY_API_VERSION": "",
		"WAREHOUSE_SOURCE":    "",
	} {
		t.Setenv(key, value)
	}
}
