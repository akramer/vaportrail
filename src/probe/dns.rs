//! DNS probe implementation using raw UDP packets.

use std::net::UdpSocket;
use std::time::{Duration, Instant};
use super::ProbeError;

/// Run a DNS probe against the given DNS server address.
///
/// Queries for "example.com" A record and returns latency in nanoseconds.
pub async fn run_dns_probe(address: &str, timeout: Duration) -> Result<f64, ProbeError> {
    // Ensure address has port
    let target_addr = if address.contains(':') {
        address.to_string()
    } else {
        format!("{}:53", address)
    };

    // Build DNS query packet
    let packet = build_dns_query();
    let tx_id = u16::from_be_bytes([packet[0], packet[1]]);

    // Create UDP socket
    let socket = UdpSocket::bind("0.0.0.0:0")
        .map_err(|e| ProbeError::Network(format!("failed to bind socket: {}", e)))?;
    
    socket
        .set_read_timeout(Some(timeout))
        .map_err(|e| ProbeError::Network(format!("failed to set timeout: {}", e)))?;
    
    socket
        .connect(&target_addr)
        .map_err(|e| ProbeError::Network(format!("failed to connect: {}", e)))?;

    let start = Instant::now();

    // Send query
    socket
        .send(&packet)
        .map_err(|e| ProbeError::Network(format!("failed to send: {}", e)))?;

    // Read response
    let mut response = [0u8; 512];
    let n = socket.recv(&mut response).map_err(|e| {
        if e.kind() == std::io::ErrorKind::TimedOut || e.kind() == std::io::ErrorKind::WouldBlock {
            ProbeError::Timeout(timeout)
        } else {
            ProbeError::Network(format!("failed to recv: {}", e))
        }
    })?;

    let elapsed = start.elapsed().as_nanos() as f64;

    // Validate response
    if n < 12 {
        return Err(ProbeError::Network(format!("response too short: {} bytes", n)));
    }

    let resp_tx_id = u16::from_be_bytes([response[0], response[1]]);
    if resp_tx_id != tx_id {
        return Err(ProbeError::Network(format!(
            "transaction ID mismatch: got {}, expected {}",
            resp_tx_id, tx_id
        )));
    }

    // Check RCODE (lower 4 bits of byte 3)
    let rcode = response[3] & 0x0F;
    if rcode != 0 {
        return Err(ProbeError::Network(format!("DNS error RCODE: {}", rcode)));
    }

    Ok(elapsed)
}

/// Build a minimal DNS query packet for "example.com" A record.
fn build_dns_query() -> Vec<u8> {
    let tx_id: u16 = rand::random();
    let flags: u16 = 0x0100; // Standard query, recursion desired
    let qd_count: u16 = 1;
    let an_count: u16 = 0;
    let ns_count: u16 = 0;
    let ar_count: u16 = 0;

    // Header (12 bytes)
    let mut packet = Vec::with_capacity(64);
    packet.extend_from_slice(&tx_id.to_be_bytes());
    packet.extend_from_slice(&flags.to_be_bytes());
    packet.extend_from_slice(&qd_count.to_be_bytes());
    packet.extend_from_slice(&an_count.to_be_bytes());
    packet.extend_from_slice(&ns_count.to_be_bytes());
    packet.extend_from_slice(&ar_count.to_be_bytes());

    // Question: example.com A IN
    // Domain name encoding: length-prefixed labels
    packet.extend_from_slice(&[7, b'e', b'x', b'a', b'm', b'p', b'l', b'e']);
    packet.extend_from_slice(&[3, b'c', b'o', b'm']);
    packet.push(0); // Null terminator

    // QTYPE: A record (1)
    packet.extend_from_slice(&1u16.to_be_bytes());
    // QCLASS: IN (1)
    packet.extend_from_slice(&1u16.to_be_bytes());

    packet
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_build_dns_query() {
        let packet = build_dns_query();
        // Should be at least: 12 (header) + 13 (question name) + 4 (type/class)
        assert!(packet.len() >= 29);
    }
}
