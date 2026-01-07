//! Rollup manager for aggregating probe results.

use crate::db::{
    deserialize_tdigest, serialize_tdigest, get_tdigest_stats, AggregatedResult, Store, Target,
};

use chrono::{DateTime, Duration as ChronoDuration, Utc};
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use std::time::Duration;
use tdigest::TDigest;
use tokio::sync::Mutex;

/// A retention policy for a data window.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetentionPolicy {
    pub window: i32,
    pub retention: i64,
}

/// Default retention policies.
pub fn default_policies() -> Vec<RetentionPolicy> {
    vec![
        RetentionPolicy { window: 0, retention: 604800 },       // Raw: 7 days
        RetentionPolicy { window: 60, retention: 15768000 },    // 1m: 6 months
        RetentionPolicy { window: 300, retention: 31536000 },   // 5m: 1 year
        RetentionPolicy { window: 3600, retention: 315360000 }, // 1h: 10 years
        RetentionPolicy { window: 86400, retention: 3153600000 }, // 1d: ~100 years
    ]
}

/// Return default policies as JSON string.
pub fn default_policies_json() -> String {
    serde_json::to_string(&default_policies()).unwrap_or_else(|_| "[]".to_string())
}

/// Validate retention policies.
pub fn validate_retention_policies(policies: &[RetentionPolicy]) -> Result<(), String> {
    let mut sorted = policies.to_vec();
    sorted.sort_by_key(|p| p.window);

    for (i, p) in sorted.iter().enumerate() {
        if p.window < 0 {
            return Err("retention window cannot be negative".to_string());
        }
        
        if i > 0 {
            let prev_window = sorted[i - 1].window;
            if prev_window > 0 && p.window % prev_window != 0 {
                return Err(format!(
                    "window {} is not a multiple of smaller window {}",
                    p.window, prev_window
                ));
            }
        }
    }

    Ok(())
}

/// Parse retention policies from JSON.
pub fn get_retention_policies(target: &Target) -> Option<Vec<RetentionPolicy>> {
    if target.retention_policies.is_empty() || target.retention_policies == "[]" {
        return None;
    }
    
    serde_json::from_str(&target.retention_policies).ok()
}

/// Manager for rolling up raw data into time windows.
pub struct RollupManager {
    store: Arc<Store>,
    _stop: Arc<Mutex<Option<tokio::sync::broadcast::Sender<()>>>>,
}

impl RollupManager {
    pub fn new(store: Arc<Store>) -> Self {
        Self {
            store,
            _stop: Arc::new(Mutex::new(None)),
        }
    }

    /// Start the rollup manager background task.
    pub fn start(&self) {
        let store = self.store.clone();

        tokio::spawn(async move {
            let mut interval = tokio::time::interval(Duration::from_secs(10));

            loop {
                interval.tick().await;
                process_rollups(&store);
            }
        });
    }
}

fn process_rollups(store: &Store) {
    let targets = match store.get_targets() {
        Ok(t) => t,
        Err(e) => {
            tracing::error!("RollupManager: Failed to get targets: {}", e);
            return;
        }
    };

    for target in targets {
        let policies = match get_retention_policies(&target) {
            Some(p) => p,
            None => continue,
        };

        let mut sorted_policies = policies;
        sorted_policies.sort_by_key(|p| p.window);

        let mut last_window = 0;
        for policy in sorted_policies {
            if policy.window == 0 {
                last_window = 0;
                continue;
            }

            process_target_window(store, &target, policy.window, last_window);
            last_window = policy.window;
        }
    }
}

