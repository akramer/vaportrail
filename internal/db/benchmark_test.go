package db

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func setupBenchmarkDB(b *testing.B) (*DB, func()) {
	// Create a temporary file for the database
	f, err := os.CreateTemp("", "benchmark_*.db")
	if err != nil {
		b.Fatalf("Failed to create temp db file: %v", err)
	}
	dbPath := f.Name()
	f.Close()

	d, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create db: %v", err)
	}

	// Disable synchronous commit for faster setup
	_, err = d.Exec("PRAGMA synchronous = OFF")
	if err != nil {
		b.Fatalf("Failed to set synchronous OFF: %v", err)
	}

	return d, func() {
		d.Close()
		os.Remove(dbPath)
	}
}

func populateBenchmarkData(b *testing.B, d *DB, numTargets, rowsPerTarget int) []int64 {
	var targetIDs []int64
	now := time.Now().UTC()

	// Create targets
	for i := 0; i < numTargets; i++ {
		t := &Target{
			Name:      fmt.Sprintf("Target-%d", i),
			Address:   "http://localhost",
			ProbeType: "http",
		}
		id, err := d.AddTarget(t)
		if err != nil {
			b.Fatalf("Failed to add target: %v", err)
		}
		targetIDs = append(targetIDs, id)

		// Create raw results in batches
		batchSize := 1000
		for j := 0; j < rowsPerTarget; j += batchSize {
			var batch []RawResult
			for k := 0; k < batchSize && (j+k) < rowsPerTarget; k++ {
				// Distribute time to have range queries meaningful
				// 1 minute apart
				r := RawResult{
					Time:     now.Add(-time.Duration(rowsPerTarget-(j+k)) * time.Minute),
					TargetID: id,
					Latency:  float64(100 + (j+k)%100),
				}
				batch = append(batch, r)
			}
			if err := d.AddRawResults(batch); err != nil {
				b.Fatalf("Failed to add raw results: %v", err)
			}
		}
	}
	return targetIDs
}

func BenchmarkGetRawResults(b *testing.B) {
	d, cleanup := setupBenchmarkDB(b)
	defer cleanup()

	numTargets := 5
	rowsPerTarget := 10000
	targetIDs := populateBenchmarkData(b, d, numTargets, rowsPerTarget)

	// Query for a range in the middle
	now := time.Now().UTC()
	end := now.Add(-time.Duration(rowsPerTarget/2) * time.Minute)
	start := end.Add(-60 * time.Minute) // 1 hour range

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Use varying target to avoid simple caching if any (though SQLite cache is page based)
		tid := targetIDs[i%numTargets]
		_, err := d.GetRawResults(tid, start, end, 100)
		if err != nil {
			b.Fatalf("GetRawResults failed: %v", err)
		}
	}
}

func BenchmarkGetEarliestRawResultTime(b *testing.B) {
	d, cleanup := setupBenchmarkDB(b)
	defer cleanup()

	numTargets := 5
	rowsPerTarget := 10000
	targetIDs := populateBenchmarkData(b, d, numTargets, rowsPerTarget)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tid := targetIDs[i%numTargets]
		_, err := d.GetEarliestRawResultTime(tid)
		if err != nil {
			b.Fatalf("GetEarliestRawResultTime failed: %v", err)
		}
	}
}

func BenchmarkGetEarliestRawResultTime_Sparse(b *testing.B) {
	d, cleanup := setupBenchmarkDB(b)
	defer cleanup()

	// Scenario: Target 1 has massive history. Target 2 has only recent history.
	// We want to find earliest time for Target 2.
	// With (time, target_id), it has to scan through Target 1's history.
	// With (target_id, time), it jumps to Target 2.

	// Target 1: Old data
	t1 := &Target{Name: "OldTarget", Address: "test", ProbeType: "http"}
	id1, _ := d.AddTarget(t1)

	now := time.Now().UTC()
	// Insert 10000 rows for T1, starting 1 year ago
	batchSize := 1000
	for j := 0; j < 10000; j += batchSize {
		var batch []RawResult
		for k := 0; k < batchSize; k++ {
			r := RawResult{
				Time:     now.Add(-365*24*time.Hour).Add(time.Duration(j+k) * time.Minute),
				TargetID: id1,
				Latency:  100,
			}
			batch = append(batch, r)
		}
		d.AddRawResults(batch)
	}

	// Target 2: New data
	t2 := &Target{Name: "NewTarget", Address: "test", ProbeType: "http"}
	id2, _ := d.AddTarget(t2)
	// Insert 100 rows for T2, starting now
	var batch []RawResult
	for k := 0; k < 100; k++ {
		r := RawResult{
			Time:     now.Add(time.Duration(k) * time.Minute),
			TargetID: id2,
			Latency:  100,
		}
		batch = append(batch, r)
	}
	d.AddRawResults(batch)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := d.GetEarliestRawResultTime(id2)
		if err != nil {
			b.Fatalf("GetEarliestRawResultTime failed: %v", err)
		}
	}
}
