package db

import (
	"database/sql"
	"fmt"
	"time"

	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var fs embed.FS

type Store interface {
	AddTarget(t *Target) (int64, error)
	UpdateTarget(t *Target) error
	GetTargets() ([]Target, error)
	GetTarget(id int64) (*Target, error)
	DeleteTarget(id int64) error
	AddResult(r *Result) error
	GetResults(targetID int64, limit int) ([]Result, error)
	GetResultsByTime(targetID int64, start, end time.Time) ([]Result, error)
	Close() error

	// New methods
	AddRawResults(results []RawResult) error
	AddAggregatedResult(r *AggregatedResult) error
	GetLastRollupTime(targetID int64, windowSeconds int) (time.Time, error)
	GetRawResults(targetID int64, start, end time.Time, limit int) ([]RawResult, error)
	GetAggregatedResults(targetID int64, windowSeconds int, start, end time.Time) ([]AggregatedResult, error)
	DeleteRawResultsBefore(targetID int64, cutoff time.Time) error
	DeleteAggregatedResultsBefore(targetID int64, windowSeconds int, cutoff time.Time) error
	DeleteAggregatedResultsByWindow(targetID int64, windowSeconds int) error
	GetEarliestRawResultTime(targetID int64) (time.Time, error)

	// Status Page Stats
	GetDBSizeBytes() (int64, error)
	GetPageCount() (int64, error)
	GetPageSize() (int64, error)
	GetFreelistCount() (int64, error)
	GetTDigestStats() ([]TDigestStat, error)
	GetRawStats() (*RawStats, error)
}

type TDigestStat struct {
	TargetName    string
	WindowSeconds int
	TotalBytes    int64
	Count         int64
	AvgBytes      float64
}

type RawStats struct {
	Count      int64
	TotalBytes int64
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
	driver, err := sqlite3.WithInstance(d.DB, &sqlite3.Config{})
	if err != nil {
		return fmt.Errorf("failed to create sqlite3 driver: %w", err)
	}

	src, err := iofs.New(fs, "migrations")
	if err != nil {
		return fmt.Errorf("failed to create iofs source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite3", driver)
	if err != nil {
		return fmt.Errorf("failed to create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

type Target struct {
	ID                int64
	Name              string
	Address           string
	ProbeType         string
	ProbeConfig       string // JSON
	ProbeInterval     float64
	Timeout           float64
	RetentionPolicies string // JSON
}

type Result struct {
	Time         time.Time
	TargetID     int64
	TimeoutCount int64
	TDigestData  []byte
}

type RawResult struct {
	Time     time.Time
	TargetID int64
	Latency  float64
}

type AggregatedResult struct {
	Time          time.Time
	TargetID      int64
	WindowSeconds int
	TDigestData   []byte
	TimeoutCount  int64
}

func (d *DB) AddTarget(t *Target) (int64, error) {
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	res, err := d.Exec(`INSERT INTO targets (name, address, probe_type, probe_config, probe_interval, timeout, retention_policies) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Address, t.ProbeType, t.ProbeConfig, t.ProbeInterval, t.Timeout, t.RetentionPolicies)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateTarget(t *Target) error {
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	_, err := d.Exec(`UPDATE targets SET name=?, address=?, probe_type=?, probe_interval=?, timeout=?, retention_policies=? WHERE id=?`,
		t.Name, t.Address, t.ProbeType, t.ProbeInterval, t.Timeout, t.RetentionPolicies, t.ID)
	return err
}

func (d *DB) AddResult(r *Result) error {
	_, err := d.Exec(`INSERT INTO results (time, target_id, timeout_count, tdigest_data) 
		VALUES (?, ?, ?, ?)`,
		r.Time, r.TargetID, r.TimeoutCount, r.TDigestData)
	return err
}

func (d *DB) GetTargets() ([]Target, error) {
	rows, err := d.Query(`SELECT id, name, address, probe_type, probe_config, probe_interval, timeout, COALESCE(retention_policies, '[]') FROM targets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var targets []Target
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ID, &t.Name, &t.Address, &t.ProbeType, &t.ProbeConfig, &t.ProbeInterval, &t.Timeout, &t.RetentionPolicies); err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func (d *DB) GetTarget(id int64) (*Target, error) {
	var t Target
	err := d.QueryRow(`SELECT id, name, address, probe_type, probe_config, probe_interval, timeout, COALESCE(retention_policies, '[]') FROM targets WHERE id = ?`, id).Scan(
		&t.ID, &t.Name, &t.Address, &t.ProbeType, &t.ProbeConfig, &t.ProbeInterval, &t.Timeout, &t.RetentionPolicies,
	)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) GetResults(targetID int64, limit int) ([]Result, error) {
	rows, err := d.Query(`SELECT time, target_id, timeout_count, tdigest_data 
		FROM results WHERE target_id = ? ORDER BY time DESC LIMIT ?`, targetID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Time, &r.TargetID, &r.TimeoutCount, &r.TDigestData); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, nil
}

func (d *DB) GetResultsByTime(targetID int64, start, end time.Time) ([]Result, error) {
	rows, err := d.Query(`SELECT time, target_id, timeout_count, tdigest_data 
		FROM results WHERE target_id = ? AND time >= ? AND time <= ? ORDER BY time ASC`, targetID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.Time, &r.TargetID, &r.TimeoutCount, &r.TDigestData); err != nil {
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

func (d *DB) AddRawResults(results []RawResult) error {
	if len(results) == 0 {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}

	// Prepare statement for bulk insert
	stmt, err := tx.Prepare(`INSERT INTO raw_results (time, target_id, latency) VALUES (?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, r := range results {
		_, err = stmt.Exec(r.Time, r.TargetID, r.Latency)
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) AddAggregatedResult(r *AggregatedResult) error {
	_, err := d.Exec(`INSERT INTO aggregated_results (time, target_id, window_seconds, tdigest_data, timeout_count) 
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(time, target_id, window_seconds) DO UPDATE SET
		tdigest_data=excluded.tdigest_data,
		timeout_count=excluded.timeout_count`,
		r.Time, r.TargetID, r.WindowSeconds, r.TDigestData, r.TimeoutCount)
	return err
}

func (d *DB) GetLastRollupTime(targetID int64, windowSeconds int) (time.Time, error) {
	var ns sql.NullString
	err := d.QueryRow(`SELECT MAX(time) FROM aggregated_results WHERE target_id = ? AND window_seconds = ?`, targetID, windowSeconds).Scan(&ns)
	if err != nil {
		return time.Time{}, err
	}
	if ns.Valid {
		return parseDBTime(ns.String)
	}
	return time.Time{}, nil
}

func (d *DB) GetRawResults(targetID int64, start, end time.Time, limit int) ([]RawResult, error) {
	rows, err := d.Query(`SELECT time, target_id, latency FROM raw_results 
		WHERE target_id = ? AND time >= ? AND time < ? ORDER BY time ASC LIMIT ?`, targetID, start, end, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []RawResult
	for rows.Next() {
		var r RawResult
		if err := rows.Scan(&r.Time, &r.TargetID, &r.Latency); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) GetAggregatedResults(targetID int64, windowSeconds int, start, end time.Time) ([]AggregatedResult, error) {
	rows, err := d.Query(`SELECT time, target_id, window_seconds, tdigest_data, timeout_count 
		FROM aggregated_results 
		WHERE target_id = ? AND window_seconds = ? AND time >= ? AND time < ? ORDER BY time ASC`, targetID, windowSeconds, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []AggregatedResult
	for rows.Next() {
		var r AggregatedResult
		if err := rows.Scan(&r.Time, &r.TargetID, &r.WindowSeconds, &r.TDigestData, &r.TimeoutCount); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, nil
}

func (d *DB) DeleteRawResultsBefore(targetID int64, cutoff time.Time) error {
	_, err := d.Exec(`DELETE FROM raw_results WHERE target_id = ? AND time < ?`, targetID, cutoff)
	return err
}

func (d *DB) DeleteAggregatedResultsBefore(targetID int64, windowSeconds int, cutoff time.Time) error {
	_, err := d.Exec(`DELETE FROM aggregated_results WHERE target_id = ? AND window_seconds = ? AND time < ?`, targetID, windowSeconds, cutoff)
	return err
}

func (d *DB) DeleteAggregatedResultsByWindow(targetID int64, windowSeconds int) error {
	_, err := d.Exec(`DELETE FROM aggregated_results WHERE target_id = ? AND window_seconds = ?`, targetID, windowSeconds)
	return err
}

func (d *DB) GetEarliestRawResultTime(targetID int64) (time.Time, error) {
	var ns sql.NullString
	err := d.QueryRow(`SELECT MIN(time) FROM raw_results WHERE target_id = ?`, targetID).Scan(&ns)
	if err != nil {
		return time.Time{}, err
	}
	if ns.Valid {
		return parseDBTime(ns.String)
	}
	return time.Time{}, nil
}

func parseDBTime(s string) (time.Time, error) {
	// Try standard formats
	// SQLite driver usually uses RFC3339Nano or similar
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("failed to parse DB time: %s", s)
}

func (d *DB) GetDBSizeBytes() (int64, error) {
	var pageCount, pageSize int64
	if err := d.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return 0, err
	}
	if err := d.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, err
	}
	return pageCount * pageSize, nil
}

func (d *DB) GetPageCount() (int64, error) {
	var count int64
	if err := d.QueryRow("PRAGMA page_count").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (d *DB) GetPageSize() (int64, error) {
	var size int64
	if err := d.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		return 0, err
	}
	return size, nil
}

func (d *DB) GetFreelistCount() (int64, error) {
	var count int64
	if err := d.QueryRow("PRAGMA freelist_count").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (d *DB) GetTDigestStats() ([]TDigestStat, error) {
	rows, err := d.Query(`
		SELECT 
			t.name, 
			ar.window_seconds, 
			SUM(LENGTH(ar.tdigest_data)) as total_bytes, 
			COUNT(*) as count 
		FROM aggregated_results ar
		JOIN targets t ON ar.target_id = t.id
		GROUP BY t.id, ar.window_seconds
		ORDER BY total_bytes DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []TDigestStat
	for rows.Next() {
		var s TDigestStat
		if err := rows.Scan(&s.TargetName, &s.WindowSeconds, &s.TotalBytes, &s.Count); err != nil {
			return nil, err
		}
		if s.Count > 0 {
			s.AvgBytes = float64(s.TotalBytes) / float64(s.Count)
		}
		stats = append(stats, s)
	}
	return stats, nil
}

func (d *DB) GetRawStats() (*RawStats, error) {
	// Estimate size:
	// time (datetime string) ~30 bytes
	// target_id (int) ~8 bytes
	// latency (float) ~8 bytes
	// overhead ~ overhead
	// Or use length(time) + length(latency)? latency is real.
	// SQLite store:
	// time: TEXT (RFC3339Nano) ~ 35 bytes?
	// target_id: INTEGER
	// latency: REAL
	var count int64
	// Just counting rows for now and estimating size.
	if err := d.QueryRow("SELECT COUNT(*) FROM raw_results").Scan(&count); err != nil {
		return nil, err
	}

	// Better estimation query if we want to be closer:
	// sum(payload) isn't easy without iterating or complex generic sql.
	// But calculating size of time string is possible.
	// Let's approximate: 50 bytes per row.
	return &RawStats{
		Count:      count,
		TotalBytes: count * 50,
	}, nil
}
