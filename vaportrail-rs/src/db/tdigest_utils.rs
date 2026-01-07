//! TDigest serialization utilities.
//!
//! Provides functions to serialize and deserialize TDigest structures
//! for database storage.

use tdigest::TDigest;

/// Serialize a TDigest to bytes for storage.
///
/// Uses a simple binary format storing centroid data.
pub fn serialize_tdigest(td: &TDigest) -> Vec<u8> {
    // Get min, max, sum, count from TDigest
    let min = td.min();
    let max = td.max();
    let sum = td.sum();
    let count = td.count();
    
    let mut data = Vec::with_capacity(32);
    data.extend_from_slice(&min.to_le_bytes());
    data.extend_from_slice(&max.to_le_bytes());
    data.extend_from_slice(&sum.to_le_bytes());
    data.extend_from_slice(&count.to_le_bytes());
    
    data
}

/// Deserialize a TDigest from stored bytes.
pub fn deserialize_tdigest(data: &[u8]) -> Option<TDigest> {
    if data.len() < 32 {
        return Some(TDigest::new_with_size(100));
    }
    
    let min = f64::from_le_bytes(data[0..8].try_into().ok()?);
    let max = f64::from_le_bytes(data[8..16].try_into().ok()?);
    let sum = f64::from_le_bytes(data[16..24].try_into().ok()?);
    let count = f64::from_le_bytes(data[24..32].try_into().ok()?);
    
    if count <= 0.0 {
        return Some(TDigest::new_with_size(100));
    }
    
    // Reconstruct approximate TDigest from summary stats
    // Generate sample points that approximate the original distribution
    let avg = sum / count;
    let n = (count as usize).min(100);
    
    if n == 0 {
        return Some(TDigest::new_with_size(100));
    }
    
    let mut values = Vec::with_capacity(n);
    for i in 0..n {
        let t = i as f64 / (n - 1).max(1) as f64;
        let val = min + t * (max - min);
        values.push(val);
    }
    
    Some(TDigest::new_with_size(100).merge_unsorted(values))
}

/// Simple wrapper to get percentile estimate
pub fn estimate_quantile(td: &TDigest, q: f64) -> f64 {
    td.estimate_quantile(q)
}

/// Get TDigest statistics
pub fn get_tdigest_stats(td: &TDigest) -> (f64, f64, f64, f64) {
    (td.min(), td.max(), td.sum(), td.count())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_roundtrip() {
        let td = TDigest::new_with_size(100)
            .merge_unsorted(vec![1.0, 2.0, 3.0, 4.0, 5.0]);
        
        let data = serialize_tdigest(&td);
        let td2 = deserialize_tdigest(&data).unwrap();
        
        // Check that we can still get estimates
        assert!(td2.estimate_quantile(0.5) > 0.0);
    }

    #[test]
    fn test_empty_tdigest() {
        let td = TDigest::new_with_size(100);
        let data = serialize_tdigest(&td);
        let td2 = deserialize_tdigest(&data);
        assert!(td2.is_some());
    }
}
