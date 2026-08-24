// Package config reads the service's settings.
//
// Two sources, in a fixed order: **the environment wins, the `.env` file only
// fills in what is empty.** On a laptop nearly everything comes from `.env`; on
// a server that file does not exist at all. The case in between — a `.env` that
// travelled to the server by accident — cannot silently restore the old app
// credentials.
//
// If something is missing, the service stops at startup and names it. A Shopify
// app running without its API secret cannot verify a single incoming request,
// and that is worse than not running.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const EnvFile = ".env"

type Config struct {
	DatabaseURL string

	// App credentials from the Shopify Partner dashboard. APISecret does two
	// jobs at once: trading the authorization code for a token, and verifying
	// the HMAC on every incoming request — OAuth and webhooks alike.
	APIKey    string
	APISecret string

	// Permissions requested at install time. Adding a scope forces the
	// merchant to approve again; a scope that is never used only widens what is
	// lost if the token leaks.
	Scopes string

	// This service's public address. Shopify calls back here, so it must be
	// HTTPS and must match what is registered in the dashboard exactly.
	AppURL string

	// The Admin API version in use. Stated explicitly rather than silently
	// following the newest one: Shopify ships a new version every quarter and
	// retires the old one after a year. Moving up should be a decision, not a
	// morning surprise.
	APIVersion string

	// Where warehouse stock comes from: a JSON file or an HTTP address.
	WarehouseSource string

	// The secret every caller of POST /sync must carry. That address can
	// rewrite a store's entire inventory, so it must not be open merely
	// because it is not an address Shopify owns.
	SyncToken string

	Address      string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func Load() (Config, error) {
	loadEnvFile(EnvFile)

	cfg := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		APIKey:          os.Getenv("SHOPIFY_API_KEY"),
		APISecret:       os.Getenv("SHOPIFY_API_SECRET"),
		AppURL:          strings.TrimSuffix(os.Getenv("APP_URL"), "/"),
		SyncToken:       os.Getenv("SYNC_TOKEN"),
		Scopes:          valueOr("SHOPIFY_SCOPES", "read_products,read_inventory,write_inventory"),
		APIVersion:      valueOr("SHOPIFY_API_VERSION", "2025-01"),
		WarehouseSource: valueOr("WAREHOUSE_SOURCE", "warehouse.json"),
		Address:         valueOr("ADDRESS", ":8080"),
		ReadTimeout:     seconds("READ_TIMEOUT_SECONDS", 15),
		WriteTimeout:    seconds("WRITE_TIMEOUT_SECONDS", 30),
	}

	var missing []string
	for name, value := range map[string]string{
		"DATABASE_URL":       cfg.DatabaseURL,
		"SHOPIFY_API_KEY":    cfg.APIKey,
		"SHOPIFY_API_SECRET": cfg.APISecret,
		"APP_URL":            cfg.AppURL,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	// Deliberately a length check, not just a presence check. A short token can
	// be guessed, and whoever guesses it can rewrite a store's entire stock.
	if len(cfg.SyncToken) < 32 {
		missing = append(missing, "SYNC_TOKEN (at least 32 characters)")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("incomplete environment: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// CallbackURL is the address that must be registered in the Partner dashboard
// as an allowed redirection URL. Assembled in one place so that what is sent to
// Shopify at authorize time and what this service serves cannot drift apart.
func (c Config) CallbackURL() string { return c.AppURL + "/auth/callback" }

// loadEnvFile fills the environment from a file of KEY=value lines.
//
// Anything already set in the environment is left alone — see the reason above.
func loadEnvFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()

	lines := bufio.NewScanner(file)
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !found {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func seconds(key string, fallback int) time.Duration {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n <= 0 {
		n = fallback
	}
	return time.Duration(n) * time.Second
}

func valueOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
