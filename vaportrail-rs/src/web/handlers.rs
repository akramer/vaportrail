//! HTTP request handlers.

use super::AppState;
use crate::db::{deserialize_tdigest, get_tdigest_stats, Target, RawStats};
use crate::scheduler::{default_policies_json, get_retention_policies, validate_retention_policies, RetentionPolicy};

use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::{Html, IntoResponse, Json},
};
use chrono::{DateTime, Duration as ChronoDuration, Utc};
use serde::{Deserialize, Serialize};

// ============================================================================
// Templates (using simple string replacement instead of askama for simplicity)
// ============================================================================

const DASHBOARD_TEMPLATE: &str = include_str!("templates/dashboard.html");
const GRAPH_TEMPLATE: &str = include_str!("templates/graph.html");
const STATUS_TEMPLATE: &str = include_str!("templates/status.html");
const LAYOUT_TEMPLATE: &str = include_str!("templates/layout.html");

// ============================================================================
// Dashboard
// ============================================================================

pub async fn handle_dashboard(State(state): State<AppState>) -> impl IntoResponse {
    let targets = state.store.get_targets().unwrap_or_default();
    let targets_json = serde_json::to_string(&targets).unwrap_or_else(|_| "[]".to_string());
    let default_policies = default_policies_json();

    let content = DASHBOARD_TEMPLATE
        .replace("{{targets_json}}", &targets_json)
        .replace("{{default_policies}}", &default_policies);

    let page = LAYOUT_TEMPLATE
        .replace("{{title}}", "VaporTrail Dashboard")
        .replace("{{content}}", &content);

    Html(page)
}

// ============================================================================
// API: Targets
// ============================================================================

