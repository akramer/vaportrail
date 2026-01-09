//! Ping probe implementation with native ICMP (high-precision) and command fallback.
//!
//! Uses blocking sockets in spawn_blocking for sub-millisecond timing precision.

use std::mem::MaybeUninit;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::process::Stdio;
use std::sync::atomic::{AtomicU16, Ordering};
use std::sync::OnceLock;
use std::time::{Duration, Instant};

use regex::Regex;
use socket2::{Domain, Protocol, Socket, Type};
use tokio::process::Command;

use super::ProbeError;

/// ICMP capability state
#[derive(Debug, Clone, Copy, PartialEq)]
enum IcmpCapability {
    /// Native ICMP sockets are available
    Native,
    /// Only command fallback is available
    CommandOnly,
}

static ICMP_CAPABILITY: OnceLock<IcmpCapability> = OnceLock::new();

/// Ping sequence counter for unique identification
static PING_SEQUENCE: AtomicU16 = AtomicU16::new(0);

/// Generate a unique identifier for each ping request.
/// This ensures concurrent pings can be distinguished even to the same destination.
fn generate_ping_id() -> (u16, u16) {
    let identifier: u16 = rand::random();
    let sequence = PING_SEQUENCE.fetch_add(1, Ordering::Relaxed);
    (identifier, sequence)
}

/// Detect ICMP capability by attempting to create a socket.
fn detect_icmp_capability() -> IcmpCapability {
    // Try to create an ICMP socket (RAW first, then DGRAM for unprivileged)
    
    // Try RAW socket first (requires CAP_NET_RAW or root)
    if Socket::new(Domain::IPV4, Type::RAW, Some(Protocol::ICMPV4)).is_ok() {
        tracing::info!("Ping probe: using native ICMP (RAW socket, privileged)");
        return IcmpCapability::Native;
    }
    
    // Try DGRAM (unprivileged on Linux with ping_group_range set, or macOS)
    if Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::ICMPV4)).is_ok() {
        tracing::info!("Ping probe: using native ICMP (DGRAM socket, unprivileged)");
        return IcmpCapability::Native;
    }
    
    tracing::info!("Ping probe: native ICMP unavailable, using command fallback");
    IcmpCapability::CommandOnly
}

/// Run a ping probe against the given address.
///
/// Returns latency in nanoseconds. Uses spawn_blocking for sub-millisecond precision.
pub async fn run_ping_probe(address: &str, timeout: Duration) -> Result<f64, ProbeError> {
    let capability = *ICMP_CAPABILITY.get_or_init(detect_icmp_capability);

    if capability == IcmpCapability::Native {
        // Resolve address before spawn_blocking (DNS is async)
        let ip = resolve_address(address).await?;
        let addr_str = address.to_string();
        
        // Run blocking ICMP in dedicated thread for precise timing
        let result = tokio::task::spawn_blocking(move || {
            run_blocking_ping(ip, timeout)
        })
        .await
        .map_err(|e| ProbeError::Network(format!("spawn_blocking failed: {}", e)))?;
        
        match result {
            Ok(latency) => return Ok(latency),
            Err(e) => {
                // Check if this is a permission error
                let error_str = format!("{:?}", e);
                if error_str.contains("Permission")
                    || error_str.contains("Operation not permitted")
                    || error_str.contains("denied")
                {
                    tracing::warn!(
                        "Native ping failed with permission error for {}, falling back to command: {}",
                        addr_str, error_str
                    );
                    return run_ping_command(&addr_str, timeout).await;
                }
                return Err(e);
            }
        }
    }

    // Fallback to command execution
    run_ping_command(address, timeout).await
}

/// Resolve hostname to IP address.
async fn resolve_address(address: &str) -> Result<IpAddr, ProbeError> {
    // Try direct parse first
    if let Ok(ip) = address.parse::<IpAddr>() {
        return Ok(ip);
    }
    
    // DNS resolution
    let addrs: Vec<_> = tokio::net::lookup_host(format!("{}:0", address))
        .await
        .map_err(|e| ProbeError::Network(format!("DNS resolution failed: {}", e)))?
        .collect();
    
    addrs
        .into_iter()
        .next()
        .map(|sa| sa.ip())
        .ok_or_else(|| ProbeError::Network(format!("No addresses found for {}", address)))
}

/// Run blocking ICMP ping with precise timing.
/// This runs in a dedicated thread via spawn_blocking.
fn run_blocking_ping(ip: IpAddr, timeout: Duration) -> Result<f64, ProbeError> {
    match ip {
        IpAddr::V4(v4) => run_blocking_ping_v4(v4, timeout),
        IpAddr::V6(v6) => run_blocking_ping_v6(v6, timeout),
    }
}

