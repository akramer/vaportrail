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
	cfg, err := LoadFromArgs(os.Args[1:])
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		os.Exit(2)
	}
	return cfg
}

// LoadFromArgs loads configuration from the provided arguments.
// It allows for easier testing by avoiding global state.
func LoadFromArgs(args []string) (*ServerConfig, error) {
	cfg := DefaultConfig()

	// 1. Start with Defaults (already in cfg)

	// 2. Override with Environment Variables
	if portStr := os.Getenv("VAPORTRAIL_HTTP_PORT"); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			cfg.HTTPPort = port
		}
	}

	if dbPath := os.Getenv("VAPORTRAIL_DB_PATH"); dbPath != "" {
		cfg.DBPath = dbPath
	}

	// 3. Override with Flags
	// We use a custom FlagSet to avoid global state and enable safe testing.
	// We set the default values of the flags to the current values of cfg.
	// This ensures that if a flag is NOT provided, the value remains what it was (Default or Env).
	// If a flag IS provided, it overwrites the value.

	fs := flag.NewFlagSet("vaportrail", flag.ContinueOnError)

	fs.IntVar(&cfg.HTTPPort, "port", cfg.HTTPPort, "HTTP server port (env: VAPORTRAIL_HTTP_PORT)")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "SQLite database path (env: VAPORTRAIL_DB_PATH)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	return cfg, nil
}
