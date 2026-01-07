//! HTTP probe implementation.

use std::time::{Duration, Instant};
use super::ProbeError;

/// Run an HTTP probe against the given address.
///
/// Returns latency in nanoseconds.
pub async fn run_http_probe(address: &str, timeout: Duration) -> Result<f64, ProbeError> {
    let url = if address.starts_with("http://") || address.starts_with("https://") {
        address.to_string()
    } else {
        format!("http://{}", address)
    };

    let client = reqwest::Client::builder()
        .timeout(timeout)
        .build()
        .map_err(|e| ProbeError::Network(e.to_string()))?;

    let start = Instant::now();
    
    let response = client
        .get(&url)
        .send()
        .await
        .map_err(|e| {
            if e.is_timeout() {
                ProbeError::Timeout(timeout)
            } else {
                ProbeError::Network(e.to_string())
            }
        })?;

    // Read the full body to measure complete transfer time
    let _body = response
        .bytes()
        .await
        .map_err(|e| ProbeError::Network(e.to_string()))?;

    Ok(start.elapsed().as_nanos() as f64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_http_probe_invalid_url() {
        let result = run_http_probe("http://256.256.256.256", Duration::from_millis(100)).await;
        assert!(result.is_err());
    }
}
