DROP INDEX IF EXISTS idx_raw_results_time_target;
CREATE INDEX IF NOT EXISTS idx_raw_results_target_time ON raw_results(target_id, time);
