package config

import (
	"os"
	"strconv"
)

// ServerConfig holds the global configuration for the VaporTrail server.
// Probe-specific configurations are stored in the database.
type ServerConfig struct {
	// HTTPPort is the port the web server listens on.
	HTTPPort int
	// DBPath is the file path to the SQLite database.
	DBPath string
}

// DefaultConfig returns a default configuration.
func DefaultConfig() *ServerConfig {
	return &ServerConfig{
		HTTPPort: 8080,
		DBPath:   "vaportrail.db",
	}
}

// Load loads the configuration from environment variables,
// falling back to default values if not specified.
func Load() *ServerConfig {
	cfg := DefaultConfig()

	if portStr := os.Getenv("VAPORTRAIL_HTTP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.HTTPPort = port
		}
	}

	if dbPath := os.Getenv("VAPORTRAIL_DB_PATH"); dbPath != "" {
		cfg.DBPath = dbPath
	}

	return cfg
}
