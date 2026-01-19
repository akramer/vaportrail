package db

import (
	"testing"
	"time"
)

func TestGetEarliestRawResultTime(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}
	defer d.Close()

	// 1. Add target
	target := &Target{
		Name:      "test",
		Address:   "test",
		ProbeType: "http",
	}
	id, _ := d.AddTarget(target)

	// 2. Add raw results
	now := time.Now().UTC()
	raws := []RawResult{
		{Time: now.Add(-10 * time.Minute), TargetID: id, Latency: 100},
		{Time: now.Add(-5 * time.Minute), TargetID: id, Latency: 100},
	}
	d.AddRawResults(raws)

	// 3. Get Earliest
	earliest, err := d.GetEarliestRawResultTime(id)
	if err != nil {
		t.Fatalf("GetEarliestRawResultTime failed: %v", err)
	}

	// Compare unix timestamp to ignore nanosecond diffs if any
	// SQLite storage might truncate/round depending on format
	if !earliest.Equal(raws[0].Time) {
		// Try fuzzy match? RFC3339 doesn't have nanos usually unless specified.
		// Go time.Time has monotonic clock. Use Equal or check Unix().
		// If driver stores as string, monotonic clock is lost.
		// Let's compare UnixNano or just formatting.
		if earliest.Format(time.RFC3339) != raws[0].Time.Format(time.RFC3339) {
			t.Errorf("Expected earliest %v, got %v", raws[0].Time, earliest)
		}
	}
}

func TestGetLastRollupTime(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}
	defer d.Close()

	id, _ := d.AddTarget(&Target{Name: "test", Address: "test", ProbeType: "http"})
	now := time.Now().UTC()

	// Add aggregated result
	agg := &AggregatedResult{
		Time:          now,
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   []byte{},
		TimeoutCount:  0,
	}
	d.AddAggregatedResult(agg)

	// Get Last Rollup
	last, err := d.GetLastRollupTime(id, 60)
	if err != nil {
		t.Fatalf("GetLastRollupTime failed: %v", err)
	}

	if last.Format(time.RFC3339) != now.Format(time.RFC3339) {
		t.Errorf("Expected last rollup %v, got %v", now, last)
	}
}

func TestDataStatsTriggers_RawResults(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}
	defer d.Close()

	// Verify stats start at 0 (or no row)
	stats, err := d.GetRawStats()
	if err != nil {
		t.Fatalf("GetRawStats failed: %v", err)
	}
	if stats.Count != 0 {
		t.Errorf("Expected initial count 0, got %d", stats.Count)
	}

	// Add target
	id, _ := d.AddTarget(&Target{Name: "test", Address: "test", ProbeType: "http"})
	now := time.Now().UTC()

	// Add raw results and check count increases
	raws := []RawResult{
		{Time: now.Add(-10 * time.Minute), TargetID: id, Latency: 100},
		{Time: now.Add(-5 * time.Minute), TargetID: id, Latency: 200},
		{Time: now.Add(-1 * time.Minute), TargetID: id, Latency: 300},
	}
	if err := d.AddRawResults(raws); err != nil {
		t.Fatalf("AddRawResults failed: %v", err)
	}

	stats, err = d.GetRawStats()
	if err != nil {
		t.Fatalf("GetRawStats failed: %v", err)
	}
	if stats.Count != 3 {
		t.Errorf("Expected count 3 after insert, got %d", stats.Count)
	}
	if stats.TotalBytes != 3*50 {
		t.Errorf("Expected total bytes %d, got %d", 3*50, stats.TotalBytes)
	}

	// Delete raw results and check count decreases
	if err := d.DeleteRawResultsBefore(id, now); err != nil {
		t.Fatalf("DeleteRawResultsBefore failed: %v", err)
	}

	stats, err = d.GetRawStats()
	if err != nil {
		t.Fatalf("GetRawStats failed: %v", err)
	}
	if stats.Count != 0 {
		t.Errorf("Expected count 0 after delete, got %d", stats.Count)
	}
	if stats.TotalBytes != 0 {
		t.Errorf("Expected total bytes 0 after delete, got %d", stats.TotalBytes)
	}
}

