package scheduler

import (
	"encoding/json"
	"log"
	"sync"
	"time"
	"vaportrail/internal/db"

	"github.com/jonboulle/clockwork"
)

type RetentionManager struct {
	db    db.Store
	clock clockwork.Clock
	stop  chan struct{}
	wg    sync.WaitGroup
}

func NewRetentionManager(database db.Store) *RetentionManager {
	return &RetentionManager{
		db:    database,
		clock: clockwork.NewRealClock(),
		stop:  make(chan struct{}),
	}
}

func (rm *RetentionManager) Start() {
	rm.wg.Add(1)
	go rm.run()
}

func (rm *RetentionManager) Stop() {
	close(rm.stop)
	rm.wg.Wait()
}

func (rm *RetentionManager) run() {
	defer rm.wg.Done()
	// Run retention checks every hour
	ticker := rm.clock.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// Initial run
	rm.enforceRetention()

	for {
		select {
		case <-rm.stop:
			return
		case <-ticker.Chan():
			rm.enforceRetention()
		}
	}
}

func (rm *RetentionManager) enforceRetention() {
	targets, err := rm.db.GetTargets()
	if err != nil {
		log.Printf("RetentionManager: Failed to get targets: %v", err)
		return
	}

	for _, t := range targets {
		policies := defaultPolicies
		if t.RetentionPolicies != "" && t.RetentionPolicies != "[]" {
			var p []RetentionPolicy
			if err := json.Unmarshal([]byte(t.RetentionPolicies), &p); err == nil && len(p) > 0 {
				policies = p
			}
		}

		for _, p := range policies {
			cutoff := rm.clock.Now().Add(-time.Duration(p.Retention) * time.Second)

			if p.Window == 0 {
				// Raw data retention
				// We need a DeleteRawResults method in DB
				if err := rm.db.DeleteRawResultsBefore(t.ID, cutoff); err != nil {
					log.Printf("RetentionManager: Failed to delete raw results for %s: %v", t.Name, err)
				}
			} else {
				// Aggregated data retention
				// We need a DeleteAggregatedResultsBefore method
				if err := rm.db.DeleteAggregatedResultsBefore(t.ID, p.Window, cutoff); err != nil {
					log.Printf("RetentionManager: Failed to delete aggregated results (w=%d) for %s: %v", p.Window, t.Name, err)
				}
			}
		}
	}
}
