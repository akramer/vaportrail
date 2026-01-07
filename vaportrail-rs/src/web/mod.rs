//! Web server module.

mod handlers;

pub use handlers::*;

use crate::config::ServerConfig;
use crate::db::Store;
use crate::scheduler::Scheduler;

use axum::{
    extract::DefaultBodyLimit,
    routing::{delete, get, post, put},
    Router,
};
use std::net::SocketAddr;
use std::sync::Arc;
use tower_http::cors::{Any, CorsLayer};

/// Application state shared across handlers.
#[derive(Clone)]
pub struct AppState {
    pub config: ServerConfig,
    pub store: Arc<Store>,
    pub scheduler: Arc<Scheduler>,
}

/// Web server for VaporTrail.
pub struct Server {
    state: AppState,
}

impl Server {
    /// Create a new server with the given dependencies.
    pub fn new(config: ServerConfig, store: Arc<Store>, scheduler: Arc<Scheduler>) -> Self {
        Self {
            state: AppState {
                config,
                store,
                scheduler,
            },
        }
    }

    /// Build the router with all routes.
    fn routes(&self) -> Router {
        let cors = CorsLayer::new().allow_origin(Any).allow_methods(Any);

        Router::new()
            // Dashboard
            .route("/", get(handlers::handle_dashboard))
            // API endpoints
            .route("/api/targets", get(handlers::handle_get_targets))
            .route("/api/targets", post(handlers::handle_create_target))
            .route("/api/targets/{id}", put(handlers::handle_update_target))
            .route("/api/targets/{id}", delete(handlers::handle_delete_target))
            .route("/api/results", get(handlers::handle_get_results))
            // Pages
            .route("/graph", get(handlers::handle_graph))
            .route("/status", get(handlers::handle_status))
            // Static assets
            .route("/favicon.ico", get(handlers::handle_favicon))
            .layer(cors)
            .layer(DefaultBodyLimit::max(1024 * 1024)) // 1MB
            .with_state(self.state.clone())
    }

    /// Start the server on the configured port.
    pub async fn start(&self) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let addr = SocketAddr::from(([0, 0, 0, 0], self.state.config.http_port));
        let router = self.routes();

        tracing::info!("Web server listening on {}", addr);
        
        let listener = tokio::net::TcpListener::bind(addr).await?;
        axum::serve(listener, router).await?;

        Ok(())
    }
}
