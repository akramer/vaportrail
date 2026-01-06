package db

import (
	"testing"
	"time"
)

func TestDeleteAggregatedResultsByWindow(t *testing.T) {
	// Create in-memory DB
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}
	defer db.Close()

	// Create a target
	target := &Target{
		Name:              "TestTarget",
		Address:           "http://example.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window":0,"retention":604800},{"window":60,"retention":15768000},{"window":300,"retention":31536000}]`,
	}
	targetID, err := db.AddTarget(target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}

	baseTime := time.Now().UTC().Truncate(time.Second)

	// Add aggregated results for different windows
	// Window 60
	db.AddAggregatedResult(&AggregatedResult{
		Time:          baseTime,
		TargetID:      targetID,
		WindowSeconds: 60,
		TDigestData:   []byte{1, 2, 3},
		TimeoutCount:  0,
	})
	db.AddAggregatedResult(&AggregatedResult{
		Time:          baseTime.Add(-60 * time.Second),
		TargetID:      targetID,
		WindowSeconds: 60,
		TDigestData:   []byte{1, 2, 3},
		TimeoutCount:  0,
	})

	// Window 300
	db.AddAggregatedResult(&AggregatedResult{
		Time:          baseTime,
		TargetID:      targetID,
		WindowSeconds: 300,
		TDigestData:   []byte{4, 5, 6},
		TimeoutCount:  0,
	})

	// Verify we have data for both windows
	results60, err := db.GetAggregatedResults(targetID, 60, baseTime.Add(-1*time.Hour), baseTime.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("Failed to get aggregated results for window 60: %v", err)
	}
	if len(results60) != 2 {
		t.Errorf("Expected 2 results for window 60, got %d", len(results60))
	}

	results300, err := db.GetAggregatedResults(targetID, 300, baseTime.Add(-1*time.Hour), baseTime.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("Failed to get aggregated results for window 300: %v", err)
	}
	if len(results300) != 1 {
		t.Errorf("Expected 1 result for window 300, got %d", len(results300))
	}

	// Delete all results for window 60
	err = db.DeleteAggregatedResultsByWindow(targetID, 60)
	if err != nil {
		t.Fatalf("DeleteAggregatedResultsByWindow failed: %v", err)
	}

	// Verify window 60 data is gone
	results60After, err := db.GetAggregatedResults(targetID, 60, baseTime.Add(-1*time.Hour), baseTime.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("Failed to get aggregated results after deletion: %v", err)
	}
	if len(results60After) != 0 {
		t.Errorf("Expected 0 results for window 60 after deletion, got %d", len(results60After))
	}

	// Verify window 300 data is still there
	results300After, err := db.GetAggregatedResults(targetID, 300, baseTime.Add(-1*time.Hour), baseTime.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("Failed to get aggregated results for window 300 after deletion: %v", err)
	}
	if len(results300After) != 1 {
		t.Errorf("Expected 1 result for window 300 (should be unaffected), got %d", len(results300After))
	}
}
