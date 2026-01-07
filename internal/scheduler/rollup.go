package scheduler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
	"vaportrail/internal/db"

	"github.com/caio/go-tdigest/v4"
	"github.com/jonboulle/clockwork"
)

type RetentionPolicy struct {
	Window    int `json:"window"`
	Retention int `json:"retention"`
}

var defaultPolicies = []RetentionPolicy{
	{Window: 0, Retention: 604800},         // Raw: 7 days
	{Window: 60, Retention: 15768000},      // 1m: 6 months
	{Window: 300, Retention: 31536000},     // 5m: 1 year
	{Window: 3600, Retention: 315360000},   // 1h: 10 years
	{Window: 86400, Retention: 3153600000}, // 1d: ~100 years (User didn't specify retention for 1d, assuming long)
}

func ValidateRetentionPolicies(policies []RetentionPolicy) error {
	// Sort policies by window size
	sortPolicies(policies)

	for i, p := range policies {
		if p.Window < 0 {
			return errors.New("retention window cannot be negative")
		}
		if i == 0 {
			if p.Window == 0 {
				continue // 0 (Raw) is valid base
			}
			// Smallest window must be >= 1s (implicit raw is 0, but here checking config)
		} else {
			// Check multiple
			prevWindow := policies[i-1].Window
			if prevWindow == 0 {
				// Raw fits into anything integer (seconds)
			} else {
				if p.Window%prevWindow != 0 {
					return fmt.Errorf("window %d is not a multiple of smaller window %d", p.Window, prevWindow)
				}
			}
		}
	}
	return nil
}

func sortPolicies(policies []RetentionPolicy) {
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].Window < policies[j].Window
	})
}

// DefaultPolicies returns the default retention policies for use at target creation time.
func DefaultPolicies() []RetentionPolicy {
	// Return a copy to prevent mutation
	result := make([]RetentionPolicy, len(defaultPolicies))
	copy(result, defaultPolicies)
	return result
}

// DefaultPoliciesJSON returns the default retention policies as a JSON string.
func DefaultPoliciesJSON() string {
	data, _ := json.Marshal(defaultPolicies)
	return string(data)
}

// ErrNoRetentionPolicies is returned when a target has no retention policies configured.
var ErrNoRetentionPolicies = errors.New("no retention policies configured")

// GetRetentionPolicies parses and returns retention policies for a target.
// Returns ErrNoRetentionPolicies if no policies are configured.
func GetRetentionPolicies(t db.Target) ([]RetentionPolicy, error) {
	if t.RetentionPolicies == "" || t.RetentionPolicies == "[]" {
		return nil, ErrNoRetentionPolicies
	}
	var p []RetentionPolicy
	if err := json.Unmarshal([]byte(t.RetentionPolicies), &p); err != nil {
		return nil, fmt.Errorf("failed to parse retention policies: %w", err)
	}
	if len(p) == 0 {
		return nil, ErrNoRetentionPolicies
	}
	return p, nil
}

type RollupManager struct {
	db    db.Store
	clock clockwork.Clock
	stop  chan struct{}
	wg    sync.WaitGroup
}

func NewRollupManager(database db.Store) *RollupManager {
	return &RollupManager{
		db:    database,
		clock: clockwork.NewRealClock(),
		stop:  make(chan struct{}),
	}
}

func (rm *RollupManager) Start() {
	rm.wg.Add(1)
	go rm.run()
}

func (rm *RollupManager) Stop() {
	close(rm.stop)
	rm.wg.Wait()
}

func (rm *RollupManager) run() {
	defer rm.wg.Done()
	ticker := rm.clock.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-rm.stop:
			return
		case <-ticker.Chan():
			rm.processRollups()
		}
	}
}

func (rm *RollupManager) processRollups() {
	targets, err := rm.db.GetTargets()
	if err != nil {
		log.Printf("RollupManager: Failed to get targets: %v", err)
		return
	}

	for _, t := range targets {
		policies, err := GetRetentionPolicies(t)
		if err != nil {
			// Skip targets with no policies configured
			continue
		}
		// Ensure sorted
		sortPolicies(policies)

		// Map windows to find source
		// 0 -> Raw
		lastWindow := 0

		for _, p := range policies {
			if p.Window == 0 {
				lastWindow = 0
				continue
			}

			// Process this window using lastWindow as source
			rm.processTargetWindow(t, p.Window, lastWindow)
			lastWindow = p.Window
		}
	}
}

