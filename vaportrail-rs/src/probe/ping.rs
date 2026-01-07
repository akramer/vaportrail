//! Ping probe implementation with command fallback.

use std::process::Stdio;
use std::sync::OnceLock;
use std::time::Duration;

use regex::Regex;
use tokio::process::Command;

use super::ProbeError;

/// ICMP capability: None, Privileged, or Unprivileged
#[derive(Debug, Clone, Copy, PartialEq)]
enum IcmpCapability {
    None,
    // Privileged,
    // Unprivileged,
}

static ICMP_CAPABILITY: OnceLock<IcmpCapability> = OnceLock::new();

/// Detect ICMP capability at startup.
fn detect_icmp_capability() -> IcmpCapability {
    // For simplicity, always fall back to command execution
    // Native ICMP requires platform-specific code and raw sockets
    tracing::info!("Ping probe: using command fallback (ping -c 1)");
    IcmpCapability::None
}

/// Run a ping probe against the given address.
///
/// Returns latency in nanoseconds.
pub async fn run_ping_probe(address: &str, timeout: Duration) -> Result<f64, ProbeError> {
    let capability = *ICMP_CAPABILITY.get_or_init(detect_icmp_capability);

    if capability != IcmpCapability::None {
        // Native ICMP path (currently disabled)
    }

    // Fall back to command execution
    run_ping_command(address, timeout).await
}

/// Run ping via command execution (fallback).
async fn run_ping_command(address: &str, timeout: Duration) -> Result<f64, ProbeError> {
    let timeout_secs = timeout.as_secs().max(1);

    let output = Command::new("ping")
        .args(["-c", "1", "-W", &timeout_secs.to_string(), address])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .await
        .map_err(|e| ProbeError::Command(format!("failed to execute ping: {}", e)))?;

    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        let stdout = String::from_utf8_lossy(&output.stdout);
        if stderr.contains("timeout") || stdout.contains("100% packet loss") || stdout.contains("100.0% packet loss") {
            return Err(ProbeError::Timeout(timeout));
        }
        return Err(ProbeError::Command(format!(
            "ping failed: {}",
            stdout
        )));
    }

    // Parse output for time
    let stdout = String::from_utf8_lossy(&output.stdout);
    parse_ping_output(&stdout)
}

/// Parse ping command output for latency.
fn parse_ping_output(output: &str) -> Result<f64, ProbeError> {
    // Try multiple patterns for different ping formats
    
    // Pattern 1: Per-packet response "time=X.XXX ms" (Linux, some macOS)
    static RE1: OnceLock<Regex> = OnceLock::new();
    let re1 = RE1.get_or_init(|| Regex::new(r"time[=<](?P<val>[0-9.]+)\s*ms").unwrap());

    if let Some(caps) = re1.captures(output) {
        if let Some(val_match) = caps.name("val") {
            if let Ok(ms) = val_match.as_str().parse::<f64>() {
                return Ok(ms * 1_000_000.0);
            }
        }
    }

    // Pattern 2: Summary line "round-trip min/avg/max/stddev = X/X/X/X ms" (macOS)
    static RE2: OnceLock<Regex> = OnceLock::new();
    let re2 = RE2.get_or_init(|| {
        Regex::new(r"round-trip\s+min/avg/max/stddev\s*=\s*([0-9.]+)/([0-9.]+)/([0-9.]+)").unwrap()
    });

    if let Some(caps) = re2.captures(output) {
        // Use average (second capture group)
        if let Some(avg_match) = caps.get(2) {
            if let Ok(ms) = avg_match.as_str().parse::<f64>() {
                return Ok(ms * 1_000_000.0);
            }
        }
    }

    // Pattern 3: Summary line "rtt min/avg/max/mdev = X/X/X/X ms" (Linux)
    static RE3: OnceLock<Regex> = OnceLock::new();
    let re3 = RE3.get_or_init(|| {
        Regex::new(r"rtt\s+min/avg/max/mdev\s*=\s*([0-9.]+)/([0-9.]+)/([0-9.]+)").unwrap()
    });

    if let Some(caps) = re3.captures(output) {
        if let Some(avg_match) = caps.get(2) {
            if let Ok(ms) = avg_match.as_str().parse::<f64>() {
                return Ok(ms * 1_000_000.0);
            }
        }
    }

    Err(ProbeError::Command(format!(
        "failed to parse ping output: {}",
        output
    )))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_ping_output_linux() {
        let output = "64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=12.345 ms";
        let latency = parse_ping_output(output).unwrap();
        assert!((latency - 12_345_000.0).abs() < 1.0);
    }
    
    #[test]
    fn test_parse_ping_output_macos_summary() {
        let output = r#"PING google.com (142.250.69.174): 56 data bytes

--- google.com ping statistics ---
1 packets transmitted, 1 packets received, 0.0% packet loss
round-trip min/avg/max/stddev = 17.906/17.906/17.906/0.000 ms"#;
        let latency = parse_ping_output(output).unwrap();
        assert!((latency - 17_906_000.0).abs() < 1.0);
    }

    #[test]
    fn test_parse_ping_output_linux_summary() {
        let output = r#"PING 8.8.8.8 (8.8.8.8) 56(84) bytes of data.
64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=12.3 ms

--- 8.8.8.8 ping statistics ---
1 packets transmitted, 1 received, 0% packet loss, time 0ms
rtt min/avg/max/mdev = 12.300/12.300/12.300/0.000 ms"#;
        let latency = parse_ping_output(output).unwrap();
        // Should match the per-packet time first
        assert!((latency - 12_300_000.0).abs() < 1.0);
    }
}
