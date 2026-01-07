//! Scheduler module for running probes and aggregating data.

mod retention;
mod rollup;

pub use retention::*;
pub use rollup::*;

use crate::db::{RawResult, Store, Target};
use crate::probe::{run_probe, ProbeConfig, ProbeError};

use chrono::Utc;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::{mpsc, RwLock};

/// The main scheduler that orchestrates probe execution.
pub struct Scheduler {
    store: Arc<Store>,
    stop_chans: Arc<RwLock<HashMap<i64, tokio::sync::broadcast::Sender<()>>>>,
    raw_result_tx: mpsc::Sender<RawResult>,
    rollup_manager: Arc<RollupManager>,
    retention_manager: Arc<RetentionManager>,
}

impl Scheduler {
    /// Create a new scheduler with the given store.
    pub fn new(store: Arc<Store>) -> Self {
        let (tx, rx) = mpsc::channel(1000);
        
        let rollup_manager = Arc::new(RollupManager::new(store.clone()));
        let retention_manager = Arc::new(RetentionManager::new(store.clone()));

        // Start batch writer in a separate task
        let store_clone = store.clone();
        tokio::spawn(run_batch_writer(rx, store_clone));

        Self {
            store,
            stop_chans: Arc::new(RwLock::new(HashMap::new())),
            raw_result_tx: tx,
            rollup_manager,
            retention_manager,
        }
    }

    /// Start the scheduler and begin monitoring all targets.
    pub async fn start(&self) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let targets = self.store.get_targets()?;
        
        tracing::info!("Starting scheduler with {} targets", targets.len());
        
        for target in targets {
            self.add_target(target).await;
        }

        // Start rollup and retention managers
        self.rollup_manager.start();
        self.retention_manager.start();

        Ok(())
    }

    /// Add a target to be monitored.
    pub async fn add_target(&self, target: Target) {
        let mut stop_chans = self.stop_chans.write().await;
        
        if stop_chans.contains_key(&target.id) {
            return; // Already running
        }

        let (stop_tx, _) = tokio::sync::broadcast::channel(1);
        stop_chans.insert(target.id, stop_tx.clone());
        drop(stop_chans);

        tracing::info!("Scheduler: Adding target {}", target.name);

        let raw_result_tx = self.raw_result_tx.clone();
        let target_id = target.id;
        let stop_chans = self.stop_chans.clone();

        tokio::spawn(async move {
            run_probe_loop(target, raw_result_tx, stop_tx.subscribe()).await;
            
            // Clean up when done
            let mut chans = stop_chans.write().await;
            chans.remove(&target_id);
        });
    }

    /// Remove a target from monitoring.
    pub async fn remove_target(&self, id: i64) {
        let mut stop_chans = self.stop_chans.write().await;
        
        if let Some(stop_tx) = stop_chans.remove(&id) {
            let _ = stop_tx.send(());
            tracing::info!("Scheduler: Removed target {}", id);
        }
    }
}

/// Run the probe loop for a single target.
async fn run_probe_loop(
    target: Target,
    tx: mpsc::Sender<RawResult>,
    mut stop_rx: tokio::sync::broadcast::Receiver<()>,
) {
    let probe_interval = if target.probe_interval <= 0.0 {
        1.0
    } else {
        target.probe_interval
    };
    
    let timeout = if target.timeout <= 0.0 {
        5.0
    } else {
        target.timeout
    };

    let interval_duration = Duration::from_secs_f64(probe_interval);
    let timeout_duration = Duration::from_secs_f64(timeout);

    let config = ProbeConfig::new(&target.probe_type, &target.address, timeout_duration);

    // Semaphore to limit concurrent probes (max 5)
    let semaphore = Arc::new(tokio::sync::Semaphore::new(5));

    let mut interval = tokio::time::interval(interval_duration);
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

    loop {
        tokio::select! {
            _ = stop_rx.recv() => {
                break;
            }
            _ = interval.tick() => {
                let permit = match semaphore.clone().try_acquire_owned() {
                    Ok(p) => p,
                    Err(_) => {
                        tracing::warn!("Skipping probe for {} due to overlap limit", target.name);
                        continue;
                    }
                };

                let config = config.clone();
                let tx = tx.clone();
                let target_id = target.id;
                let target_name = target.name.clone();

                tokio::spawn(async move {
                    let _permit = permit; // Hold permit until done
                    
                    let start_time = Utc::now();
                    let result = run_probe(&config).await;

                    let raw = match result {
                        Ok(latency) => RawResult {
                            time: start_time,
                            target_id,
                            latency,
                        },
                        Err(ProbeError::Timeout(_)) => RawResult {
                            time: start_time,
                            target_id,
                            latency: -1.0, // Timeout marker
                        },
                        Err(e) => {
                            tracing::error!("Probe failed for {}: {}", target_name, e);
                            return;
                        }
                    };

                    if tx.send(raw).await.is_err() {
                        tracing::error!("Failed to send result for {}", target_name);
                    }
                });
            }
        }
    }
}

/// Run the batch writer that accumulates and flushes raw results.
async fn run_batch_writer(mut rx: mpsc::Receiver<RawResult>, store: Arc<Store>) {
    let mut buffer: Vec<RawResult> = Vec::with_capacity(100);
    let mut interval = tokio::time::interval(Duration::from_secs(2));

    loop {
        tokio::select! {
            result = rx.recv() => {
                match result {
                    Some(r) => {
                        buffer.push(r);
                        if buffer.len() >= 500 {
                            flush_buffer(&store, &mut buffer);
                        }
                    }
                    None => {
                        // Channel closed, flush remaining and exit
                        flush_buffer(&store, &mut buffer);
                        break;
                    }
                }
            }
            _ = interval.tick() => {
                flush_buffer(&store, &mut buffer);
            }
        }
    }
}

fn flush_buffer(store: &Store, buffer: &mut Vec<RawResult>) {
    if buffer.is_empty() {
        return;
    }

    if let Err(e) = store.add_raw_results(buffer) {
        tracing::error!("Failed to flush raw results: {}", e);
    }

    buffer.clear();
}
