//! SQLite database store implementation.

use chrono::{DateTime, NaiveDateTime, Utc};
use rusqlite::{params, Connection, Result as SqlResult};
use std::path::Path;
use std::sync::{Arc, Mutex};
use thiserror::Error;

use super::models::*;

/// Database error types.
#[derive(Error, Debug)]
pub enum DbError {
    #[error("SQLite error: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("Migration error: {0}")]
    Migration(String),
    #[error("Not found")]
    NotFound,
}

/// Thread-safe database store.
#[derive(Clone)]
pub struct Store {
    conn: Arc<Mutex<Connection>>,
}

impl Store {
    /// Create a new store with the given database path.
    pub fn new<P: AsRef<Path>>(path: P) -> Result<Self, DbError> {
        let conn = Connection::open(path)?;
        let store = Self {
            conn: Arc::new(Mutex::new(conn)),
        };
        store.init()?;
        Ok(store)
    }

    /// Initialize the database with migrations.
    fn init(&self) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        
        // Run migrations inline (embedded SQL)
        conn.execute_batch(include_str!("../../migrations/000001_init.up.sql"))
            .map_err(|e| DbError::Migration(format!("Migration 1 failed: {}", e)))?;
        
        // Try to run subsequent migrations, ignoring "already exists" errors
        let _ = conn.execute_batch(include_str!("../../migrations/000002_drop_stddev.up.sql"));
        let _ = conn.execute_batch(include_str!("../../migrations/000003_raw_and_rollups.up.sql"));
        let _ = conn.execute_batch(include_str!("../../migrations/000004_drop_commit_interval.up.sql"));
        let _ = conn.execute_batch(include_str!("../../migrations/000005_default_retention_policies.up.sql"));
        
