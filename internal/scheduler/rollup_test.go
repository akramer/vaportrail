package scheduler

import (
	"testing"
	"time"
	"vaportrail/internal/db"

	"github.com/jonboulle/clockwork"
)

func TestRollupManager(t *testing.T) {
	mockDB := NewMockStore()
	rm := NewRollupManager(mockDB)
	fakeClock := clockwork.NewFakeClock()
	rm.clock = fakeClock

	// Setup a target with 60s rollup
	target := db.Target{
		Name:              "RollupTarget",
		Address:           "start.pcom",
		ProbeType:         "http",
		ProbeInterval:     1.0,
		Timeout:           1.0,
		RetentionPolicies: `[{"window": 60, "retention": 3600}]`, // 1m rollup, 1h retention
	}
	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	// Start RollupManager
	rm.Start()
	defer rm.Stop()

	// Start time: Now
	startTime := fakeClock.Now().Truncate(time.Minute)

	// 1. Pre-seed a "last rollup" to avoid 24h backfill
	// We say the previous minute (T-60s) was done.
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time:          startTime.Add(-60 * time.Second),
		TargetID:      id,
		WindowSeconds: 60,
	})

	// 2. Insert Raw Data for the first minute (00:00:00 to 00:01:00)

	// Insert 60 points of data (1 per second)
	for i := 0; i < 60; i++ {
		pointTime := startTime.Add(time.Duration(i) * time.Second)
		mockDB.AddRawResults([]db.RawResult{{
			Time:     pointTime,
			TargetID: id,
			Latency:  100.0, // Constant latency for easy verification
		}})
	}

	// Advance clock past window + buffer (Window 60s + Timeout 1s + Buffer 2s = 63s)
	// We need to advance enough to trigger the ticker (10s) and pass the cutoff
	// Wait for goroutine to initialize ticker
	time.Sleep(10 * time.Millisecond)
	fakeClock.Advance(70 * time.Second)

	// Wait for goroutine to process
	time.Sleep(500 * time.Millisecond)

	// Verify we have 1 aggregated result for window=60
	results, _ := mockDB.GetAggregatedResults(id, 60, startTime, startTime.Add(2*time.Minute))
	if len(results) != 1 {
		t.Fatalf("Expected 1 aggregated result, got %d", len(results))
	}

	agg := results[0]
	if !agg.Time.Equal(startTime) {
		t.Errorf("Expected AggTime %v, got %v", startTime, agg.Time)
	}

	// Verify TDigest content (min/max/quantile)
	td, _ := db.DeserializeTDigest(agg.TDigestData)
	if td.Quantile(0.5) != 100.0 {
		t.Errorf("Expected Median 100.0, got %v", td.Quantile(0.5))
	}
}

func TestRollupManager_CatchUp(t *testing.T) {
	mockDB := NewMockStore()
	rm := NewRollupManager(mockDB)
	fakeClock := clockwork.NewFakeClock()
	rm.clock = fakeClock

	target := db.Target{
		Name:              "CatchUpTarget",
		Address:           "catch.up",
		ProbeType:         "http",
		Timeout:           1.0,
		RetentionPolicies: `[{"window": 60, "retention": 3600}]`,
	}
	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	rm.Start()
	defer rm.Stop()

	startTime := fakeClock.Now().Truncate(time.Minute)

	// Pre-seed prior rollup to avoid backfill
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time:          startTime.Add(-60 * time.Second),
		TargetID:      id,
		WindowSeconds: 60,
	})

	// Simulate data for 3 minutes
	for m := 0; m < 3; m++ {
		baseTime := startTime.Add(time.Duration(m) * time.Minute)
		// Add one point per minute to keep it simple
		mockDB.AddRawResults([]db.RawResult{{
			Time:     baseTime,
			TargetID: id,
			Latency:  float64(m + 1), // 1.0, 2.0, 3.0
		}})
	}

	// Advance clock by 5 minutes (well past the 3 minutes of data)
	// This should trigger catch-up for all 3 windows
	time.Sleep(10 * time.Millisecond)
	fakeClock.Advance(5 * time.Minute)
	time.Sleep(500 * time.Millisecond)

	// Fetch only the 3 minutes we care about (T0, T1, T2)
	results, _ := mockDB.GetAggregatedResults(id, 60, startTime, startTime.Add(3*time.Minute))
	if len(results) != 3 {
		t.Fatalf("Expected 3 aggregated results, got %d", len(results))
	}

	for i, res := range results {
		expectedTime := startTime.Add(time.Duration(i) * time.Minute)
		if !res.Time.Equal(expectedTime) {
			t.Errorf("Result %d: Expected Time %v, got %v", i, expectedTime, res.Time)
		}

		td, _ := db.DeserializeTDigest(res.TDigestData)
		if td.Quantile(0.5) != float64(i+1) {
			t.Errorf("Result %d: Expected Value %v, got %v", i, float64(i+1), td.Quantile(0.5))
		}
	}
}

