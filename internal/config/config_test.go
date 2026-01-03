package config

import (
	"os"
	"testing"
)

func TestLoad(t *testing.T) {
	// Save original env vars to restore later
	origPort := os.Getenv("VAPORTRAIL_HTTP_PORT")
	origDB := os.Getenv("VAPORTRAIL_DB_PATH")
	defer func() {
		os.Setenv("VAPORTRAIL_HTTP_PORT", origPort)
		os.Setenv("VAPORTRAIL_DB_PATH", origDB)
	}()

	t.Run("Defaults", func(t *testing.T) {
		os.Unsetenv("VAPORTRAIL_HTTP_PORT")
		os.Unsetenv("VAPORTRAIL_DB_PATH")

		cfg := Load()
		if cfg.HTTPPort != 8080 {
			t.Errorf("Expected default port 8080, got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "vaportrail.db" {
			t.Errorf("Expected default db path 'vaportrail.db', got '%s'", cfg.DBPath)
		}
	})

	t.Run("Environment Variables", func(t *testing.T) {
		os.Setenv("VAPORTRAIL_HTTP_PORT", "9090")
		os.Setenv("VAPORTRAIL_DB_PATH", "/tmp/test.db")

		cfg := Load()
		if cfg.HTTPPort != 9090 {
			t.Errorf("Expected port 9090, got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "/tmp/test.db" {
			t.Errorf("Expected db path '/tmp/test.db', got '%s'", cfg.DBPath)
		}
	})

	t.Run("Invalid Port", func(t *testing.T) {
		os.Setenv("VAPORTRAIL_HTTP_PORT", "invalid")

		cfg := Load()
		// Should fall back to default or ignore? Code ignores error, so keeps default.
		if cfg.HTTPPort != 8080 {
			t.Errorf("Expected default port 8080 when invalid, got %d", cfg.HTTPPort)
		}
	})
}