        Ok(())
    }

    // --- Target CRUD ---

    /// Add a new target and return its ID.
    pub fn add_target(&self, target: &mut Target) -> Result<i64, DbError> {
        if target.probe_interval <= 0.0 {
            target.probe_interval = 1.0;
        }
        if target.timeout <= 0.0 {
            target.timeout = 5.0;
        }
        
        let conn = self.conn.lock().unwrap();
        conn.execute(
            "INSERT INTO targets (name, address, probe_type, probe_config, probe_interval, timeout, retention_policies) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                target.name,
                target.address,
                target.probe_type,
                target.probe_config,
                target.probe_interval,
                target.timeout,
                target.retention_policies,
            ],
        )?;
        let id = conn.last_insert_rowid();
        target.id = id;
        Ok(id)
    }

    /// Update an existing target.
    pub fn update_target(&self, target: &Target) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        let probe_interval = if target.probe_interval <= 0.0 { 1.0 } else { target.probe_interval };
        let timeout = if target.timeout <= 0.0 { 5.0 } else { target.timeout };
        
        conn.execute(
            "UPDATE targets SET name=?1, address=?2, probe_type=?3, probe_interval=?4, timeout=?5, retention_policies=?6 WHERE id=?7",
            params![
                target.name,
                target.address,
                target.probe_type,
                probe_interval,
                timeout,
                target.retention_policies,
                target.id,
            ],
        )?;
        Ok(())
    }

    /// Get all targets.
    pub fn get_targets(&self) -> Result<Vec<Target>, DbError> {
        let conn = self.conn.lock().unwrap();
        let mut stmt = conn.prepare(
            "SELECT id, name, address, probe_type, probe_config, probe_interval, timeout, COALESCE(retention_policies, '[]') FROM targets"
        )?;
        
        let targets = stmt.query_map([], |row| {
            Ok(Target {
                id: row.get(0)?,
                name: row.get(1)?,
                address: row.get(2)?,
                probe_type: row.get(3)?,
                probe_config: row.get(4)?,
                probe_interval: row.get(5)?,
                timeout: row.get(6)?,
                retention_policies: row.get(7)?,
            })
        })?
        .collect::<SqlResult<Vec<_>>>()?;
        
        Ok(targets)
    }

    /// Get a target by ID.
    pub fn get_target(&self, id: i64) -> Result<Target, DbError> {
        let conn = self.conn.lock().unwrap();
        let target = conn.query_row(
            "SELECT id, name, address, probe_type, probe_config, probe_interval, timeout, COALESCE(retention_policies, '[]') FROM targets WHERE id = ?1",
            params![id],
            |row| {
                Ok(Target {
                    id: row.get(0)?,
                    name: row.get(1)?,
                    address: row.get(2)?,
                    probe_type: row.get(3)?,
                    probe_config: row.get(4)?,
                    probe_interval: row.get(5)?,
                    timeout: row.get(6)?,
                    retention_policies: row.get(7)?,
                })
            },
        )?;
        Ok(target)
    }

    /// Delete a target and its results.
    pub fn delete_target(&self, id: i64) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        conn.execute("DELETE FROM results WHERE target_id = ?1", params![id])?;
        conn.execute("DELETE FROM raw_results WHERE target_id = ?1", params![id])?;
        conn.execute("DELETE FROM aggregated_results WHERE target_id = ?1", params![id])?;
        conn.execute("DELETE FROM targets WHERE id = ?1", params![id])?;
        Ok(())
    }

    // --- Raw Results ---

    /// Add raw results in batch.
    pub fn add_raw_results(&self, results: &[RawResult]) -> Result<(), DbError> {
        if results.is_empty() {
            return Ok(());
        }
        
        let conn = self.conn.lock().unwrap();
        let tx = conn.unchecked_transaction()?;
        
        {
            let mut stmt = tx.prepare(
                "INSERT INTO raw_results (time, target_id, latency) VALUES (?1, ?2, ?3)"
            )?;
            
            for r in results {
                stmt.execute(params![
                    r.time.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                    r.target_id,
                    r.latency,
                ])?;
            }
        }
        
        tx.commit()?;
        Ok(())
    }

    /// Get raw results for a target within a time range.
    pub fn get_raw_results(
        &self,
        target_id: i64,
        start: DateTime<Utc>,
        end: DateTime<Utc>,
        limit: i32,
    ) -> Result<Vec<RawResult>, DbError> {
        let conn = self.conn.lock().unwrap();
        let mut stmt = conn.prepare(
            "SELECT time, target_id, latency FROM raw_results 
             WHERE target_id = ?1 AND time >= ?2 AND time < ?3 ORDER BY time ASC LIMIT ?4"
        )?;
        
        let results = stmt.query_map(
            params![
                target_id,
                start.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                end.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                limit,
            ],
            |row| {
                let time_str: String = row.get(0)?;
                let time = parse_db_time(&time_str).unwrap_or_else(Utc::now);
                Ok(RawResult {
                    time,
                    target_id: row.get(1)?,
                    latency: row.get(2)?,
                })
            },
        )?
        .collect::<SqlResult<Vec<_>>>()?;
        
        Ok(results)
    }

    /// Delete raw results before a cutoff time.
    pub fn delete_raw_results_before(&self, target_id: i64, cutoff: DateTime<Utc>) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        conn.execute(
            "DELETE FROM raw_results WHERE target_id = ?1 AND time < ?2",
            params![target_id, cutoff.format("%Y-%m-%d %H:%M:%S%.9f").to_string()],
        )?;
        Ok(())
    }

    /// Get earliest raw result time for a target.
    pub fn get_earliest_raw_result_time(&self, target_id: i64) -> Result<Option<DateTime<Utc>>, DbError> {
        let conn = self.conn.lock().unwrap();
        let result: Option<String> = conn.query_row(
            "SELECT MIN(time) FROM raw_results WHERE target_id = ?1",
            params![target_id],
            |row| row.get(0),
        )?;
        
        Ok(result.and_then(|s| parse_db_time(&s)))
    }

    // --- Aggregated Results ---

    /// Add a single aggregated result.
    pub fn add_aggregated_result(&self, result: &AggregatedResult) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        conn.execute(
            "INSERT INTO aggregated_results (time, target_id, window_seconds, tdigest_data, timeout_count) 
             VALUES (?1, ?2, ?3, ?4, ?5)
             ON CONFLICT(time, target_id, window_seconds) DO UPDATE SET
             tdigest_data=excluded.tdigest_data, timeout_count=excluded.timeout_count",
            params![
                result.time.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                result.target_id,
                result.window_seconds,
                result.tdigest_data,
                result.timeout_count,
            ],
        )?;
        Ok(())
    }

    /// Add aggregated results in batch.
    pub fn add_aggregated_results(&self, results: &[AggregatedResult]) -> Result<(), DbError> {
        if results.is_empty() {
            return Ok(());
        }
        
        let conn = self.conn.lock().unwrap();
        let tx = conn.unchecked_transaction()?;
        
        {
            let mut stmt = tx.prepare(
                "INSERT INTO aggregated_results (time, target_id, window_seconds, tdigest_data, timeout_count) 
                 VALUES (?1, ?2, ?3, ?4, ?5)
                 ON CONFLICT(time, target_id, window_seconds) DO UPDATE SET
                 tdigest_data=excluded.tdigest_data, timeout_count=excluded.timeout_count"
            )?;
            
            for r in results {
                stmt.execute(params![
                    r.time.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                    r.target_id,
                    r.window_seconds,
                    r.tdigest_data,
                    r.timeout_count,
                ])?;
            }
        }
        
        tx.commit()?;
        Ok(())
    }

    /// Get aggregated results for a target and window.
    pub fn get_aggregated_results(
        &self,
        target_id: i64,
        window_seconds: i32,
        start: DateTime<Utc>,
        end: DateTime<Utc>,
    ) -> Result<Vec<AggregatedResult>, DbError> {
        let conn = self.conn.lock().unwrap();
        let mut stmt = conn.prepare(
            "SELECT time, target_id, window_seconds, tdigest_data, timeout_count 
             FROM aggregated_results 
             WHERE target_id = ?1 AND window_seconds = ?2 AND time >= ?3 AND time < ?4 
             ORDER BY time ASC"
        )?;
        
        let results = stmt.query_map(
            params![
                target_id,
                window_seconds,
                start.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
                end.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
            ],
            |row| {
                let time_str: String = row.get(0)?;
                let time = parse_db_time(&time_str).unwrap_or_else(Utc::now);
                Ok(AggregatedResult {
                    time,
                    target_id: row.get(1)?,
                    window_seconds: row.get(2)?,
                    tdigest_data: row.get(3)?,
                    timeout_count: row.get(4)?,
                })
            },
        )?
        .collect::<SqlResult<Vec<_>>>()?;
        
        Ok(results)
    }

    /// Get the last rollup time for a target and window.
    pub fn get_last_rollup_time(&self, target_id: i64, window_seconds: i32) -> Result<Option<DateTime<Utc>>, DbError> {
        let conn = self.conn.lock().unwrap();
        let result: Option<String> = conn.query_row(
            "SELECT MAX(time) FROM aggregated_results WHERE target_id = ?1 AND window_seconds = ?2",
            params![target_id, window_seconds],
            |row| row.get(0),
        )?;
        
        Ok(result.and_then(|s| parse_db_time(&s)))
    }

    /// Delete aggregated results before a cutoff.
    pub fn delete_aggregated_results_before(
        &self,
        target_id: i64,
        window_seconds: i32,
        cutoff: DateTime<Utc>,
    ) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        conn.execute(
            "DELETE FROM aggregated_results WHERE target_id = ?1 AND window_seconds = ?2 AND time < ?3",
            params![
                target_id,
                window_seconds,
                cutoff.format("%Y-%m-%d %H:%M:%S%.9f").to_string(),
            ],
        )?;
        Ok(())
    }

    /// Delete all aggregated results for a specific window size.
    pub fn delete_aggregated_results_by_window(&self, target_id: i64, window_seconds: i32) -> Result<(), DbError> {
        let conn = self.conn.lock().unwrap();
        conn.execute(
            "DELETE FROM aggregated_results WHERE target_id = ?1 AND window_seconds = ?2",
            params![target_id, window_seconds],
        )?;
        Ok(())
    }

    // --- Status Page Stats ---

    /// Get database size in bytes.
    pub fn get_db_size_bytes(&self) -> Result<i64, DbError> {
        let conn = self.conn.lock().unwrap();
        let page_count: i64 = conn.query_row("PRAGMA page_count", [], |r| r.get(0))?;
        let page_size: i64 = conn.query_row("PRAGMA page_size", [], |r| r.get(0))?;
        Ok(page_count * page_size)
    }

    /// Get page count.
    pub fn get_page_count(&self) -> Result<i64, DbError> {
        let conn = self.conn.lock().unwrap();
        Ok(conn.query_row("PRAGMA page_count", [], |r| r.get(0))?)
    }

    /// Get page size.
    pub fn get_page_size(&self) -> Result<i64, DbError> {
        let conn = self.conn.lock().unwrap();
        Ok(conn.query_row("PRAGMA page_size", [], |r| r.get(0))?)
    }

    /// Get freelist count.
    pub fn get_freelist_count(&self) -> Result<i64, DbError> {
        let conn = self.conn.lock().unwrap();
        Ok(conn.query_row("PRAGMA freelist_count", [], |r| r.get(0))?)
    }

    /// Get TDigest storage statistics.
    pub fn get_tdigest_stats(&self) -> Result<Vec<TDigestStat>, DbError> {
        let conn = self.conn.lock().unwrap();
        let mut stmt = conn.prepare(
            "SELECT t.name, ar.window_seconds, SUM(LENGTH(ar.tdigest_data)) as total_bytes, COUNT(*) as count 
             FROM aggregated_results ar
             JOIN targets t ON ar.target_id = t.id
             GROUP BY t.id, ar.window_seconds
             ORDER BY total_bytes DESC"
        )?;
        
        let stats = stmt.query_map([], |row| {
            let total_bytes: i64 = row.get(2)?;
            let count: i64 = row.get(3)?;
            Ok(TDigestStat {
                target_name: row.get(0)?,
                window_seconds: row.get(1)?,
                total_bytes,
                count,
                avg_bytes: if count > 0 { total_bytes as f64 / count as f64 } else { 0.0 },
            })
        })?
        .collect::<SqlResult<Vec<_>>>()?;
        
        Ok(stats)
    }

    /// Get raw results statistics.
    pub fn get_raw_stats(&self) -> Result<RawStats, DbError> {
        let conn = self.conn.lock().unwrap();
        let count: i64 = conn.query_row("SELECT COUNT(*) FROM raw_results", [], |r| r.get(0))?;
        Ok(RawStats {
            count,
            total_bytes: count * 50, // Estimate ~50 bytes per row
        })
    }
}

