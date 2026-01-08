CREATE TABLE IF NOT EXISTS raw_results (
    time DATETIME NOT NULL,
    target_id INTEGER NOT NULL,
    latency REAL,
    FOREIGN KEY(target_id) REFERENCES targets(id)
);

CREATE INDEX IF NOT EXISTS idx_raw_results_time_target ON raw_results(time, target_id);

CREATE TABLE IF NOT EXISTS aggregated_results (
    time DATETIME NOT NULL,
    target_id INTEGER NOT NULL,
    window_seconds INTEGER NOT NULL,
    tdigest_data BLOB,
    timeout_count INTEGER DEFAULT 0,
    PRIMARY KEY (time, target_id, window_seconds),
    FOREIGN KEY(target_id) REFERENCES targets(id)
);

ALTER TABLE targets ADD COLUMN retention_policies JSON;
