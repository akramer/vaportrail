package scheduler

import (
	"log"
	"strings"
	"sync"
	"time"
	"vaportrail/internal/db"
	"vaportrail/internal/probe"

	"github.com/jonboulle/clockwork"
)

type Scheduler struct {
	db          db.Store
	probeRunner probe.Runner

	mu            sync.Mutex
	stopChans     map[int64]chan struct{}
	stopped       bool
	probeWG       sync.WaitGroup
	Clock         clockwork.Clock
	rawResultChan chan db.RawResult
	batchStopChan chan struct{}
	batchWG       sync.WaitGroup
	stopOnce      sync.Once

	rollupManager    *RollupManager
	retentionManager *RetentionManager
}

func New(database db.Store) *Scheduler {
	return &Scheduler{
		db:               database,
		probeRunner:      probe.RealRunner{},
		stopChans:        make(map[int64]chan struct{}),
		Clock:            clockwork.NewRealClock(),
		rawResultChan:    make(chan db.RawResult, 1000), // Buffer size 1000
		batchStopChan:    make(chan struct{}),
		rollupManager:    NewRollupManager(database),
		retentionManager: NewRetentionManager(database),
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

	s.batchWG.Add(1)
	go s.runBatchWriter()
	s.rollupManager.Start()
	s.retentionManager.Start()

	return nil
}

func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		for id, ch := range s.stopChans {
			close(ch)
			delete(s.stopChans, id)
		}
		s.mu.Unlock()

		s.probeWG.Wait()
		close(s.batchStopChan)
		s.batchWG.Wait()
		s.rollupManager.Stop()
		s.retentionManager.Stop()
	})
}

func (s *Scheduler) runBatchWriter() {
	defer s.batchWG.Done()
	ticker := s.Clock.NewTicker(2 * time.Second) // Flush every 2 seconds
	defer ticker.Stop()

	var buffer []db.RawResult

	flush := func() {
		if len(buffer) == 0 {
			return
		}
		if err := s.db.AddRawResults(buffer); err != nil {
			log.Printf("Failed to flush raw results: %v", err)
		} else {
			// log.Printf("Flushed %d raw results", len(buffer))
		}
		buffer = buffer[:0] // Reset buffer (reuse existing slice)
	}

	for {
		select {
		case res := <-s.rawResultChan:
			buffer = append(buffer, res)
			if len(buffer) >= 500 { // Max batch size
				flush()
			}
		case <-ticker.Chan():
			flush()
		case <-s.batchStopChan:
			for {
				select {
				case res := <-s.rawResultChan:
					buffer = append(buffer, res)
				default:
					flush()
					return
				}
			}
		}
	}
}

func (s *Scheduler) AddTarget(t db.Target) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, exists := s.stopChans[t.ID]; exists {
		s.mu.Unlock()
		return // Already running
	}
	stopCh := make(chan struct{})
	s.stopChans[t.ID] = stopCh
	s.probeWG.Add(1)
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
	defer s.probeWG.Done()

	cfg, err := probe.GetConfig(t.ProbeType, t.Address)
	if err != nil {
		log.Printf("Failed to get config for target %s: %v", t.Name, err)
		return
	}

	// Default interval 1s
	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}
	cfg.Timeout = time.Duration(t.Timeout*1000) * time.Millisecond

	probeTicker := s.Clock.NewTicker(time.Duration(t.ProbeInterval*1000) * time.Millisecond)
	// No aggregation loop here anymore.

	// Concurrency limiter: ensure no more than 5 probes overlap for this target
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	runProbe := func() {
		select {
		case sem <- struct{}{}:
			wg.Add(1)
			// Acquired semaphore
			go func() {
				defer wg.Done()
				defer func() { <-sem }() // Release

				startTime := s.Clock.Now().UTC()
				res, err := s.probeRunner.Run(cfg)

				raw := db.RawResult{
					Time:     startTime,
					TargetID: t.ID,
					Latency:  res,
				}

				if err != nil {
					if strings.Contains(err.Error(), "probe timed out") {
						raw.Latency = -1.0
						s.rawResultChan <- raw
						return
					}
					log.Printf("Probe failed for %s: %v", t.Name, err)
					return
				}
				s.rawResultChan <- raw
			}()
		default:
			log.Printf("Skipping probe for %s due to overlapping limit", t.Name)
		}
	}

	for {
		select {
		case <-stopCh:
			wg.Wait()
			return
		case <-probeTicker.Chan():
			runProbe()
		}
	}
}
