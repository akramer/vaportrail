CREATE TABLE IF NOT EXISTS targets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    address TEXT NOT NULL,
    probe_type TEXT NOT NULL,
    probe_config JSON NOT NULL,
    probe_interval REAL DEFAULT 1.0,
    commit_interval REAL DEFAULT 60.0,
    timeout REAL DEFAULT 5.0
);

CREATE TABLE IF NOT EXISTS results (
    time DATETIME NOT NULL,
    target_id INTEGER NOT NULL,
    stddev_ns REAL,
    sum_sq_ns REAL,
    timeout_count INTEGER DEFAULT 0,
    tdigest_data BLOB,
    FOREIGN KEY(target_id) REFERENCES targets(id)
);

CREATE INDEX IF NOT EXISTS idx_results_time ON results(time);
CREATE INDEX IF NOT EXISTS idx_results_target ON results(target_id);
