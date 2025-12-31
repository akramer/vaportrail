package scheduler

import (
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
	if r.MinNS != 500 || r.MaxNS != 500 {
		t.Errorf("Expected Min/Max 500, got Min=%d Max=%d", r.MinNS, r.MaxNS)
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