/// ICMP Echo Request for IPv4
fn run_blocking_ping_v4(ip: Ipv4Addr, timeout: Duration) -> Result<f64, ProbeError> {
    // Try RAW first (privileged), then DGRAM (unprivileged)
    let socket = Socket::new(Domain::IPV4, Type::RAW, Some(Protocol::ICMPV4))
        .or_else(|_| Socket::new(Domain::IPV4, Type::DGRAM, Some(Protocol::ICMPV4)))
        .map_err(|e| ProbeError::Network(format!("Failed to create ICMP socket: {}", e)))?;
    
    socket.set_read_timeout(Some(timeout))
        .map_err(|e| ProbeError::Network(format!("Failed to set timeout: {}", e)))?;
    socket.set_write_timeout(Some(timeout))
        .map_err(|e| ProbeError::Network(format!("Failed to set timeout: {}", e)))?;
    
    let dest = SocketAddr::new(IpAddr::V4(ip), 0);
    socket.connect(&dest.into())
        .map_err(|e| ProbeError::Network(format!("Failed to connect: {}", e)))?;
    
    // Build ICMP Echo Request packet with unique identifier
    let (identifier, sequence) = generate_ping_id();
    let packet = build_icmp_echo_request(identifier, sequence);
    
    // Start timing just before send
    let start = Instant::now();
    
    socket.send(&packet)
        .map_err(|e| {
            if e.kind() == std::io::ErrorKind::PermissionDenied {
                ProbeError::Network(format!("Permission denied: {}", e))
            } else {
                ProbeError::Network(format!("Failed to send: {}", e))
            }
        })?;
    
    // Receive reply - loop until we get OUR reply or timeout
    loop {
        let mut buf: [MaybeUninit<u8>; 1500] = unsafe { MaybeUninit::uninit().assume_init() };
        let len = socket.recv(&mut buf).map_err(|e| {
            if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut {
                ProbeError::Timeout(timeout)
            } else {
                ProbeError::Network(format!("Failed to receive: {}", e))
            }
        })?;
        // SAFETY: recv initialized `len` bytes
        let buf: &[u8] = unsafe { std::slice::from_raw_parts(buf.as_ptr() as *const u8, len) };
        
        // Stop timing immediately after receive
        let elapsed = start.elapsed();
        
        // Check if we've exceeded timeout
        if elapsed >= timeout {
            return Err(ProbeError::Timeout(timeout));
        }
        
        // Verify this is our echo reply
        // For DGRAM sockets, we get just the ICMP header (no IP header)
        // For RAW sockets, we'd get IP header + ICMP
        if len >= 8 {
            // ICMP type 0 = Echo Reply
            let icmp_offset = if buf[0] >> 4 == 4 { 20 } else { 0 }; // Skip IP header if present
            if len > icmp_offset + 7 {
                let reply_type = buf[icmp_offset];
                let reply_id = u16::from_be_bytes([buf[icmp_offset + 4], buf[icmp_offset + 5]]);
                let reply_seq = u16::from_be_bytes([buf[icmp_offset + 6], buf[icmp_offset + 7]]);
                
                if reply_type == 0 && reply_id == identifier && reply_seq == sequence {
                    return Ok(elapsed.as_nanos() as f64);
                }
                // Wrong packet - continue waiting for the right one
            }
        }
        // Received something else, keep waiting
    }
}

/// ICMP Echo Request for IPv6
fn run_blocking_ping_v6(ip: Ipv6Addr, timeout: Duration) -> Result<f64, ProbeError> {
    // Try RAW first (privileged), then DGRAM (unprivileged)
    let socket = Socket::new(Domain::IPV6, Type::RAW, Some(Protocol::ICMPV6))
        .or_else(|_| Socket::new(Domain::IPV6, Type::DGRAM, Some(Protocol::ICMPV6)))
        .map_err(|e| ProbeError::Network(format!("Failed to create ICMPv6 socket: {}", e)))?;
    
    socket.set_read_timeout(Some(timeout))
        .map_err(|e| ProbeError::Network(format!("Failed to set timeout: {}", e)))?;
    socket.set_write_timeout(Some(timeout))
        .map_err(|e| ProbeError::Network(format!("Failed to set timeout: {}", e)))?;
    
    let dest = SocketAddr::new(IpAddr::V6(ip), 0);
    socket.connect(&dest.into())
        .map_err(|e| ProbeError::Network(format!("Failed to connect: {}", e)))?;
    
    // Build ICMPv6 Echo Request packet with unique identifier
    let (identifier, sequence) = generate_ping_id();
    let packet = build_icmpv6_echo_request(identifier, sequence);
    
    // Start timing just before send
    let start = Instant::now();
    
    socket.send(&packet)
        .map_err(|e| {
            if e.kind() == std::io::ErrorKind::PermissionDenied {
                ProbeError::Network(format!("Permission denied: {}", e))
            } else {
                ProbeError::Network(format!("Failed to send: {}", e))
            }
        })?;
    
    // Receive reply - loop until we get OUR reply or timeout
    loop {
        let mut buf: [MaybeUninit<u8>; 1500] = unsafe { MaybeUninit::uninit().assume_init() };
        let len = socket.recv(&mut buf).map_err(|e| {
            if e.kind() == std::io::ErrorKind::WouldBlock || e.kind() == std::io::ErrorKind::TimedOut {
                ProbeError::Timeout(timeout)
            } else {
                ProbeError::Network(format!("Failed to receive: {}", e))
            }
        })?;
        // SAFETY: recv initialized `len` bytes
        let buf: &[u8] = unsafe { std::slice::from_raw_parts(buf.as_ptr() as *const u8, len) };
        
        // Stop timing immediately after receive
        let elapsed = start.elapsed();
        
        // Check if we've exceeded timeout
        if elapsed >= timeout {
            return Err(ProbeError::Timeout(timeout));
        }
        
        // Verify this is our echo reply (ICMPv6 type 129 = Echo Reply)
        if len >= 8 {
            let reply_type = buf[0];
            let reply_id = u16::from_be_bytes([buf[4], buf[5]]);
            let reply_seq = u16::from_be_bytes([buf[6], buf[7]]);
            
            if reply_type == 129 && reply_id == identifier && reply_seq == sequence {
                return Ok(elapsed.as_nanos() as f64);
            }
            // Wrong packet - continue waiting for the right one
        }
        // Received something else, keep waiting
    }
}

