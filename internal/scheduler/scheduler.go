package scheduler

import (
	"log"
	"math"
	"strings"
	"sync"
	"time"
	"vaportrail/internal/db"
	"vaportrail/internal/probe"

	"github.com/caio/go-tdigest/v4"
	"github.com/jonboulle/clockwork"
)

type Scheduler struct {
	db          db.Store
	probeRunner probe.Runner

	mu        sync.Mutex
	stopChans map[int64]chan struct{}
	Clock     clockwork.Clock
}

func New(database db.Store) *Scheduler {
	return &Scheduler{
		db:          database,
		probeRunner: probe.RealRunner{},
		stopChans:   make(map[int64]chan struct{}),
		Clock:       clockwork.NewRealClock(),
	}
}

func (s *Scheduler) Start() error {
	targets, err := s.db.GetTargets()
	if err != nil {
		return err
	}

	log.Printf("Starting scheduler with %d targets", len(targets))
	for _, t := range targets {
		s.AddTarget(t)
	}
	return nil
}

func (s *Scheduler) AddTarget(t db.Target) {
	s.mu.Lock()
	if _, exists := s.stopChans[t.ID]; exists {
		s.mu.Unlock()
		return // Already running
	}
	stopCh := make(chan struct{})
	s.stopChans[t.ID] = stopCh
	s.mu.Unlock()

	log.Printf("Scheduler: Adding new target %s", t.Name)
	go s.runProbeLoop(t, stopCh)
}

func (s *Scheduler) RemoveTarget(id int64) {
	s.mu.Lock()
	if ch, exists := s.stopChans[id]; exists {
		close(ch)
		delete(s.stopChans, id)
		log.Printf("Scheduler: Removed target %d", id)
	}
	s.mu.Unlock()
}

func (s *Scheduler) runProbeLoop(t db.Target, stopCh chan struct{}) {
	cfg, err := probe.GetConfig(t.ProbeType, t.Address)
	if err != nil {
		log.Printf("Failed to get config for target %s: %v", t.Name, err)
		return
	}

	// Default interval 1s
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.CommitInterval <= 0 {
		t.CommitInterval = 60.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	cfg.Timeout = time.Duration(t.Timeout*1000) * time.Millisecond

	probeTicker := s.Clock.NewTicker(time.Duration(t.ProbeInterval*1000) * time.Millisecond)
	commitTicker := s.Clock.NewTicker(time.Duration(t.CommitInterval*1000) * time.Millisecond)
	defer probeTicker.Stop()
	defer commitTicker.Stop()

	// Concurrency limiter: ensure no more than 5 probes overlap for this target
	sem := make(chan struct{}, 5)

	// Results channel from probes
	resultsChan := make(chan float64, 100)

	var wg sync.WaitGroup

	// Aggregator state
	var (
		count        int64
		timeoutCount int64
		sum          float64
		sqSum        float64
		td, _        = tdigest.New(tdigest.Compression(100))
	)

	// Start Aggregation Loop
	go func() {
		for {
			select {
			case val, ok := <-resultsChan:
				if !ok {
					return
				}
				if val == -1.0 {
					timeoutCount++
					continue
				}
				count++
				sum += val
				sqSum += val * val
				td.Add(val)

			case <-commitTicker.Chan():
				if count == 0 && timeoutCount == 0 {
					continue
				}

				// Make a copy or calculate stats
				var avg, stdDev, variance float64
				if count > 0 {
					avg = sum / float64(count)
					variance = (sqSum / float64(count)) - (avg * avg)
					if variance < 0 {
						variance = 0
					}
					stdDev = math.Sqrt(variance)
				}

				tdData, err := db.SerializeTDigest(td)
				if err != nil {
					log.Printf("Failed to serialize tdigest for %s: %v", t.Name, err)
					continue
				}

				dbRes := &db.Result{
					Time:         s.Clock.Now().UTC(),
					TargetID:     t.ID,
					StdDevNS:     stdDev,
					SumSqNS:      sqSum,
					TimeoutCount: timeoutCount,
					TDigestData:  tdData,
				}

				if err := s.db.AddResult(dbRes); err != nil {
					log.Printf("Failed to save result for %s: %v", t.Name, err)
				} else {
					log.Printf("Saved result for %s (count=%d, timeouts=%d)", t.Name, count, timeoutCount)
				}

				// Reset stats
				count = 0
				timeoutCount = 0
				sum = 0
				sqSum = 0
				td, _ = tdigest.New(tdigest.Compression(100))
			}
		}
	}()

	runProbe := func() {
		select {
		case sem <- struct{}{}:
			wg.Add(1)
			// Acquired semaphore
			go func() {
				defer wg.Done()
				defer func() { <-sem }() // Release
				res, err := s.probeRunner.Run(cfg)
				if err != nil {
					if strings.Contains(err.Error(), "probe timed out") {
						// We treat timeout as a result passed to aggregation loop (as 0 or special signal?)
						// Actually, better to send a special value or separate channel?
						// Let's use -1.0 to signal timeout in resultsChan to keep it simple for now,
						// or just handle it here?
						// Since aggregation loop is separate, we need to send it there.
						// Let's send -1.0.
						resultsChan <- -1.0
						return
					}
					log.Printf("Probe failed for %s: %v", t.Name, err)
					return
				}
				resultsChan <- res
			}()
		default:
			log.Printf("Skipping probe for %s due to overlapping limit", t.Name)
		}
	}

	for {
		select {
		case <-stopCh:
			wg.Wait()
			close(resultsChan)
			return
		case <-probeTicker.Chan():
			runProbe()
		}
	}
}
