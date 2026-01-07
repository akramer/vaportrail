//! Retention manager for cleaning up old data.

use crate::db::Store;

use super::rollup::get_retention_policies;
use chrono::{Duration as ChronoDuration, Utc};
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::Mutex;

/// Manager for deleting data past retention periods.
pub struct RetentionManager {
    store: Arc<Store>,
    stop: Arc<Mutex<Option<tokio::sync::broadcast::Sender<()>>>>,
}

impl RetentionManager {
    pub fn new(store: Arc<Store>) -> Self {
        Self {
            store,
            stop: Arc::new(Mutex::new(None)),
        }
    }

    /// Start the retention manager background task.
    pub fn start(&self) {
        let store = self.store.clone();
        let stop = self.stop.clone();

        tokio::spawn(async move {
            let (tx, _) = tokio::sync::broadcast::channel(1);
            {
                let mut stop_guard = stop.lock().await;
                *stop_guard = Some(tx.clone());
            }

            let mut rx = tx.subscribe();
            let mut interval = tokio::time::interval(Duration::from_secs(60));

            loop {
                tokio::select! {
                    _ = rx.recv() => break,
                    _ = interval.tick() => {
                        process_retention(&store);
                    }
                }
            }
        });
    }

    /// Stop the retention manager.
    pub async fn stop(&self) {
        let stop = self.stop.lock().await;
        if let Some(tx) = stop.as_ref() {
            let _ = tx.send(());
        }
    }
}

fn process_retention(store: &Store) {
    let targets = match store.get_targets() {
        Ok(t) => t,
        Err(e) => {
            tracing::error!("RetentionManager: Failed to get targets: {}", e);
            return;
        }
    };

    let now = Utc::now();

    for target in targets {
        let policies = match get_retention_policies(&target) {
            Some(p) => p,
            None => continue,
        };

        for policy in policies {
            let cutoff = now - ChronoDuration::seconds(policy.retention as i64);

            if policy.window == 0 {
                // Delete raw results
                if let Err(e) = store.delete_raw_results_before(target.id, cutoff) {
                    tracing::error!(
                        "RetentionManager: Failed to delete raw results for {}: {}",
                        target.name,
                        e
                    );
                }
            } else {
                // Delete aggregated results for this window
                if let Err(e) = store.delete_aggregated_results_before(target.id, policy.window, cutoff) {
                    tracing::error!(
                        "RetentionManager: Failed to delete aggregated results for {} (w={}): {}",
                        target.name,
                        policy.window,
                        e
                    );
                }
            }
        }
    }
}