func TestDataStatsTriggers_AggregatedResults(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}
	defer d.Close()

	// Add target
	id, _ := d.AddTarget(&Target{Name: "TestTarget", Address: "test", ProbeType: "http"})
	now := time.Now().UTC().Truncate(time.Minute)

	// Initially no tdigest stats
	tdStats, err := d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	if len(tdStats) != 0 {
		t.Errorf("Expected 0 tdigest stats initially, got %d", len(tdStats))
	}

	// Add aggregated result with known blob size
	blob1 := make([]byte, 100)
	agg1 := &AggregatedResult{
		Time:          now,
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   blob1,
		TimeoutCount:  0,
	}
	if err := d.AddAggregatedResult(agg1); err != nil {
		t.Fatalf("AddAggregatedResult failed: %v", err)
	}

	tdStats, err = d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	if len(tdStats) != 1 {
		t.Fatalf("Expected 1 tdigest stat, got %d", len(tdStats))
	}
	if tdStats[0].Count != 1 {
		t.Errorf("Expected count 1, got %d", tdStats[0].Count)
	}
	if tdStats[0].TotalBytes != 100 {
		t.Errorf("Expected total bytes 100, got %d", tdStats[0].TotalBytes)
	}
	if tdStats[0].TargetName != "TestTarget" {
		t.Errorf("Expected target name 'TestTarget', got '%s'", tdStats[0].TargetName)
	}
	if tdStats[0].WindowSeconds != 60 {
		t.Errorf("Expected window 60, got %d", tdStats[0].WindowSeconds)
	}

	// Add another aggregated result at different time
	blob2 := make([]byte, 150)
	agg2 := &AggregatedResult{
		Time:          now.Add(time.Minute),
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   blob2,
		TimeoutCount:  0,
	}
	if err := d.AddAggregatedResult(agg2); err != nil {
		t.Fatalf("AddAggregatedResult failed: %v", err)
	}

	tdStats, err = d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	if len(tdStats) != 1 {
		t.Fatalf("Expected 1 tdigest stat (same target/window), got %d", len(tdStats))
	}
	if tdStats[0].Count != 2 {
		t.Errorf("Expected count 2, got %d", tdStats[0].Count)
	}
	if tdStats[0].TotalBytes != 250 {
		t.Errorf("Expected total bytes 250, got %d", tdStats[0].TotalBytes)
	}

	// Update an aggregated result (UPSERT) - blob size changes
	blob3 := make([]byte, 200)
	agg1Updated := &AggregatedResult{
		Time:          now,
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   blob3,
		TimeoutCount:  1,
	}
	if err := d.AddAggregatedResult(agg1Updated); err != nil {
		t.Fatalf("AddAggregatedResult (update) failed: %v", err)
	}

	tdStats, err = d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	// Count should still be 2 (upsert, not a new row)
	// Total bytes should be 200 + 150 = 350 (old 100 became 200)
	if tdStats[0].Count != 2 {
		t.Errorf("Expected count 2 after update, got %d", tdStats[0].Count)
	}
	if tdStats[0].TotalBytes != 350 {
		t.Errorf("Expected total bytes 350 after update, got %d", tdStats[0].TotalBytes)
	}

	// Delete aggregated results
	if err := d.DeleteAggregatedResultsByWindow(id, 60); err != nil {
		t.Fatalf("DeleteAggregatedResultsByWindow failed: %v", err)
	}

	tdStats, err = d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	// After delete, either the stat is removed or count is 0
	// Our query only returns rows with count > 0 after join, so should be empty
	if len(tdStats) != 0 {
		t.Errorf("Expected 0 tdigest stats after delete, got %d (count=%d)", len(tdStats), tdStats[0].Count)
	}
}

func TestGetStatsFromDataStats(t *testing.T) {
	d, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}
	defer d.Close()

	// Add target
	id, _ := d.AddTarget(&Target{Name: "StatsTest", Address: "test", ProbeType: "http"})
	now := time.Now().UTC()

	// Add some raw results
	raws := make([]RawResult, 100)
	for i := 0; i < 100; i++ {
		raws[i] = RawResult{
			Time:     now.Add(time.Duration(i) * time.Second),
			TargetID: id,
			Latency:  float64(i * 1000000), // ns
		}
	}
	if err := d.AddRawResults(raws); err != nil {
		t.Fatalf("AddRawResults failed: %v", err)
	}

	// Add some aggregated results with different windows
	for i := 0; i < 10; i++ {
		blob := make([]byte, 50+i*10)
		agg := &AggregatedResult{
			Time:          now.Add(time.Duration(i) * time.Minute),
			TargetID:      id,
			WindowSeconds: 60,
			TDigestData:   blob,
		}
		d.AddAggregatedResult(agg)
	}

	for i := 0; i < 5; i++ {
		blob := make([]byte, 100+i*20)
		agg := &AggregatedResult{
			Time:          now.Add(time.Duration(i) * time.Hour),
			TargetID:      id,
			WindowSeconds: 3600,
			TDigestData:   blob,
		}
		d.AddAggregatedResult(agg)
	}

	// Verify GetRawStats
	rawStats, err := d.GetRawStats()
	if err != nil {
		t.Fatalf("GetRawStats failed: %v", err)
	}
	if rawStats.Count != 100 {
		t.Errorf("Expected raw count 100, got %d", rawStats.Count)
	}
	if rawStats.TotalBytes != 100*50 {
		t.Errorf("Expected raw total bytes %d, got %d", 100*50, rawStats.TotalBytes)
	}

	// Verify GetTDigestStats
	tdStats, err := d.GetTDigestStats()
	if err != nil {
		t.Fatalf("GetTDigestStats failed: %v", err)
	}
	if len(tdStats) != 2 {
		t.Fatalf("Expected 2 tdigest stats (2 windows), got %d", len(tdStats))
	}

	// Find the 60s window stat
	var stat60, stat3600 *TDigestStat
	for i := range tdStats {
		if tdStats[i].WindowSeconds == 60 {
			stat60 = &tdStats[i]
		} else if tdStats[i].WindowSeconds == 3600 {
			stat3600 = &tdStats[i]
		}
	}

	if stat60 == nil {
		t.Fatal("60s window stat not found")
	}
	if stat60.Count != 10 {
		t.Errorf("Expected 60s window count 10, got %d", stat60.Count)
	}
	// Sum of 50+i*10 for i=0..9 = 50*10 + 10*(0+1+2+...+9) = 500 + 10*45 = 950
	if stat60.TotalBytes != 950 {
		t.Errorf("Expected 60s window total bytes 950, got %d", stat60.TotalBytes)
	}

	if stat3600 == nil {
		t.Fatal("3600s window stat not found")
	}
	if stat3600.Count != 5 {
		t.Errorf("Expected 3600s window count 5, got %d", stat3600.Count)
	}
	// Sum of 100+i*20 for i=0..4 = 100*5 + 20*(0+1+2+3+4) = 500 + 20*10 = 700
	if stat3600.TotalBytes != 700 {
		t.Errorf("Expected 3600s window total bytes 700, got %d", stat3600.TotalBytes)
	}
}
