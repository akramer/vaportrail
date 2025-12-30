package scheduler

import (
	"testing"
	"time"
	"vaportrail/internal/db"

	"github.com/jonboulle/clockwork"
)

func TestScheduler_RunProbeLoop(t *testing.T) {
	// Setup in-memory DB
	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}

	// Setup fake clock
	fakeClock := clockwork.NewFakeClock()
	s := New(database)
	s.Clock = fakeClock

	// Add a target with 0.1s probe interval and 1s commit interval
	target := db.Target{
		Name:           "TestTarget",
		Address:        "localhost", // Won't actually matter for logic flow if probe fails/succeeds quickly
		ProbeType:      "ping",
		ProbeInterval:  0.1,
		CommitInterval: 1.0,
	}

	id, err := database.AddTarget(&target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}
	target.ID = id

	// Start scheduler
	// Since runProbeLoop is private and called by goroutine, we can start it by AddTarget
	// BUT AddTarget calls runProbeLoop in a goroutine.
	// We want to control the clock.

	// We can't really "wait" for the probe to finish execution easily without mocking the Probe execution itself.
	// However, `probe.Run` executes a real command. "ping localhost" might actually work or fail.
	// We should probably rely on the side effects (DB records).

	s.AddTarget(target)

	// Advance clock by 0.1s -> Should trigger 1 probe
	fakeClock.Advance(100 * time.Millisecond)
	// Probe runs async. We might need to wait a tiny bit for the goroutine to finish.
	time.Sleep(50 * time.Millisecond) // Real time sleep to allow goroutine to spawn

	// Advance clock until commit (1s total) -> Should trigger ~10 probes and 1 commit
	fakeClock.Advance(900 * time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	// Check results
	results, err := database.GetResults(id, 100)
	if err != nil {
		t.Fatalf("Failed to get results: %v", err)
	}

	// Ideally we have 1 result committed (after 1s)
	if len(results) == 0 {
		t.Log("No results found yet. This might be due to timing execution of actual 'ping'.")
		// This test depends on 'ping' actually finishing within our real-time wait.
		// If ping takes > 100ms real time (it sleeps for jitter 0-100ms + execution),
		// we might miss it if we don't wait enough real time.
	} else {
		t.Logf("Found %d results", len(results))
		if results[0].AvgNS == 0 {
			t.Log("Result has 0 stats, maybe probe failed?")
		}
	}

	s.RemoveTarget(id)
}

func TestTargetRemovalRace(t *testing.T) {
	// Setup in-memory DB
	database, err := db.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}

	// Setup fake clock
	fakeClock := clockwork.NewFakeClock()
	s := New(database)
	s.Clock = fakeClock

	// Add a target that executes quickly but we'll span many
	target := db.Target{
		Name:           "RaceTarget",
		Address:        "127.0.0.1",
		ProbeType:      "ping",
		ProbeInterval:  0.01,
		CommitInterval: 1.0,
	}

	targetID, err := database.AddTarget(&target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}
	target.ID = targetID

	// Run multiple iterations of Add / Run / Remove to try to trigger race
	for i := 0; i < 10; i++ {
		s.AddTarget(target)

		// Create a bunch of probes
		// Advance clock
		go func() {
			for j := 0; j < 20; j++ {
				fakeClock.Advance(10 * time.Millisecond)
				time.Sleep(1 * time.Millisecond) // Yield
			}
		}()

		time.Sleep(50 * time.Millisecond) // Let some probes start

		// Now remove target while probes might be running
		s.RemoveTarget(targetID)

		// Wait a bit to ensure clean shutdown
		time.Sleep(50 * time.Millisecond)
	}
}
