//! TDigest serialization utilities.
//!
//! Provides functions to serialize and deserialize TDigest structures
//! for database storage using varint encoding for compact representation.

use tdigests::{TDigest, Centroid};
use unsigned_varint::{encode as varint_encode, decode as varint_decode};

/// Serialize a TDigest to bytes for storage.
///
/// Format: [centroid_count: varint] [mean_bits: varint, weight_bits: varint]...
/// Each f64 is stored as its bit representation encoded as a varint u64.
pub fn serialize_tdigest(td: &TDigest) -> Vec<u8> {
    let centroids = td.centroids();
    let mut data = Vec::with_capacity(centroids.len() * 16 + 4);
    
    // Write centroid count
    let mut buf = varint_encode::u64_buffer();
    let encoded = varint_encode::u64(centroids.len() as u64, &mut buf);
    data.extend_from_slice(encoded);
    
    // Write each centroid's mean and weight as varint-encoded u64 bits
    for c in centroids {
        let mean_bits = c.mean.to_bits();
        let encoded = varint_encode::u64(mean_bits, &mut buf);
        data.extend_from_slice(encoded);
        
        let weight_bits = c.weight.to_bits();
        let encoded = varint_encode::u64(weight_bits, &mut buf);
        data.extend_from_slice(encoded);
    }
    
    data
}

/// Deserialize a TDigest from stored bytes.
pub fn deserialize_tdigest(data: &[u8]) -> Option<TDigest> {
    if data.is_empty() {
        return None;
    }
    
    let mut remaining = data;
    
    // Read centroid count
    let (count, rest) = varint_decode::u64(remaining).ok()?;
    remaining = rest;
    
    if count == 0 {
        return None;
    }
    
    let mut centroids = Vec::with_capacity(count as usize);
    
    for _ in 0..count {
        // Read mean
        let (mean_bits, rest) = varint_decode::u64(remaining).ok()?;
        remaining = rest;
        let mean = f64::from_bits(mean_bits);
        
        // Read weight
        let (weight_bits, rest) = varint_decode::u64(remaining).ok()?;
        remaining = rest;
        let weight = f64::from_bits(weight_bits);
        
        centroids.push(Centroid::new(mean, weight));
    }
    
    Some(TDigest::from_centroids(centroids))
}

/// Simple wrapper to get percentile estimate
pub fn estimate_quantile(td: &TDigest, q: f64) -> f64 {
    td.estimate_quantile(q)
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
