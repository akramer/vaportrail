-- Fix primary key order: change from (time, target_id, window_seconds) to (target_id, window_seconds, time)
-- This allows efficient queries that filter by target_id and window_seconds first

-- Drop triggers that reference aggregated_results
DROP TRIGGER IF EXISTS agg_results_insert_stats;
DROP TRIGGER IF EXISTS agg_results_update_stats;
DROP TRIGGER IF EXISTS agg_results_delete_stats;

-- Create new table with correct PK order
CREATE TABLE aggregated_results_new (
    time DATETIME NOT NULL,
    target_id INTEGER NOT NULL,
    window_seconds INTEGER NOT NULL,
    tdigest_data BLOB,
    timeout_count INTEGER DEFAULT 0,
    PRIMARY KEY (target_id, window_seconds, time),
    FOREIGN KEY(target_id) REFERENCES targets(id)
);

-- Copy data
INSERT INTO aggregated_results_new SELECT * FROM aggregated_results;

-- Swap tables
DROP TABLE aggregated_results;
ALTER TABLE aggregated_results_new RENAME TO aggregated_results;

-- Recreate triggers
CREATE TRIGGER agg_results_insert_stats
AFTER INSERT ON aggregated_results
BEGIN
    INSERT INTO data_stats (stat_key, row_count, total_bytes)
    VALUES ('agg:' || NEW.target_id || ':' || NEW.window_seconds, 1, COALESCE(LENGTH(NEW.tdigest_data), 0))
    ON CONFLICT(stat_key) DO UPDATE SET
        row_count = row_count + 1,
        total_bytes = total_bytes + COALESCE(LENGTH(NEW.tdigest_data), 0);
END;

CREATE TRIGGER agg_results_update_stats
AFTER UPDATE ON aggregated_results
BEGIN
    UPDATE data_stats SET
        total_bytes = total_bytes - COALESCE(LENGTH(OLD.tdigest_data), 0) + COALESCE(LENGTH(NEW.tdigest_data), 0)
    WHERE stat_key = 'agg:' || NEW.target_id || ':' || NEW.window_seconds;
END;

CREATE TRIGGER agg_results_delete_stats
AFTER DELETE ON aggregated_results
BEGIN
    UPDATE data_stats SET
        row_count = row_count - 1,
        total_bytes = total_bytes - COALESCE(LENGTH(OLD.tdigest_data), 0)
    WHERE stat_key = 'agg:' || OLD.target_id || ':' || OLD.window_seconds;
END;