/// Build an ICMP Echo Request packet (type 8, code 0).
fn build_icmp_echo_request(identifier: u16, sequence: u16) -> Vec<u8> {
    let mut packet = vec![0u8; 64]; // 8 byte header + 56 byte payload
    
    packet[0] = 8;  // Type: Echo Request
    packet[1] = 0;  // Code: 0
    // Checksum at [2..4], computed later
    packet[4..6].copy_from_slice(&identifier.to_be_bytes());
    packet[6..8].copy_from_slice(&sequence.to_be_bytes());
    
    // Fill payload with timestamp
    let timestamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos() as u64;
    packet[8..16].copy_from_slice(&timestamp.to_be_bytes());
    
    // Compute checksum
    let checksum = icmp_checksum(&packet);
    packet[2..4].copy_from_slice(&checksum.to_be_bytes());
    
    packet
}

/// Build an ICMPv6 Echo Request packet (type 128, code 0).
fn build_icmpv6_echo_request(identifier: u16, sequence: u16) -> Vec<u8> {
    let mut packet = vec![0u8; 64]; // 8 byte header + 56 byte payload
    
    packet[0] = 128; // Type: Echo Request
    packet[1] = 0;   // Code: 0
    // Checksum at [2..4] - kernel computes this for ICMPv6
    packet[4..6].copy_from_slice(&identifier.to_be_bytes());
    packet[6..8].copy_from_slice(&sequence.to_be_bytes());
    
    // Fill payload with timestamp
    let timestamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos() as u64;
    packet[8..16].copy_from_slice(&timestamp.to_be_bytes());
    
    // Note: ICMPv6 checksum is typically computed by the kernel
    // when using datagram sockets, so we leave it as 0
    
    packet
}

/// Compute ICMP checksum (RFC 1071).
fn icmp_checksum(data: &[u8]) -> u16 {
    let mut sum: u32 = 0;
    let mut i = 0;
    
    while i < data.len() - 1 {
        sum += u16::from_be_bytes([data[i], data[i + 1]]) as u32;
        i += 2;
    }
    
    // Handle odd byte
    if i < data.len() {
        sum += (data[i] as u32) << 8;
    }
    
    // Fold 32-bit sum to 16 bits
    while sum >> 16 != 0 {
        sum = (sum & 0xFFFF) + (sum >> 16);
    }
    
    !sum as u16
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
        if stderr.contains("timeout")
            || stdout.contains("100% packet loss")
            || stdout.contains("100.0% packet loss")
        {
            return Err(ProbeError::Timeout(timeout));
        }
        return Err(ProbeError::Command(format!("ping failed: {}", stdout)));
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
    fn test_icmp_checksum() {
        // Test with a known packet
        let mut packet = vec![0u8; 8];
        packet[0] = 8; // Echo request
        packet[1] = 0; // Code
        // Checksum will be computed
        packet[4] = 0x12; // ID high
        packet[5] = 0x34; // ID low
        packet[6] = 0x00; // Seq high
        packet[7] = 0x01; // Seq low
        
        let checksum = icmp_checksum(&packet);
        // Verify checksum is non-zero and reasonable
        assert_ne!(checksum, 0);
    }
    
    #[test]
    fn test_build_icmp_packet() {
        let packet = build_icmp_echo_request(0x1234, 0x0001);
        assert_eq!(packet.len(), 64);
        assert_eq!(packet[0], 8); // Type
        assert_eq!(packet[1], 0); // Code
        assert_eq!(packet[4..6], [0x12, 0x34]); // ID
        assert_eq!(packet[6..8], [0x00, 0x01]); // Sequence
    }

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
