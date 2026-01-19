-- Stats table for fast lookups
-- stat_key format:
--   'raw_count' -> total raw_results rows
--   'agg:<target_id>:<window_seconds>' -> count and size for aggregated_results
CREATE TABLE IF NOT EXISTS data_stats (
    stat_key TEXT PRIMARY KEY,
    row_count INTEGER DEFAULT 0,
    total_bytes INTEGER DEFAULT 0
);

-- Initialize raw_count from existing data
INSERT OR IGNORE INTO data_stats (stat_key, row_count, total_bytes)
SELECT 'raw_count', COUNT(*), COUNT(*) * 50 FROM raw_results;

-- Initialize aggregated stats from existing data
INSERT OR IGNORE INTO data_stats (stat_key, row_count, total_bytes)
SELECT 
    'agg:' || target_id || ':' || window_seconds,
    COUNT(*),
    COALESCE(SUM(LENGTH(tdigest_data)), 0)
FROM aggregated_results
GROUP BY target_id, window_seconds;

-- Triggers for raw_results
CREATE TRIGGER IF NOT EXISTS raw_results_insert_stats
AFTER INSERT ON raw_results
BEGIN
    INSERT INTO data_stats (stat_key, row_count, total_bytes)
    VALUES ('raw_count', 1, 50)
    ON CONFLICT(stat_key) DO UPDATE SET
        row_count = row_count + 1,
        total_bytes = total_bytes + 50;
END;

CREATE TRIGGER IF NOT EXISTS raw_results_delete_stats
AFTER DELETE ON raw_results
BEGIN
    UPDATE data_stats SET
        row_count = row_count - 1,
        total_bytes = total_bytes - 50
    WHERE stat_key = 'raw_count';
END;

-- Triggers for aggregated_results
CREATE TRIGGER IF NOT EXISTS agg_results_insert_stats
AFTER INSERT ON aggregated_results
BEGIN
    INSERT INTO data_stats (stat_key, row_count, total_bytes)
    VALUES ('agg:' || NEW.target_id || ':' || NEW.window_seconds, 1, COALESCE(LENGTH(NEW.tdigest_data), 0))
    ON CONFLICT(stat_key) DO UPDATE SET
        row_count = row_count + 1,
        total_bytes = total_bytes + COALESCE(LENGTH(NEW.tdigest_data), 0);
END;

CREATE TRIGGER IF NOT EXISTS agg_results_update_stats
AFTER UPDATE ON aggregated_results
BEGIN
    UPDATE data_stats SET
        total_bytes = total_bytes - COALESCE(LENGTH(OLD.tdigest_data), 0) + COALESCE(LENGTH(NEW.tdigest_data), 0)
    WHERE stat_key = 'agg:' || NEW.target_id || ':' || NEW.window_seconds;
END;

CREATE TRIGGER IF NOT EXISTS agg_results_delete_stats
AFTER DELETE ON aggregated_results
BEGIN
    UPDATE data_stats SET
        row_count = row_count - 1,
        total_bytes = total_bytes - COALESCE(LENGTH(OLD.tdigest_data), 0)
    WHERE stat_key = 'agg:' || OLD.target_id || ':' || OLD.window_seconds;
END;