pub async fn handle_get_targets(State(state): State<AppState>) -> impl IntoResponse {
    match state.store.get_targets() {
        Ok(targets) => Json(targets).into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

#[derive(Debug, Deserialize)]
pub struct CreateTargetRequest {
    pub name: String,
    pub address: String,
    pub probe_type: String,
    #[serde(default)]
    pub probe_interval: f64,
    #[serde(default)]
    pub timeout: f64,
    #[serde(default)]
    pub retention_policies: Option<Vec<RetentionPolicy>>,
}

pub async fn handle_create_target(
    State(state): State<AppState>,
    Json(req): Json<CreateTargetRequest>,
) -> impl IntoResponse {
    // Validate probe type
    if !["ping", "http", "dns"].contains(&req.probe_type.as_str()) {
        return (StatusCode::BAD_REQUEST, "Invalid probe type").into_response();
    }

    // Handle retention policies
    let retention_json = if let Some(policies) = &req.retention_policies {
        if let Err(e) = validate_retention_policies(policies) {
            return (StatusCode::BAD_REQUEST, e).into_response();
        }
        serde_json::to_string(policies).unwrap_or_else(|_| default_policies_json())
    } else {
        default_policies_json()
    };

    let mut target = Target {
        id: 0,
        name: req.name,
        address: req.address,
        probe_type: req.probe_type,
        probe_config: String::new(),
        probe_interval: if req.probe_interval <= 0.0 { 1.0 } else { req.probe_interval },
        timeout: if req.timeout <= 0.0 { 5.0 } else { req.timeout },
        retention_policies: retention_json,
    };

    match state.store.add_target(&mut target) {
        Ok(_) => {
            // Add to scheduler
            state.scheduler.add_target(target.clone()).await;
            Json(target).into_response()
        }
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

pub async fn handle_update_target(
    State(state): State<AppState>,
    Path(id): Path<i64>,
    Json(req): Json<CreateTargetRequest>,
) -> impl IntoResponse {
    // Validate probe type
    if !["ping", "http", "dns"].contains(&req.probe_type.as_str()) {
        return (StatusCode::BAD_REQUEST, "Invalid probe type").into_response();
    }

    // Get existing target
    let existing = match state.store.get_target(id) {
        Ok(t) => t,
        Err(_) => return (StatusCode::NOT_FOUND, "Target not found").into_response(),
    };

    // Handle retention policies
    let retention_json = if let Some(policies) = &req.retention_policies {
        if let Err(e) = validate_retention_policies(policies) {
            return (StatusCode::BAD_REQUEST, e).into_response();
        }
        serde_json::to_string(policies).unwrap_or_else(|_| existing.retention_policies.clone())
    } else {
        existing.retention_policies.clone()
    };

    let updated = Target {
        id,
        name: req.name,
        address: req.address,
        probe_type: req.probe_type,
        probe_config: existing.probe_config,
        probe_interval: if req.probe_interval <= 0.0 { 1.0 } else { req.probe_interval },
        timeout: if req.timeout <= 0.0 { 5.0 } else { req.timeout },
        retention_policies: retention_json,
    };

    // Remove and re-add to scheduler
    state.scheduler.remove_target(id).await;

    match state.store.update_target(&updated) {
        Ok(_) => {
            state.scheduler.add_target(updated.clone()).await;
            Json(updated).into_response()
        }
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

pub async fn handle_delete_target(
    State(state): State<AppState>,
    Path(id): Path<i64>,
) -> impl IntoResponse {
    state.scheduler.remove_target(id).await;

    match state.store.delete_target(id) {
        Ok(_) => StatusCode::NO_CONTENT.into_response(),
        Err(e) => (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
    }
}

// ============================================================================
// API: Results
// ============================================================================

#[derive(Debug, Deserialize)]
pub struct ResultsQuery {
    pub target_id: i64,
    #[serde(default)]
    pub start: Option<String>,
    #[serde(default)]
    pub end: Option<String>,
    #[serde(default)]
    pub include_raw: Option<bool>,
}

#[derive(Debug, Serialize)]
pub struct ApiResult {
    pub time: DateTime<Utc>,
    pub target_id: i64,
    pub min_ns: i64,
    pub max_ns: i64,
    pub avg_ns: i64,
    pub p0: f64,
    pub p1: f64,
    pub p25: f64,
    pub p50: f64,
    pub p75: f64,
    pub p99: f64,
    pub p100: f64,
    pub percentiles: Vec<f64>,
    pub timeout_count: i64,
    pub probe_count: i64,
    pub window_seconds: i32,
}

#[derive(Debug, Serialize)]
pub struct ApiRawResult {
    pub time: DateTime<Utc>,
    pub latency: f64,
}

#[derive(Debug, Serialize)]
pub struct ResultsResponse {
    pub results: Vec<ApiResult>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub raw: Option<Vec<ApiRawResult>>,
}

pub async fn handle_get_results(
    State(state): State<AppState>,
    Query(query): Query<ResultsQuery>,
) -> impl IntoResponse {
    // Parse time range
    let end = query
        .end
        .as_ref()
        .and_then(|s| DateTime::parse_from_rfc3339(s).ok())
        .map(|dt| dt.with_timezone(&Utc))
        .unwrap_or_else(Utc::now);

    let start = query
        .start
        .as_ref()
        .and_then(|s| DateTime::parse_from_rfc3339(s).ok())
        .map(|dt| dt.with_timezone(&Utc))
        .unwrap_or_else(|| end - ChronoDuration::hours(1));

    let duration = end - start;
    let duration_secs = duration.num_seconds();

    // Get target to check retention policies
    let target = match state.store.get_target(query.target_id) {
        Ok(t) => t,
        Err(_) => return (StatusCode::NOT_FOUND, "Target not found").into_response(),
    };

    // Determine best window size
    let policies = get_retention_policies(&target).unwrap_or_default();
    let window_seconds = select_window(&policies, duration_secs);

    // Fetch aggregated results
    let agg_results = if window_seconds > 0 {
        match state.store.get_aggregated_results(query.target_id, window_seconds, start, end) {
            Ok(r) => r,
            Err(e) => return (StatusCode::INTERNAL_SERVER_ERROR, e.to_string()).into_response(),
        }
    } else {
        vec![]
    };

    // Convert to API format
    let results: Vec<ApiResult> = agg_results
        .into_iter()
        .map(|r| {
            let td = deserialize_tdigest(&r.tdigest_data);
            let (p0, p1, p25, p50, p75, p99, p100, percentiles, min, max, avg, count) =
                if let Some(ref td) = td {
                    let (td_min, td_max, td_sum, td_count) = get_tdigest_stats(td);
                    
                    let avg_val = if td_count > 0.0 { td_sum / td_count } else { 0.0 };
                    
                    let percentiles: Vec<f64> = (0..=100)
                        .map(|i| sanitize_float(td.estimate_quantile(i as f64 / 100.0)))
                        .collect();
                    
                    (
                        sanitize_float(td.estimate_quantile(0.0)),
                        sanitize_float(td.estimate_quantile(0.01)),
                        sanitize_float(td.estimate_quantile(0.25)),
                        sanitize_float(td.estimate_quantile(0.50)),
                        sanitize_float(td.estimate_quantile(0.75)),
                        sanitize_float(td.estimate_quantile(0.99)),
                        sanitize_float(td.estimate_quantile(1.0)),
                        percentiles,
                        sanitize_float(td_min) as i64,
                        sanitize_float(td_max) as i64,
                        sanitize_float(avg_val) as i64,
                        td_count as i64,
                    )
                } else {
                    (0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, vec![], 0, 0, 0, 0)
                };

            ApiResult {
                time: r.time,
                target_id: r.target_id,
                min_ns: min,
                max_ns: max,
                avg_ns: avg,
                p0,
                p1,
                p25,
                p50,
                p75,
                p99,
                p100,
                percentiles,
                timeout_count: r.timeout_count,
                probe_count: count,
                window_seconds: r.window_seconds,
            }
        })
        .collect();

    // Optionally include raw results
    let raw = if query.include_raw.unwrap_or(false) {
        match state.store.get_raw_results(query.target_id, start, end, 1000) {
            Ok(raws) => Some(
                raws.into_iter()
                    .map(|r| ApiRawResult {
                        time: r.time,
                        latency: r.latency,
                    })
                    .collect(),
            ),
            Err(_) => None,
        }
    } else {
        None
    };

    Json(ResultsResponse { results, raw }).into_response()
}

fn select_window(policies: &[RetentionPolicy], duration_secs: i64) -> i32 {
    // Target ~200 data points
    let target_window = (duration_secs / 200) as i32;
    
    let mut sorted: Vec<_> = policies.iter().filter(|p| p.window > 0).collect();
    sorted.sort_by_key(|p| p.window);
    
    for p in sorted.iter().rev() {
        if p.window <= target_window {
            return p.window;
        }
    }
    
    // Default to smallest non-zero window
    sorted.first().map(|p| p.window).unwrap_or(60)
}

fn sanitize_float(f: f64) -> f64 {
    if f.is_nan() || f.is_infinite() {
        0.0
    } else {
        f
    }
}

// ============================================================================
// Pages
// ============================================================================

#[derive(Debug, Deserialize)]
pub struct GraphQuery {
    pub id: i64,
    pub start: Option<String>,
    pub end: Option<String>,
}

pub async fn handle_graph(
    State(state): State<AppState>,
    Query(query): Query<GraphQuery>,
) -> impl IntoResponse {
    let target = match state.store.get_target(query.id) {
        Ok(t) => t,
        Err(_) => return Html("<h1>Target not found</h1>".to_string()),
    };

    let content = GRAPH_TEMPLATE
        .replace("{{target_id}}", &target.id.to_string())
        .replace("{{target_name}}", &target.name)
        .replace("{{start}}", &query.start.unwrap_or_default())
        .replace("{{end}}", &query.end.unwrap_or_default());

    let page = LAYOUT_TEMPLATE
        .replace("{{title}}", &format!("Graph - {}", target.name))
        .replace("{{content}}", &content);

    Html(page)
}

pub async fn handle_status(State(state): State<AppState>) -> impl IntoResponse {
    let db_size = state.store.get_db_size_bytes().unwrap_or(0);
    let page_count = state.store.get_page_count().unwrap_or(0);
    let page_size = state.store.get_page_size().unwrap_or(0);
    let freelist_count = state.store.get_freelist_count().unwrap_or(0);
    let tdigest_stats = state.store.get_tdigest_stats().unwrap_or_default();
    let raw_stats = state.store.get_raw_stats().unwrap_or(RawStats { count: 0, total_bytes: 0 });

    let db_size_str = format_bytes(db_size);
    let raw_size_str = format_bytes(raw_stats.total_bytes);

    let tdigest_rows: String = tdigest_stats
        .iter()
        .map(|s| {
            format!(
                "<tr><td>{}</td><td>{}s</td><td>{}</td><td>{}</td><td>{:.1}</td></tr>",
                s.target_name, s.window_seconds, format_bytes(s.total_bytes), s.count, s.avg_bytes
            )
        })
        .collect::<Vec<_>>()
        .join("\n");

    let content = STATUS_TEMPLATE
        .replace("{{db_size}}", &db_size_str)
        .replace("{{page_count}}", &page_count.to_string())
        .replace("{{page_size}}", &page_size.to_string())
        .replace("{{freelist_count}}", &freelist_count.to_string())
        .replace("{{tdigest_rows}}", &tdigest_rows)
        .replace("{{raw_count}}", &raw_stats.count.to_string())
        .replace("{{raw_size}}", &raw_size_str);

    let page = LAYOUT_TEMPLATE
        .replace("{{title}}", "VaporTrail Status")
        .replace("{{content}}", &content);

    Html(page)
}

fn format_bytes(bytes: i64) -> String {
    const KB: i64 = 1024;
    const MB: i64 = KB * 1024;
    const GB: i64 = MB * 1024;

    if bytes >= GB {
        format!("{:.2} GB", bytes as f64 / GB as f64)
    } else if bytes >= MB {
        format!("{:.2} MB", bytes as f64 / MB as f64)
    } else if bytes >= KB {
        format!("{:.2} KB", bytes as f64 / KB as f64)
    } else {
        format!("{} B", bytes)
    }
}

// ============================================================================
// Static Assets
// ============================================================================

pub async fn handle_favicon() -> impl IntoResponse {
    // Return a simple SVG favicon
    let svg = r##"<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
        <circle cx="50" cy="50" r="45" fill="#4a90d9"/>
        <path d="M25 50 L40 35 L55 50 L70 30 L85 45" stroke="white" stroke-width="4" fill="none"/>
    </svg>"##;

    (
        [(axum::http::header::CONTENT_TYPE, "image/svg+xml")],
        svg
    )
}