func TestRollupManager_Cascading(t *testing.T) {
	mockDB := NewMockStore()
	rm := NewRollupManager(mockDB)
	fakeClock := clockwork.NewFakeClock()
	rm.clock = fakeClock

	// Setup target with cascading policies: 10s -> 60s
	target := db.Target{
		Name:              "CascadingTarget",
		Address:           "cascade.pcom",
		ProbeType:         "http",
		Timeout:           1.0,
		RetentionPolicies: `[{"window": 10, "retention": 3600}, {"window": 60, "retention": 3600}]`,
	}
	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	rm.Start()
	defer rm.Stop()

	startTime := fakeClock.Now().Truncate(time.Minute)

	// Seed previous 60s window (and 10s windows) to avoid backfill
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time:          startTime.Add(-60 * time.Second),
		TargetID:      id,
		WindowSeconds: 60,
	})
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time:          startTime.Add(-10 * time.Second),
		TargetID:      id,
		WindowSeconds: 10,
	})

	// Add Raw Data for 1 full minute (60 points)
	for i := 0; i < 60; i++ {
		pointTime := startTime.Add(time.Duration(i) * time.Second)
		mockDB.AddRawResults([]db.RawResult{{
			Time:     pointTime,
			TargetID: id,
			Latency:  100.0,
		}})
	}

	// 1. Advance clock to trigger 10s rollups
	time.Sleep(10 * time.Millisecond)
	fakeClock.Advance(70 * time.Second) // Pass 1m + buffer
	time.Sleep(500 * time.Millisecond)

	// Verify 10s rollups exist (should be 6 of them)
	results10s, _ := mockDB.GetAggregatedResults(id, 10, startTime, startTime.Add(60*time.Second))
	if len(results10s) != 6 {
		t.Fatalf("Expected 6 10s rollups, got %d", len(results10s))
	}

	// 2. DELETE RAW DATA to force cascading
	mockDB.RawResults[id] = nil

	// 3. Clear 60s results if any were seemingly created (should check)
	// Actually, if we advanced 70s, the 60s window (ending at +60s) SHOULD have been processed.
	// But let's check if it was processed from Raw.
	// The implementation iterates sorted policies: 10s, then 60s.
	// When processing 60s window (0:00-1:00), it uses 10s (0:00-1:00) as source.
	// Wait, did the 10s rollups finish BEFORE the 60s rollup started?
	// The loop iterates policies sequentially. So 10s loop runs first.
	// 10s loop processes 0:00-0:10, 0:10-0:20... until caught up.
	// Then 60s loop runs. It looks for data for 0:00-1:00.
	// It uses sourceWindow=10.
	// So it should successfully aggregate from the just-created 10s rollups!

	results60s, _ := mockDB.GetAggregatedResults(id, 60, startTime, startTime.Add(60*time.Second))
	if len(results60s) != 1 {
		t.Fatalf("Expected 1 60s rollup, got %d", len(results60s))
	}

	// Verify content
	td, _ := db.DeserializeTDigest(results60s[0].TDigestData)
	if td.Count() != 60 { // Should include all points
		// Wait, count might be count of sub-digests if merge logic isn't perfect?
		// No, Merge adds weights. TDigest count should be sum of weights.
		t.Errorf("Expected Count 60, got %v", td.Count())
	}
	if td.Quantile(0.5) != 100.0 {
		t.Errorf("Expected Median 100.0, got %v", td.Quantile(0.5))
	}
}
