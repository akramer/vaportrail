package config

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
