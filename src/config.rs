//! Configuration module for VaporTrail.
//!
//! Loads configuration from environment variables with sensible defaults.

use std::env;

/// Server configuration loaded from environment variables.
#[derive(Debug, Clone)]
pub struct ServerConfig {
    /// HTTP port for the web server (default: 8080)
    pub http_port: u16,
    /// Path to the SQLite database file (default: "vaportrail.db")
    pub db_path: String,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            http_port: 8080,
            db_path: "vaportrail.db".to_string(),
        }
    }
}

impl ServerConfig {
    /// Load configuration from environment variables.
    ///
    /// Environment variables:
    /// - `VAPORTRAIL_HTTP_PORT`: HTTP port (default: 8080)
    /// - `VAPORTRAIL_DB_PATH`: Database file path (default: "vaportrail.db")
    pub fn load() -> Self {
        let mut cfg = Self::default();

        if let Ok(port_str) = env::var("VAPORTRAIL_HTTP_PORT") {
            if let Ok(port) = port_str.parse() {
                cfg.http_port = port;
            }
        }

        if let Ok(db_path) = env::var("VAPORTRAIL_DB_PATH") {
            cfg.db_path = db_path;
        }

        cfg
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_config() {
        let cfg = ServerConfig::default();
        assert_eq!(cfg.http_port, 8080);
        assert_eq!(cfg.db_path, "vaportrail.db");
    }
}
