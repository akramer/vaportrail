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

	// Helper to load config for testing, ignoring errors unless specified
	loadConfig := func(args []string) *ServerConfig {
		cfg, err := LoadFromArgs(args)
		if err != nil {
			t.Fatalf("LoadFromArgs failed: %v", err)
		}
		return cfg
	}

	t.Run("Defaults", func(t *testing.T) {
		os.Unsetenv("VAPORTRAIL_HTTP_PORT")
		os.Unsetenv("VAPORTRAIL_DB_PATH")

		cfg := loadConfig(nil)
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

		cfg := loadConfig(nil)
		if cfg.HTTPPort != 9090 {
			t.Errorf("Expected port 9090, got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "/tmp/test.db" {
			t.Errorf("Expected db path '/tmp/test.db', got '%s'", cfg.DBPath)
		}
	})

	t.Run("Flags Override Defaults", func(t *testing.T) {
		os.Unsetenv("VAPORTRAIL_HTTP_PORT")
		os.Unsetenv("VAPORTRAIL_DB_PATH")

		cfg := loadConfig([]string{"-port", "7070", "-db", "flag.db"})
		if cfg.HTTPPort != 7070 {
			t.Errorf("Expected port 7070, got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "flag.db" {
			t.Errorf("Expected db path 'flag.db', got '%s'", cfg.DBPath)
		}
	})

	t.Run("Flags Override Env", func(t *testing.T) {
		os.Setenv("VAPORTRAIL_HTTP_PORT", "9090")
		os.Setenv("VAPORTRAIL_DB_PATH", "env.db")

		cfg := loadConfig([]string{"-port", "6060", "-db", "flag_override.db"})
		if cfg.HTTPPort != 6060 {
			t.Errorf("Expected port 6060 (flag), got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "flag_override.db" {
			t.Errorf("Expected db path 'flag_override.db' (flag), got '%s'", cfg.DBPath)
		}
	})

	t.Run("Mixed Env and Flags", func(t *testing.T) {
		// Env sets DB, Flag sets Port
		os.Setenv("VAPORTRAIL_HTTP_PORT", "9090")
		os.Setenv("VAPORTRAIL_DB_PATH", "env.db")

		cfg := loadConfig([]string{"-port", "5050"})
		if cfg.HTTPPort != 5050 {
			t.Errorf("Expected port 5050 (flag), got %d", cfg.HTTPPort)
		}
		if cfg.DBPath != "env.db" {
			t.Errorf("Expected db path 'env.db' (env), got '%s'", cfg.DBPath)
		}
	})

	t.Run("Invalid Port Env", func(t *testing.T) {
		os.Setenv("VAPORTRAIL_HTTP_PORT", "invalid")

		cfg := loadConfig(nil)
		// Should fall back to default
		if cfg.HTTPPort != 8080 {
			t.Errorf("Expected default port 8080 when invalid env, got %d", cfg.HTTPPort)
		}
	})

	t.Run("Unknown Flag", func(t *testing.T) {
		_, err := LoadFromArgs([]string{"-unknown"})
		if err == nil {
			t.Error("Expected error for unknown flag, got nil")
		}
	})
}