/// Process rollups for a specific target and window size.
/// This is the core rollup logic that advances through time windows.
pub fn process_target_window(store: &Store, target: &Target, window_seconds: i32, source_window: i32) {
    // 1. Get last rollup time for this window
    let (start, is_first_rollup) = match store.get_last_rollup_time(target.id, window_seconds) {
        Ok(Some(last_time)) => {
            // We have a previous rollup - its time is the START of that window
            // So next window starts at last_time + window_seconds
            (last_time, false)
        }
        Ok(None) => {
            // No previous rollup - find earliest raw data and truncate to window boundary
            match store.get_earliest_raw_result_time(target.id) {
                Ok(Some(earliest)) => {
                    let truncated = truncate_to_window(earliest, window_seconds);
                    (truncated, true)
                }
                Ok(None) => return, // No raw data to process
                Err(e) => {
                    tracing::error!("RollupManager: Error getting earliest time: {}", e);
                    return;
                }
            }
        }
        Err(e) => {
            tracing::error!("RollupManager: Failed to get last rollup time: {}", e);
            return;
        }
    };

    // 2. Calculate next window start
    // If this is the first rollup, start from the truncated earliest time
    // Otherwise, advance by one window from the last rollup
    let mut next_window_start = if is_first_rollup {
        start
    } else {
        start + ChronoDuration::seconds(window_seconds as i64)
    };

    // 3. Safety cutoff: don't process windows that haven't fully passed yet
    // Add buffer for timeout + commit delay
    let cutoff = Utc::now() - ChronoDuration::seconds((target.timeout as i64) + 3);

    let mut results = Vec::new();

    // 4. Process all complete windows
    loop {
        let window_end = next_window_start + ChronoDuration::seconds(window_seconds as i64);
        
        // Stop if this window hasn't finished yet
        if window_end > cutoff {
            break;
        }

        if let Some(agg) = aggregate_window(store, target, window_seconds, source_window, next_window_start, window_end) {
            results.push(agg);
        }

        next_window_start = window_end;
    }

    // 5. Batch save all results
    if !results.is_empty() {
        let count = results.len();
        if let Err(e) = store.add_aggregated_results(&results) {
            tracing::error!(
                "RollupManager: Failed to save batch for {} (w={}s): {}",
                target.name,
                window_seconds,
                e
            );
        } else {
            tracing::debug!(
                "RollupManager: Saved {} rollups for {} (w={}s)",
                count,
                target.name,
                window_seconds
            );
        }
    }
}

fn aggregate_window(
    store: &Store,
    target: &Target,
    window_seconds: i32,
    source_window: i32,
    start: DateTime<Utc>,
    end: DateTime<Utc>,
) -> Option<AggregatedResult> {
    let mut tdigest = TDigest::new_with_size(100);
    let mut timeout_count: i64 = 0;
    let rows_processed: usize;

    if source_window == 0 {
        // Aggregate from raw results
        let raws = match store.get_raw_results(target.id, start, end, i32::MAX) {
            Ok(r) => r,
            Err(e) => {
                tracing::error!("RollupManager: Error fetching raw results: {}", e);
                return None;
            }
        };

        rows_processed = raws.len();
        if raws.is_empty() {
            // Return empty rollup to mark this window as processed
            return Some(create_empty_rollup(target, window_seconds, start));
        }

        let values: Vec<f64> = raws
            .iter()
            .filter_map(|r| {
                if r.latency == -1.0 {
                    timeout_count += 1;
                    None
                } else {
                    Some(r.latency)
                }
            })
            .collect();

        if !values.is_empty() {
            tdigest = TDigest::new_with_size(100).merge_unsorted(values);
        }
    } else {
        // Aggregate from sub-rollups
        let sub_results = match store.get_aggregated_results(target.id, source_window, start, end) {
            Ok(r) => r,
            Err(e) => {
                tracing::error!("RollupManager: Error fetching aggregated results: {}", e);
                return None;
            }
        };

        rows_processed = sub_results.len();
        if sub_results.is_empty() {
            return Some(create_empty_rollup(target, window_seconds, start));
        }

        // Merge all sub-tdigests
        let mut all_values: Vec<f64> = Vec::new();
        for res in &sub_results {
            timeout_count += res.timeout_count;
            if !res.tdigest_data.is_empty() {
                if let Some(sub_td) = deserialize_tdigest(&res.tdigest_data) {
                    let (min, max, _sum, count) = get_tdigest_stats(&sub_td);
                    if count > 0.0 {
                        // Approximate by sampling between min and max
                        let n = (count as usize).min(10);
                        for i in 0..n {
                            let t = i as f64 / (n - 1).max(1) as f64;
                            all_values.push(min + t * (max - min));
                        }
                    }
                }
            }
        }

        if !all_values.is_empty() {
            tdigest = TDigest::new_with_size(100).merge_unsorted(all_values);
        }
    }

    let td_bytes = serialize_tdigest(&tdigest);

    tracing::info!(
        "RollupManager: Aggregated {} (w={}s) at {}: {} rows, {} timeouts",
        target.name,
        window_seconds,
        start.format("%H:%M:%S"),
        rows_processed,
        timeout_count
    );

    Some(AggregatedResult {
        time: start,
        target_id: target.id,
        window_seconds,
        tdigest_data: td_bytes,
        timeout_count,
    })
}

