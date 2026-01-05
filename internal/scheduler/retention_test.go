package scheduler

import (
	"testing"
	"time"
	"vaportrail/internal/db"

	"github.com/jonboulle/clockwork"
)

func TestRetentionManager(t *testing.T) {
	mockDB := NewMockStore()
	rm := NewRetentionManager(mockDB)
	fakeClock := clockwork.NewFakeClock()
	rm.clock = fakeClock

	// Setup target with short retention policies for testing
	// Raw: 10s retention
	// Window 60: 20s retention
	target := db.Target{
		Name:      "RetentionTarget",
		ProbeType: "http",
		RetentionPolicies: `[
			{"window": 0, "retention": 10},
			{"window": 60, "retention": 20}
		]`,
	}
	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	rm.Start()
	defer rm.Stop()

	baseTime := fakeClock.Now()

	// 1. Insert Data
	// Raw data at T-30s (Should be deleted)
	// Raw data at T-5s (Should be kept)
	mockDB.AddRawResults([]db.RawResult{
		{Time: baseTime.Add(-30 * time.Second), TargetID: id, Latency: 100},
		{Time: baseTime.Add(-5 * time.Second), TargetID: id, Latency: 200},
	})

	// Aggr data at T-30s (Should be deleted, retention 20s)
	// Aggr data at T-10s (Should be kept)
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time: baseTime.Add(-30 * time.Second), TargetID: id, WindowSeconds: 60,
	})
	mockDB.AddAggregatedResult(&db.AggregatedResult{
		Time: baseTime.Add(-10 * time.Second), TargetID: id, WindowSeconds: 60,
	})

	// 2. Trigger Retention
	// We rely on rm.Start() calling enforceRetention() immediately.
	// We just wait a bit to ensure the goroutine has run.
	time.Sleep(100 * time.Millisecond)

	// 3. Verify Raw Results
	raws, _ := mockDB.GetRawResults(id, baseTime.Add(-100*time.Second), baseTime.Add(1*time.Hour), -1)
	if len(raws) != 1 {
		t.Fatalf("Expected 1 raw result kept, got %d", len(raws))
	}
	if raws[0].Latency != 200 {
		t.Errorf("Expected latecy 200 (T-5s) to be kept, got %v", raws[0].Latency)
	}

	// 4. Verify Aggregated Results
	aggs, _ := mockDB.GetAggregatedResults(id, 60, baseTime.Add(-100*time.Second), baseTime.Add(1*time.Hour))
	if len(aggs) != 1 {
		t.Fatalf("Expected 1 aggregated result kept, got %d", len(aggs))
	}
	if !aggs[0].Time.Equal(baseTime.Add(-10 * time.Second)) {
		t.Errorf("Expected T-10s agg to be kept, got %v", aggs[0].Time)
	}
}
