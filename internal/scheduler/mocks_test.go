package scheduler

import (
	"errors"
	"time"
	"vaportrail/internal/db"
	"vaportrail/internal/probe"
)

// MockStore implements db.Store for testing
type MockStore struct {
	Targets        map[int64]db.Target
	Results        map[int64][]db.Result
	AddTargetFn    func(t *db.Target) (int64, error)
	GetTargetsFn   func() ([]db.Target, error)
	AddResultFn    func(r *db.Result) error
	DeleteTargetFn func(id int64) error
	CloseFn        func() error
}

func NewMockStore() *MockStore {
	return &MockStore{
		Targets: make(map[int64]db.Target),
		Results: make(map[int64][]db.Result),
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
