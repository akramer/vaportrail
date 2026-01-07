//! Database model types.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// A monitoring target configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Target {
    pub id: i64,
    pub name: String,
    pub address: String,
    pub probe_type: String,
    pub probe_config: String,
    pub probe_interval: f64,
    pub timeout: f64,
    pub retention_policies: String,
}

impl Default for Target {
    fn default() -> Self {
        Self {
            id: 0,
            name: String::new(),
            address: String::new(),
            probe_type: "ping".to_string(),
            probe_config: String::new(),
            probe_interval: 1.0,
            timeout: 5.0,
            retention_policies: "[]".to_string(),
        }
    }
}

/// Legacy result type (kept for compatibility).
#[derive(Debug, Clone)]
pub struct LegacyResult {
    pub time: DateTime<Utc>,
    pub target_id: i64,
    pub timeout_count: i64,
    pub tdigest_data: Vec<u8>,
}

/// A single raw probe result.
#[derive(Debug, Clone)]
pub struct RawResult {
    pub time: DateTime<Utc>,
    pub target_id: i64,
    /// Latency in nanoseconds, or -1.0 for timeout
    pub latency: f64,
}

/// An aggregated result for a time window.
#[derive(Debug, Clone)]
pub struct AggregatedResult {
    pub time: DateTime<Utc>,
    pub target_id: i64,
    pub window_seconds: i32,
    pub tdigest_data: Vec<u8>,
    pub timeout_count: i64,
}

/// TDigest storage statistics for the status page.
#[derive(Debug, Clone, Serialize)]
pub struct TDigestStat {
    pub target_name: String,
    pub window_seconds: i32,
    pub total_bytes: i64,
    pub count: i64,
    pub avg_bytes: f64,
}

/// Raw results statistics for the status page.
#[derive(Debug, Clone, Serialize)]
pub struct RawStats {
    pub count: i64,
    pub total_bytes: i64,
}
