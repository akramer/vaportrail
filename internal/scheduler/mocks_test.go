package scheduler

import (
	"errors"
	"time"
	"vaportrail/internal/db"
	"vaportrail/internal/probe"
)

// MockStore implements db.Store for testing
type MockStore struct {
	Targets           map[int64]db.Target
	Results           map[int64][]db.Result // Legacy
	RawResults        map[int64][]db.RawResult
	AggregatedResults map[int64][]db.AggregatedResult

	AddTargetFn    func(t *db.Target) (int64, error)
	GetTargetsFn   func() ([]db.Target, error)
	AddResultFn    func(r *db.Result) error
	DeleteTargetFn func(id int64) error
	CloseFn        func() error
}

func NewMockStore() *MockStore {
	return &MockStore{
		Targets:           make(map[int64]db.Target),
		Results:           make(map[int64][]db.Result),
		RawResults:        make(map[int64][]db.RawResult),
		AggregatedResults: make(map[int64][]db.AggregatedResult),
	}
}

func (m *MockStore) AddTarget(t *db.Target) (int64, error) {
	if m.AddTargetFn != nil {
		return m.AddTargetFn(t)
	}
	id := int64(len(m.Targets) + 1)
	t.ID = id
	m.Targets[id] = *t
	return id, nil
}

func (m *MockStore) UpdateTarget(t *db.Target) error {
	if _, ok := m.Targets[t.ID]; !ok {
		return errors.New("target not found")
	}
	m.Targets[t.ID] = *t
	return nil
}

func (m *MockStore) GetTargets() ([]db.Target, error) {
	if m.GetTargetsFn != nil {
		return m.GetTargetsFn()
	}
	var targets []db.Target
	for _, t := range m.Targets {
		targets = append(targets, t)
	}
	return targets, nil
}

func (m *MockStore) DeleteTarget(id int64) error {
	if m.DeleteTargetFn != nil {
		return m.DeleteTargetFn(id)
	}
	delete(m.Targets, id)
	return nil
}

func (m *MockStore) GetTarget(id int64) (*db.Target, error) {
	if t, ok := m.Targets[id]; ok {
		return &t, nil
	}
	return nil, errors.New("target not found")
}

func (m *MockStore) AddResult(r *db.Result) error {
	if m.AddResultFn != nil {
		return m.AddResultFn(r)
	}
	m.Results[r.TargetID] = append(m.Results[r.TargetID], *r)
	return nil
}

func (m *MockStore) GetResults(targetID int64, limit int) ([]db.Result, error) {
	res := m.Results[targetID]
	if len(res) > limit {
		res = res[len(res)-limit:]
	}
	return res, nil
}

func (m *MockStore) GetResultsByTime(targetID int64, start, end time.Time) ([]db.Result, error) {
	var res []db.Result
	for _, r := range m.Results[targetID] {
		if (r.Time.After(start) || r.Time.Equal(start)) && (r.Time.Before(end) || r.Time.Equal(end)) {
			res = append(res, r)
		}
	}
	return res, nil
}

func (m *MockStore) Close() error {
	if m.CloseFn != nil {
		return m.CloseFn()
	}
	return nil
}

func (m *MockStore) AddRawResults(results []db.RawResult) error {
	for _, r := range results {
		m.RawResults[r.TargetID] = append(m.RawResults[r.TargetID], r)
	}
	return nil
}

func (m *MockStore) AddAggregatedResult(r *db.AggregatedResult) error {
	m.AggregatedResults[r.TargetID] = append(m.AggregatedResults[r.TargetID], *r)
	return nil
}

func (m *MockStore) AddAggregatedResults(results []*db.AggregatedResult) error {
	for _, r := range results {
		m.AggregatedResults[r.TargetID] = append(m.AggregatedResults[r.TargetID], *r)
	}
	return nil
}

func (m *MockStore) GetLastRollupTime(targetID int64, windowSeconds int) (time.Time, error) {
	var maxTime time.Time
	for _, r := range m.AggregatedResults[targetID] {
		if r.WindowSeconds == windowSeconds {
			if r.Time.After(maxTime) {
				maxTime = r.Time
			}
		}
	}
	return maxTime, nil
}

func (m *MockStore) GetRawResults(targetID int64, start, end time.Time, limit int) ([]db.RawResult, error) {
	var res []db.RawResult
	for _, r := range m.RawResults[targetID] {
		if (r.Time.After(start) || r.Time.Equal(start)) && r.Time.Before(end) {
			res = append(res, r)
		}
	}
	// Sort by time just in case, though usually appended in order? Mock store might not guarantee order.
	// But let's just apply limit.
	if limit > 0 && len(res) > limit {
		res = res[:limit]
	}
	return res, nil
}

