package config

import (
	"flag"
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

// Load loads the configuration from command-line flags and environment variables.
// Priority order: command-line flags > environment variables > defaults.
func Load() *ServerConfig {
	cfg := DefaultConfig()

	// Apply environment variables first
	if portStr := os.Getenv("VAPORTRAIL_HTTP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.HTTPPort = port
		}
	}

	if dbPath := os.Getenv("VAPORTRAIL_DB_PATH"); dbPath != "" {
		cfg.DBPath = dbPath
	}

	// Define command-line flags (using env/default values as the flag defaults)
	portFlag := flag.Int("port", cfg.HTTPPort, "HTTP server port (env: VAPORTRAIL_HTTP_PORT)")
	dbFlag := flag.String("db", cfg.DBPath, "SQLite database path (env: VAPORTRAIL_DB_PATH)")

	flag.Parse()

	// Apply command-line flags (they override env values if explicitly set)
	cfg.HTTPPort = *portFlag
	cfg.DBPath = *dbFlag

	return cfg
}
