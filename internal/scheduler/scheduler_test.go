package scheduler

import (
	"fmt"
	"sync"
	"testing"
	"time"
	"vaportrail/internal/db"
	"vaportrail/internal/probe"

	"github.com/jonboulle/clockwork"
)

func TestScheduler_RunProbeLoop_WithMocks(t *testing.T) {
	// Setup Mock DB
	mockDB := NewMockStore()

	// Setup fake clock
	fakeClock := clockwork.NewFakeClock()
	s := New(mockDB)
	s.Clock = fakeClock

	// Setup Mock Runner
	mockRunner := &MockRunner{
		RunFn: func(cfg probe.Config) (float64, error) {
			return 500.0, nil // Return 500ns
		},
	}
	s.probeRunner = mockRunner

	// Add a target
	target := db.Target{
		Name:           "MockTarget",
		Address:        "example.com",
		ProbeType:      "http",
		ProbeInterval:  0.1, // 100ms
		CommitInterval: 1.0, // 1s
	}

	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	s.AddTarget(target)

	// Step-wise advance to ensure goroutines get scheduled

	// Advance 1.2s total, in 100ms increments
	for i := 0; i < 15; i++ {
		fakeClock.Advance(100 * time.Millisecond)
		time.Sleep(20 * time.Millisecond) // Yield to runtime
	}

	// We expect the mockDB to have received a Result
	// Poll for result
	var results []db.Result
	for i := 0; i < 5; i++ {
		results, _ = mockDB.GetResults(id, 100)
		if len(results) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(results) == 0 {
		t.Fatal("Expected results to be committed, but found none")
	}

	// Verify the data
	r := results[0]
	if r.ProbeCount == 0 {
		t.Errorf("Expected ProbeCount > 0, got %d", r.ProbeCount)
	}

	td, err := db.DeserializeTDigest(r.TDigestData)
	if err != nil {
		t.Fatalf("Failed to deserialize tdigest: %v", err)
	}
	minVal := td.Quantile(0)
	maxVal := td.Quantile(1)

	if minVal != 500.0 || maxVal != 500.0 {
		t.Errorf("Expected Min/Max 500, got Min=%v Max=%v", minVal, maxVal)
	}

	s.RemoveTarget(id)
}

func TestTargetRemovalRace_WithMocks(t *testing.T) {
	mockDB := NewMockStore()
	fakeClock := clockwork.NewFakeClock()
	s := New(mockDB)
	s.Clock = fakeClock

	// Mock that takes a bit of time
	s.probeRunner = &MockRunner{
		RunFn: func(cfg probe.Config) (float64, error) {
			time.Sleep(1 * time.Millisecond)
			return 100, nil
		},
	}

	target := db.Target{
		Name:           "RaceTarget",
		Address:        "127.0.0.1",
		ProbeType:      "ping",
		ProbeInterval:  0.01,
		CommitInterval: 1.0,
	}
	id, _ := mockDB.AddTarget(&target)
	target.ID = id

	// Run multiple iterations of Add / Run / Remove
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		s.AddTarget(target)

		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				fakeClock.Advance(15 * time.Millisecond)
				time.Sleep(1 * time.Millisecond)
			}
		}()

		time.Sleep(5 * time.Millisecond)
		s.RemoveTarget(id)
		wg.Wait()
	}
}

func TestScheduler_TimeoutLogic(t *testing.T) {
	mockDB := NewMockStore()
	fakeClock := clockwork.NewFakeClock()
	s := New(mockDB)
	s.Clock = fakeClock

	// Setup Mock Runner to simulate timeout
	// In probe.go we return: fmt.Errorf("probe timed out after %v", cfg.Timeout)
	mockRunner := &MockRunner{
		RunFn: func(cfg probe.Config) (float64, error) {
			// Simulate timeout logic
			// Since we aren't actually running exec.CommandContext under the hood in the mock,
			// we just return the error string that the scheduler looks for.
			return 0, fmt.Errorf("probe timed out after %v", cfg.Timeout)
		},
	}
	s.probeRunner = mockRunner

	target := db.Target{
		Name:           "TimeoutTarget",
		Address:        "timeout.com",
		ProbeType:      "http",
		ProbeInterval:  0.1,  // 100ms
		CommitInterval: 1.0,  // 1s
		Timeout:        0.05, // 50ms (irrelevant for mock but good for consistency)
	}

	id, _ := mockDB.AddTarget(&target)
	target.ID = id
	s.AddTarget(target)

	// Advance clock to trigger probes
	// 1s commit interval, 0.1s probe interval -> ~10 probes
	for i := 0; i < 15; i++ {
		fakeClock.Advance(100 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}

	// Verify results
	var results []db.Result
	for i := 0; i < 5; i++ {
		results, _ = mockDB.GetResults(id, 100)
		if len(results) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(results) == 0 {
		t.Fatal("Expected results, got none")
	}

	r := results[0]
	// All probes should have failed with timeout
	// ProbeCount should be 0 because we only count successes in the aggregator for stats
	// But actually, wait. In scheduler.go:
	/*
		if val == -1.0 {
			timeoutCount++
			continue
		}
		count++
	*/
	// So ProbeCount (which comes from count) will be 0. TimeoutCount should be > 0.

	if r.TimeoutCount == 0 {
		t.Errorf("Expected TimeoutCount > 0, got %d", r.TimeoutCount)
	}

	if r.ProbeCount != 0 {
		t.Errorf("Expected ProbeCount == 0 for all timeouts, got %d", r.ProbeCount)
	}

	s.RemoveTarget(id)
}
