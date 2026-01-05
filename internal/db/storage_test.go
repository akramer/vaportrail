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
