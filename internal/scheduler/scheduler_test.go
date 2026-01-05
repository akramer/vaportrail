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
	s.Start()

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

	// Advance 2.5s total, in 100ms increments (Batch writer flushes every 2s)
	for i := 0; i < 25; i++ {
		fakeClock.Advance(100 * time.Millisecond)
		time.Sleep(20 * time.Millisecond) // Yield to runtime
	}

	// We expect the mockDB to have received a Result
	// Poll for result
	// We expect the mockDB to have received RawResults
	// Poll for results
	var results []db.RawResult
	for i := 0; i < 5; i++ {
		// Use a wide time range to catch everything
		results, _ = mockDB.GetRawResults(id, time.Time{}, time.Now().Add(24*time.Hour), 1000)
		if len(results) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(results) == 0 {
		t.Fatal("Expected raw results to be committed, but found none")
	}

	// Verify the data
	r := results[0]
	if r.Latency != 500.0 {
		t.Errorf("Expected Latency 500.0, got %v", r.Latency)
	}

	s.RemoveTarget(id)
}

func TestTargetRemovalRace_WithMocks(t *testing.T) {
	mockDB := NewMockStore()
	fakeClock := clockwork.NewFakeClock()
	s := New(mockDB)
	s.Clock = fakeClock
	s.Start()

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
	s.Start()

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
	// Advance > 2s for batch flush
	for i := 0; i < 25; i++ {
		fakeClock.Advance(100 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
	}

	// Verify results
	var results []db.RawResult
	for i := 0; i < 5; i++ {
		results, _ = mockDB.GetRawResults(id, time.Time{}, time.Now().Add(24*time.Hour), 1000)
		if len(results) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(results) == 0 {
		t.Fatal("Expected raw results, got none")
	}

	r := results[0]
	// All probes should have failed with timeout -> Latency -1
	if r.Latency != -1.0 {
		t.Errorf("Expected Latency -1.0 (timeout), got %f", r.Latency)
	}

	s.RemoveTarget(id)
}
