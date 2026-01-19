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
	// We need to be careful with flags in tests to avoid "redefined" panics.
	var portFlag int
	var dbFlag string

	fs := flag.CommandLine

	if fs.Lookup("port") == nil {
		fs.IntVar(&portFlag, "port", 0, "HTTP server port (env: VAPORTRAIL_HTTP_PORT)")
	}
	if fs.Lookup("db") == nil {
		fs.StringVar(&dbFlag, "db", "", "SQLite database path (env: VAPORTRAIL_DB_PATH)")
	}

	if !flag.Parsed() {
		flag.Parse()
	}

	// Check if flags were actually set by the user
	// This is a bit hacky with the default flag set, but standard for this simple pattern.
	if p := fs.Lookup("port"); p != nil {
		// If the flag was set (visited), use it.
		// Or if we can detect it has a non-zero value and we used 0 as default.
		// However, standard `flag` doesn't expose "was set" easily for the global set without looping Visit.

		isSet := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "port" {
				isSet = true
			}
		})

		if isSet {
			if val, err := strconv.Atoi(p.Value.String()); err == nil {
				cfg.HTTPPort = val
			}
		}
	}

	if d := fs.Lookup("db"); d != nil {
		isSet := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "db" {
				isSet = true
			}
		})

		if isSet {
			cfg.DBPath = d.Value.String()
		}
	}

	return cfg
}
