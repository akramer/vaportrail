package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Store interface {
	AddTarget(t *Target) (int64, error)
	UpdateTarget(t *Target) error
	GetTargets() ([]Target, error)
	DeleteTarget(id int64) error
	AddResult(r *Result) error
	GetResults(targetID int64, limit int) ([]Result, error)
	GetResultsByTime(targetID int64, start, end time.Time) ([]Result, error)
	Close() error
}

type DB struct {
	*sql.DB
}

func New(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}

	s := &DB{db}
	if err := s.init(); err != nil {
		return nil, err
	}
	return s, nil
}

func (d *DB) init() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS targets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			address TEXT NOT NULL,
			probe_type TEXT NOT NULL,
			probe_config JSON NOT NULL,
			probe_interval REAL DEFAULT 1.0,
			commit_interval REAL DEFAULT 60.0,
			timeout REAL DEFAULT 5.0
		);`,
		`CREATE TABLE IF NOT EXISTS results (
			time DATETIME NOT NULL,
			target_id INTEGER NOT NULL,
			stddev_ns REAL,
			sum_sq_ns REAL,
			timeout_count INTEGER DEFAULT 0,
			tdigest_data BLOB,
			FOREIGN KEY(target_id) REFERENCES targets(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_results_time ON results(time);`,
		`CREATE INDEX IF NOT EXISTS idx_results_target ON results(target_id);`,
	}

	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return fmt.Errorf("init query failed: %w", err)
		}
	}

	return nil
}

type Target struct {
	ID             int64
	Name           string
	Address        string
	ProbeType      string
	ProbeConfig    string // JSON
	ProbeInterval  float64
	CommitInterval float64
	Timeout        float64
}

type Result struct {
	Time         time.Time
	TargetID     int64
	StdDevNS     float64
	SumSqNS      float64
	TimeoutCount int64
	TDigestData  []byte
}

func (d *DB) AddTarget(t *Target) (int64, error) {
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.CommitInterval <= 0 {
		t.CommitInterval = 60.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	res, err := d.Exec(`INSERT INTO targets (name, address, probe_type, probe_config, probe_interval, commit_interval, timeout) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Address, t.ProbeType, t.ProbeConfig, t.ProbeInterval, t.CommitInterval, t.Timeout)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateTarget(t *Target) error {
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.CommitInterval <= 0 {
		t.CommitInterval = 60.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	_, err := d.Exec(`UPDATE targets SET name=?, address=?, probe_type=?, probe_interval=?, commit_interval=?, timeout=? WHERE id=?`,
		t.Name, t.Address, t.ProbeType, t.ProbeInterval, t.CommitInterval, t.Timeout, t.ID)
	return err
}

func (d *DB) AddResult(r *Result) error {
	_, err := d.Exec(`INSERT INTO results (time, target_id, stddev_ns, sum_sq_ns, timeout_count, tdigest_data) 
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.Time, r.TargetID, r.StdDevNS, r.SumSqNS, r.TimeoutCount, r.TDigestData)
	return err
}

func (d *DB) GetTargets() ([]Target, error) {
	rows, err := d.Query(`SELECT id, name, address, probe_type, probe_config, probe_interval, commit_interval, timeout FROM targets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Name, &t.Address, &t.ProbeType, &t.ProbeConfig, &t.ProbeInterval, &t.CommitInterval, &t.Timeout); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func (d *DB) GetResults(targetID int64, limit int) ([]Result, error) {
	rows, err := d.Query(`SELECT time, target_id, stddev_ns, sum_sq_ns, timeout_count, tdigest_data 
		FROM results WHERE target_id = ? ORDER BY time DESC LIMIT ?`, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Time, &r.TargetID, &r.StdDevNS, &r.SumSqNS, &r.TimeoutCount, &r.TDigestData); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (d *DB) GetResultsByTime(targetID int64, start, end time.Time) ([]Result, error) {
	rows, err := d.Query(`SELECT time, target_id, stddev_ns, sum_sq_ns, timeout_count, tdigest_data 
		FROM results WHERE target_id = ? AND time >= ? AND time <= ? ORDER BY time ASC`, targetID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Time, &r.TargetID, &r.StdDevNS, &r.SumSqNS, &r.TimeoutCount, &r.TDigestData); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (d *DB) DeleteTarget(id int64) error {
	_, err := d.Exec(`DELETE FROM results WHERE target_id = ?`, id)
	if err != nil {
		return err
	}
	_, err = d.Exec(`DELETE FROM targets WHERE id = ?`, id)
	return err
}
