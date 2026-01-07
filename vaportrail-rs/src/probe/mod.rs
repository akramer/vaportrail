//! Probe module for network monitoring.
//!
//! Supports HTTP, DNS, and Ping probes.

mod dns;
mod http;
mod ping;

pub use dns::*;
pub use http::*;
pub use ping::*;

use std::time::Duration;
use thiserror::Error;

/// Probe error types.
#[derive(Error, Debug)]
pub enum ProbeError {
    #[error("probe timed out after {0:?}")]
    Timeout(Duration),
    #[error("network error: {0}")]
    Network(String),
    #[error("invalid configuration: {0}")]
    Config(String),
    #[error("command failed: {0}")]
    Command(String),
}

/// Probe configuration.
#[derive(Debug, Clone)]
pub struct ProbeConfig {
    pub probe_type: String,
    pub address: String,
    pub timeout: Duration,
}

impl ProbeConfig {
    pub fn new(probe_type: &str, address: &str, timeout: Duration) -> Self {
        Self {
            probe_type: probe_type.to_string(),
            address: address.to_string(),
            timeout,
        }
    }
}

/// Run a probe with the given configuration.
///
/// Returns latency in nanoseconds on success.
pub async fn run_probe(config: &ProbeConfig) -> Result<f64, ProbeError> {
    // Add jitter to avoid thundering herd
    let jitter = rand::random::<u64>() % 100;
    tokio::time::sleep(Duration::from_millis(jitter)).await;

    let result = match config.probe_type.as_str() {
        "http" => run_http_probe(&config.address, config.timeout).await,
        "dns" => run_dns_probe(&config.address, config.timeout).await,
        "ping" => run_ping_probe(&config.address, config.timeout).await,
        other => Err(ProbeError::Config(format!("unknown probe type: {}", other))),
    };

    // Enforce timeout check
    if let Ok(latency) = &result {
        if *latency >= config.timeout.as_nanos() as f64 {
            return Err(ProbeError::Timeout(config.timeout));
        }
    }

    result
}
