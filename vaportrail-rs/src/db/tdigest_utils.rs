//! TDigest serialization utilities.
//!
//! Provides functions to serialize and deserialize TDigest structures
//! for database storage. Uses the go-tdigest binary format for compatibility:
//! https://github.com/caio/go-tdigest
//!
//! Format (big-endian):
//! - 4 bytes: encoding version (int32, always 2 for "small encoding")
//! - 8 bytes: compression (float64)
//! - 4 bytes: number of centroids (int32)
//! - For each centroid: 4 bytes delta-encoded mean (float32)
//! - For each centroid: varint-encoded count (uint64)

use tdigests::{TDigest, Centroid};
use unsigned_varint::{encode as varint_encode, decode as varint_decode};

/// Encoding version constant matching go-tdigest's "smallEncoding"
const SMALL_ENCODING: u32 = 2;

/// Default compression value for TDigest (used when deserializing)
const DEFAULT_COMPRESSION: f64 = 100.0;

/// Serialize a TDigest to bytes for storage.
///
/// Uses the go-tdigest binary format for cross-library compatibility.
/// This format is significantly more compact than naive varint encoding:
/// - Means are delta-encoded as float32 (4 bytes each vs ~9 bytes)
/// - Only counts use varint (typically 1-3 bytes for small values)
pub fn serialize_tdigest(td: &TDigest) -> Vec<u8> {
    serialize_tdigest_with_compression(td, DEFAULT_COMPRESSION)
}

/// Serialize a TDigest with a specific compression value.
pub fn serialize_tdigest_with_compression(td: &TDigest, compression: f64) -> Vec<u8> {
    let centroids = td.centroids();
    let num_centroids = centroids.len();
    
    // Header: 16 bytes + means: 4 bytes each + counts: up to 10 bytes each (varint)
    let mut data = Vec::with_capacity(16 + num_centroids * 4 + num_centroids * 10);
    
    // Write header (big-endian)
    data.extend_from_slice(&(SMALL_ENCODING as u32).to_be_bytes());  // encoding version
    data.extend_from_slice(&compression.to_bits().to_be_bytes());    // compression
    data.extend_from_slice(&(num_centroids as u32).to_be_bytes());   // centroid count
    
    // Write delta-encoded means as float32
    let mut prev_mean: f64 = 0.0;
    for c in centroids {
        let delta = (c.mean - prev_mean) as f32;
        prev_mean = c.mean;
        data.extend_from_slice(&delta.to_bits().to_be_bytes());
    }
    
    // Write counts as varint
    let mut buf = varint_encode::u64_buffer();
    for c in centroids {
        // Weight is stored as f64 but should be a whole number
        let count = c.weight as u64;
        let encoded = varint_encode::u64(count, &mut buf);
        data.extend_from_slice(encoded);
    }
    
    data
}

/// Deserialize a TDigest from stored bytes.
///
/// Supports the go-tdigest binary format.
pub fn deserialize_tdigest(data: &[u8]) -> Option<TDigest> {
    if data.len() < 16 {
        return None;
    }
    
    // Read header (big-endian)
    let encoding = u32::from_be_bytes([data[0], data[1], data[2], data[3]]);
    if encoding != SMALL_ENCODING {
        return None;  // Unsupported encoding version
    }
    
    let _compression = f64::from_bits(u64::from_be_bytes([
        data[4], data[5], data[6], data[7],
        data[8], data[9], data[10], data[11],
    ]));
    
    let num_centroids = u32::from_be_bytes([data[12], data[13], data[14], data[15]]) as usize;
    
    if num_centroids == 0 {
        return None;
    }
    
    // Validate we have enough data for means
    let means_end = 16 + num_centroids * 4;
    if data.len() < means_end {
        return None;
    }
    
    // Read delta-encoded means
    let mut means = Vec::with_capacity(num_centroids);
    let mut cumulative_mean: f64 = 0.0;
    for i in 0..num_centroids {
        let offset = 16 + i * 4;
        let delta_bits = u32::from_be_bytes([
            data[offset], data[offset + 1], data[offset + 2], data[offset + 3],
        ]);
        let delta = f32::from_bits(delta_bits) as f64;
        cumulative_mean += delta;
        means.push(cumulative_mean);
    }
    
    // Read varint counts
    let mut remaining = &data[means_end..];
    let mut centroids = Vec::with_capacity(num_centroids);
    
    for i in 0..num_centroids {
        let (count, rest) = varint_decode::u64(remaining).ok()?;
        remaining = rest;
        centroids.push(Centroid::new(means[i], count as f64));
    }
    
    Some(TDigest::from_centroids(centroids))
}

/// Get TDigest statistics: (min, max, sum, count)
/// Computed from centroids since tdigests crate doesn't expose these directly.
pub fn get_tdigest_stats(td: &TDigest) -> (f64, f64, f64, f64) {
    let centroids = td.centroids();
    if centroids.is_empty() {
        return (0.0, 0.0, 0.0, 0.0);
    }
    
    let mut min = f64::MAX;
    let mut max = f64::MIN;
    let mut sum = 0.0;
    let mut count = 0.0;
    
    for c in centroids {
        if c.mean < min {
            min = c.mean;
        }
        if c.mean > max {
            max = c.mean;
        }
        sum += c.mean * c.weight;
        count += c.weight;
    }
    
    (min, max, sum, count)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_roundtrip() {
        let values = vec![1.0, 2.0, 3.0, 4.0, 5.0];
        let mut td = TDigest::from_values(values);
        td.compress(100);
        
        let data = serialize_tdigest(&td);
        let td2 = deserialize_tdigest(&data).unwrap();
        
        // Check that we can still get estimates
        assert!((td.estimate_quantile(0.5) - td2.estimate_quantile(0.5)).abs() < 0.01);
    }

    #[test]
    fn test_empty_data() {
        let result = deserialize_tdigest(&[]);
        assert!(result.is_none());
    }
    
    #[test]
    fn test_stats() {
        let values = vec![1.0, 2.0, 3.0, 4.0, 5.0];
        let td = TDigest::from_values(values);
        let (min, max, _sum, count) = get_tdigest_stats(&td);
        
        assert!((min - 1.0).abs() < 0.01);
        assert!((max - 5.0).abs() < 0.01);
        assert!((count - 5.0).abs() < 0.01);
    }
}