fn create_empty_rollup(target: &Target, window_seconds: i32, start: DateTime<Utc>) -> AggregatedResult {
    let td = TDigest::new_with_size(100);
    let td_bytes = serialize_tdigest(&td);
    
    AggregatedResult {
        time: start,
        target_id: target.id,
        window_seconds,
        tdigest_data: td_bytes,
        timeout_count: 0,
    }
}

/// Truncate a datetime to the start of its containing window.
pub fn truncate_to_window(dt: DateTime<Utc>, window_seconds: i32) -> DateTime<Utc> {
    let ts = dt.timestamp();
    let truncated = ts - (ts % window_seconds as i64);
    DateTime::from_timestamp(truncated, 0).unwrap_or(dt)
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn test_truncate_to_window() {
        // 2024-01-01 12:34:56 truncated to 60s window = 12:34:00
        let dt = Utc.with_ymd_and_hms(2024, 1, 1, 12, 34, 56).unwrap();
        let truncated = truncate_to_window(dt, 60);
        assert_eq!(truncated, Utc.with_ymd_and_hms(2024, 1, 1, 12, 34, 0).unwrap());
        
        // 12:34:56 truncated to 300s (5m) window = 12:30:00
        let truncated = truncate_to_window(dt, 300);
        assert_eq!(truncated, Utc.with_ymd_and_hms(2024, 1, 1, 12, 30, 0).unwrap());
        
        // 12:34:56 truncated to 3600s (1h) window = 12:00:00
        let truncated = truncate_to_window(dt, 3600);
        assert_eq!(truncated, Utc.with_ymd_and_hms(2024, 1, 1, 12, 0, 0).unwrap());
    }

    #[test]
    fn test_validate_retention_policies() {
        // Valid: windows are multiples of each other
        let valid = vec![
            RetentionPolicy { window: 0, retention: 86400 },
            RetentionPolicy { window: 60, retention: 2592000 },
            RetentionPolicy { window: 300, retention: 31536000 },
        ];
        assert!(validate_retention_policies(&valid).is_ok());

        // Invalid: 90s is not a multiple of 60s
        let invalid = vec![
            RetentionPolicy { window: 60, retention: 86400 },
            RetentionPolicy { window: 90, retention: 86400 },
        ];
        assert!(validate_retention_policies(&invalid).is_err());

        // Invalid: negative window
        let negative = vec![
            RetentionPolicy { window: -1, retention: 86400 },
        ];
        assert!(validate_retention_policies(&negative).is_err());
    }

    #[test]
    fn test_default_policies() {
        let policies = default_policies();
        assert!(!policies.is_empty());
        
        // Should include raw (window=0) and at least one aggregation window
        assert!(policies.iter().any(|p| p.window == 0));
        assert!(policies.iter().any(|p| p.window == 60));
    }

    #[test]
    fn test_get_retention_policies() {
        let mut target = Target::default();
        
        // Empty policies returns None
        target.retention_policies = String::new();
        assert!(get_retention_policies(&target).is_none());
        
        target.retention_policies = "[]".to_string();
        assert!(get_retention_policies(&target).is_none());
        
        // Valid JSON returns Some
        target.retention_policies = r#"[{"window":60,"retention":86400}]"#.to_string();
        let policies = get_retention_policies(&target);
        assert!(policies.is_some());
        assert_eq!(policies.unwrap().len(), 1);
    }
}
