//! VaporTrail - Network Monitoring Application
//!
//! A Rust port of the Go-based VaporTrail monitoring system.

mod config;
mod db;
mod probe;
mod scheduler;
mod web;

use config::ServerConfig;
use db::Store;
use scheduler::Scheduler;
use web::Server;

use std::sync::Arc;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    // Initialize logging
    tracing_subscriber::registry()
        .with(tracing_subscriber::fmt::layer())
        .with(tracing_subscriber::EnvFilter::from_default_env()
            .add_directive("vaportrail=info".parse()?))
        .init();

    // Load configuration
    let cfg = ServerConfig::load();
    tracing::info!("Starting VaporTrail on port {}...", cfg.http_port);
    tracing::info!("Using database at {}", cfg.db_path);

    // Initialize database
    let store = Arc::new(Store::new(&cfg.db_path)?);
    tracing::info!("Database initialized successfully");

    // Create scheduler
    let scheduler = Arc::new(Scheduler::new(store.clone()));

    // Add sample target if none exist
    let targets = store.get_targets()?;
    if targets.is_empty() {
        tracing::info!("Adding sample target: Google");
        let mut target = db::Target {
            name: "Google".to_string(),
            address: "google.com".to_string(),
            probe_type: "ping".to_string(),
            retention_policies: scheduler::default_policies_json(),
            ..Default::default()
        };
        store.add_target(&mut target)?;
    }

    // Start scheduler
    scheduler.start().await?;

    // Start web server
    let server = Server::new(cfg, store, scheduler);
    server.start().await?;

    Ok(())
}
