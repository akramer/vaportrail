DROP INDEX IF EXISTS idx_raw_results_target_time;
CREATE INDEX IF NOT EXISTS idx_raw_results_time_target ON raw_results(time, target_id);