func (m *MockStore) GetAggregatedResults(targetID int64, windowSeconds int, start, end time.Time) ([]db.AggregatedResult, error) {
	var res []db.AggregatedResult
	for _, r := range m.AggregatedResults[targetID] {
		if r.WindowSeconds == windowSeconds && (r.Time.After(start) || r.Time.Equal(start)) && r.Time.Before(end) {
			res = append(res, r)
		}
	}
	return res, nil
}

func (m *MockStore) DeleteRawResultsBefore(targetID int64, cutoff time.Time) error {
	var keep []db.RawResult
	for _, r := range m.RawResults[targetID] {
		// Keep if time is >= cutoff (not strictly before)
		if !r.Time.Before(cutoff) {
			keep = append(keep, r)
		}
	}
	m.RawResults[targetID] = keep
	return nil
}

func (m *MockStore) DeleteAggregatedResultsBefore(targetID int64, windowSeconds int, cutoff time.Time) error {
	var keep []db.AggregatedResult
	for _, r := range m.AggregatedResults[targetID] {
		// Only filter if matching windowSeconds
		if r.WindowSeconds == windowSeconds {
			if !r.Time.Before(cutoff) {
				keep = append(keep, r)
			}
		} else {
			// Keep other windows
			keep = append(keep, r)
		}
	}
	m.AggregatedResults[targetID] = keep
	return nil
}

func (m *MockStore) DeleteAggregatedResultsByWindow(targetID int64, windowSeconds int) error {
	var keep []db.AggregatedResult
	for _, r := range m.AggregatedResults[targetID] {
		if r.WindowSeconds != windowSeconds {
			keep = append(keep, r)
		}
	}
	m.AggregatedResults[targetID] = keep
	return nil
}

func (m *MockStore) GetEarliestRawResultTime(targetID int64) (time.Time, error) {
	var minTime time.Time
	for _, r := range m.RawResults[targetID] {
		if minTime.IsZero() || r.Time.Before(minTime) {
			minTime = r.Time
		}
	}
	return minTime, nil
}

// MockStore implements db.Store interface
func (m *MockStore) GetDBSizeBytes() (int64, error) {
	return 0, nil
}

func (m *MockStore) GetPageCount() (int64, error) {
	return 0, nil
}

func (m *MockStore) GetPageSize() (int64, error) {
	return 0, nil
}

func (m *MockStore) GetFreelistCount() (int64, error) {
	return 0, nil
}

func (m *MockStore) GetTDigestStats() ([]db.TDigestStat, error) {
	return nil, nil
}

func (m *MockStore) GetRawStats() (*db.RawStats, error) {
	// Calculate from in-memory map
	var count int64
	for key := range m.RawResults {
		count += int64(len(m.RawResults[key]))
	}
	return &db.RawStats{Count: count, TotalBytes: count * 50}, nil
}

// Dashboard methods (stubs - not used in scheduler tests)
func (m *MockStore) AddDashboard(d *db.Dashboard) (int64, error) {
	return 0, nil
}

func (m *MockStore) UpdateDashboard(d *db.Dashboard) error {
	return nil
}

func (m *MockStore) GetDashboards() ([]db.Dashboard, error) {
	return nil, nil
}

func (m *MockStore) GetDashboard(id int64) (*db.Dashboard, error) {
	return nil, nil
}

func (m *MockStore) DeleteDashboard(id int64) error {
	return nil
}

func (m *MockStore) AddDashboardGraph(g *db.DashboardGraph) (int64, error) {
	return 0, nil
}

func (m *MockStore) UpdateDashboardGraph(g *db.DashboardGraph) error {
	return nil
}

func (m *MockStore) GetDashboardGraphs(dashboardID int64) ([]db.DashboardGraph, error) {
	return nil, nil
}

func (m *MockStore) DeleteDashboardGraph(id int64) error {
	return nil
}

func (m *MockStore) SetGraphTargets(graphID int64, targetIDs []int64) error {
	return nil
}

// MockRunner implements probe.Runner for testing
type MockRunner struct {
	RunFn func(cfg probe.Config) (float64, error)
}

func (m *MockRunner) Run(cfg probe.Config) (float64, error) {
	if m.RunFn != nil {
		return m.RunFn(cfg)
	}
	return 100.0, nil // Default 100ns latency
}