func (rm *RollupManager) processTargetWindow(t db.Target, windowSeconds int, sourceWindow int) {
	// 1. Get last rollup time
	lastTime, err := rm.db.GetLastRollupTime(t.ID, windowSeconds)
	if err != nil {
		log.Printf("RollupManager: Failed to get last rollup time for %s (w=%d): %v", t.Name, windowSeconds, err)
		return
	}

	// 2. Determine start time. If never rolled up, start from... when?
	// If zero time, maybe lookup first raw data? Or just start "now" (bad)?
	// Let's look up the first available raw data time IF lastTime is zero.
	// Actually, if it's a new target, lastTime is zero.
	// We can try to catch up from (now - retention) or from first raw data.
	// Let's assume if lastTime is zero, we look for the earliest raw data.
	// But `GetLastRollupTime` returns time.Time{}.

	start := lastTime
	if start.IsZero() {
		// Optimization: Find earliest raw data time.
		earliest, err := rm.db.GetEarliestRawResultTime(t.ID)
		if err != nil {
			log.Printf("RollupManager: Error getting earliest raw time: %v", err)
			return
		}
		if earliest.IsZero() {
			// No raw data? Nothing to roll up.
			return
		}
		// Truncate to window alignment
		start = earliest.Truncate(time.Duration(windowSeconds) * time.Second)
	}

	// Align next window
	// If lastTime was 12:00:00, next window starts 12:00:00 + window (covering [12:00:00 + w, ...))
	// Wait, `lastTime` is the *start* of the last processed window.
	// So next window starts at `lastTime + window`.
	nextWindowStart := start.Add(time.Duration(windowSeconds) * time.Second)

	// If start was simulated (Zero -> 24h ago), we might need to align it strictly.
	if lastTime.IsZero() {
		nextWindowStart = start // Start fresh from that point
	}

	// Safety: don't process future
	// Cutoff logic: Now - (MaxTimeout + CommitBuffer + 1s)
	// MaxTimeout is in t.Timeout (seconds). Buffer is 2s (from Scheduler).
	cutoff := rm.clock.Now().Add(-time.Duration(t.Timeout+3) * time.Second)

	// Collect all aggregated results to commit in a single transaction
	var results []*db.AggregatedResult

	for {
		windowEnd := nextWindowStart.Add(time.Duration(windowSeconds) * time.Second)
		if windowEnd.After(cutoff) {
			break // Caught up
		}

		agg := rm.aggregateWindow(t, windowSeconds, sourceWindow, nextWindowStart, windowEnd)
		if agg != nil {
			results = append(results, agg)
		}
		nextWindowStart = windowEnd
	}

	// Commit all results in a single transaction
	if len(results) > 0 {
		if err := rm.db.AddAggregatedResults(results); err != nil {
			log.Printf("RollupManager: Failed to save batch AggResults for %s (w=%ds): %v", t.Name, windowSeconds, err)
		}
	}
}

func (rm *RollupManager) aggregateWindow(t db.Target, windowSeconds int, sourceWindow int, start, end time.Time) *db.AggregatedResult {
	// Source Data Fetching
	var tDigest *tdigest.TDigest
	var timeoutCount int64
	var rowsProcessed int
	var err error

	if sourceWindow == 0 {
		// Aggregate from Raw
		raws, err := rm.db.GetRawResults(t.ID, start, end, -1)
		if err != nil {
			log.Printf("RollupManager: Error fetching raw results: %v", err)
			return nil
		}
		rowsProcessed = len(raws)
		if len(raws) == 0 {
			return rm.createEmptyRollup(t, windowSeconds, start)
		}

		tDigest, _ = tdigest.New(tdigest.Compression(100))
		for _, r := range raws {
			if r.Latency == -1 {
				timeoutCount++
			} else {
				tDigest.Add(r.Latency)
			}
		}

	} else {
		// Aggregate from Sub-Rollup
		// Fetch aggregated results for the source window that fall within this window
		// start inclusive, end exclusive?
		// Yes, [start, end).
		// Note: The sub-rollups MUST align perfectly if Validated.

		results, err := rm.db.GetAggregatedResults(t.ID, sourceWindow, start, end)
		if err != nil {
			log.Printf("RollupManager: Error fetching aggregated results (w=%d): %v", sourceWindow, err)
			return nil
		}
		rowsProcessed = len(results)
		if len(results) == 0 {
			return rm.createEmptyRollup(t, windowSeconds, start)
		}

		tDigest, _ = tdigest.New(tdigest.Compression(100))
		for _, res := range results {
			timeoutCount += res.TimeoutCount
			if len(res.TDigestData) > 0 {
				subTD, err := db.DeserializeTDigest(res.TDigestData)
				if err == nil {
					tDigest.Merge(subTD)
				}
			}
		}
	}

	tdBytes, err := db.SerializeTDigest(tDigest)
	if err != nil {
		log.Printf("RollupManager: Serialization failed: %v", err)
		return nil
	}

	log.Printf("RollupManager: Aggregated %s (w=%ds): %d rows, %d timeouts", t.Name, windowSeconds, rowsProcessed, timeoutCount)

	return &db.AggregatedResult{
		Time:          start,
		TargetID:      t.ID,
		WindowSeconds: windowSeconds,
		TDigestData:   tdBytes,
		TimeoutCount:  timeoutCount,
	}
}

func (rm *RollupManager) createEmptyRollup(t db.Target, windowSeconds int, start time.Time) *db.AggregatedResult {
	td, _ := tdigest.New(tdigest.Compression(100))
	tdBytes, _ := db.SerializeTDigest(td)
	return &db.AggregatedResult{
		Time:          start,
		TargetID:      t.ID,
		WindowSeconds: windowSeconds,
		TDigestData:   tdBytes,
		TimeoutCount:  0,
	}
}