/// Parse a datetime string from the database.
fn parse_db_time(s: &str) -> Option<DateTime<Utc>> {
    // Try various formats
    let formats = [
        "%Y-%m-%d %H:%M:%S%.9f",
        "%Y-%m-%d %H:%M:%S%.f",
        "%Y-%m-%d %H:%M:%S",
        "%Y-%m-%dT%H:%M:%S%.9fZ",
        "%Y-%m-%dT%H:%M:%SZ",
    ];
    
    for fmt in &formats {
        if let Ok(dt) = NaiveDateTime::parse_from_str(s, fmt) {
            return Some(DateTime::from_naive_utc_and_offset(dt, Utc));
        }
    }
    
    // Try ISO 8601
    if let Ok(dt) = DateTime::parse_from_rfc3339(s) {
        return Some(dt.with_timezone(&Utc));
    }
    
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::NamedTempFile;

    #[test]
    fn test_target_crud() {
        let tmp = NamedTempFile::new().unwrap();
        let store = Store::new(tmp.path()).unwrap();
        
        // Create
        let mut target = Target {
            name: "Test".to_string(),
            address: "example.com".to_string(),
            probe_type: "ping".to_string(),
            ..Default::default()
        };
        let id = store.add_target(&mut target).unwrap();
        assert!(id > 0);
        
        // Read
        let fetched = store.get_target(id).unwrap();
        assert_eq!(fetched.name, "Test");
        
        // Update
        let mut updated = fetched;
        updated.name = "Updated".to_string();
        store.update_target(&updated).unwrap();
        
        let fetched2 = store.get_target(id).unwrap();
        assert_eq!(fetched2.name, "Updated");
        
        // Delete
        store.delete_target(id).unwrap();
        assert!(store.get_target(id).is_err());
    }
}
