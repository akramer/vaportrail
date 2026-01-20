package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"vaportrail/internal/config"
	"vaportrail/internal/db"

	"github.com/caio/go-tdigest/v4"
)

func setupTestServer(t *testing.T) (*Server, *db.DB) {
	// Use shared cache to allow multiple connections to the same in-memory DB
	// This is required because nested queries (like in GetDashboardGraphs)
	// will trigger new connections from the pool.
	database, err := db.New("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to create db: %v", err)
	}

	cfg := &config.ServerConfig{
		HTTPPort: 8080,
	}

	s := New(cfg, database, nil)
	return s, database
}

func TestHandleGetResults(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	// Add a target
	target := &db.Target{
		Name:              "Test Target",
		Address:           "example.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window": 0, "retention": 604800}, {"window": 60, "retention": 15768000}]`,
	}
	id, err := database.AddTarget(target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}

	// Add some aggregated results
	now := time.Now().UTC().Truncate(time.Second)

	// Create dummy tdigest
	td, _ := tdigest.New(tdigest.Compression(100))
	td.Add(100)
	tdBytes, _ := db.SerializeTDigest(td)

	r1 := &db.AggregatedResult{
		Time:          now.Add(-50 * time.Minute),
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   tdBytes,
		TimeoutCount:  0,
	}
	r2 := &db.AggregatedResult{
		Time:          now.Add(-30 * time.Minute),
		TargetID:      id,
		WindowSeconds: 60,
		TDigestData:   tdBytes,
		TimeoutCount:  0,
	}
	if err := database.AddAggregatedResult(r1); err != nil {
		t.Fatalf("Failed to add result 1: %v", err)
	}
	if err := database.AddAggregatedResult(r2); err != nil {
		t.Fatalf("Failed to add result 2: %v", err)
	}

	// Add some raw results for fallback/raw testing
	// Raw results added but should NOT be used.
	raw1 := &db.RawResult{
		Time:     now.Add(-40 * time.Minute),
		TargetID: id,
		Latency:  50,
	}
	if err := database.AddRawResults([]db.RawResult{*raw1}); err != nil {
		t.Fatalf("Failed to add raw result: %v", err)
	}

	// Test 1: No params (default limit 100, should see both if within limit, sorted DESC)
	req := httptest.NewRequest("GET", "/api/results/1", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v", rr.Code)
	}

	var results []APIResult
	if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}

	// Test 2: Time range filtering (Select only r2)
	// r1 is -50m, r2 is -30m.
	// Query: -40m to now. Should skip r1.
	start := now.Add(-40 * time.Minute).Format(time.RFC3339)
	end := now.Format(time.RFC3339)
	req = httptest.NewRequest("GET", "/api/results/1?start="+start+"&end="+end, nil)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v", rr.Code)
	}

	if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result, got %d", len(results))
	} else {
		// Verify it is r2
		// DB stores time with some precision, compare unix or verify ID/order
		// result doesn't have ID, check Time.
		// Note: JSON time might lose monotonic clock, compare Unix()
		if results[0].Time.Unix() != r2.Time.Unix() {
			t.Errorf("Expected result 2 time %v, got %v", r2.Time, results[0].Time)
		}
	}

	// Test 3: Invalid time params
	req = httptest.NewRequest("GET", "/api/results/1?start=invalid&end="+end, nil)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400 for invalid start time, got %v", rr.Code)
	}
}

func TestHandleGetResults_Raw(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	// Add a target
	target := &db.Target{
		Name:              "Test Target",
		Address:           "example.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window": 0, "retention": 604800}, {"window": 60, "retention": 15768000}]`,
	}
	id, err := database.AddTarget(target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	// Add 900 raw results (below 1000 limit)
	var batch []db.RawResult
	for i := 0; i < 900; i++ {
		batch = append(batch, db.RawResult{
			Time:     now.Add(time.Duration(-i) * time.Minute),
			TargetID: id,
			Latency:  float64(i * 100),
		})
	}
	if err := database.AddRawResults(batch); err != nil {
		t.Fatalf("Failed to add raw results: %v", err)
	}

	// Test 1: Fetch raw results (should succeed)
	start := now.Add(-1000 * time.Minute).Format(time.RFC3339)
	end := now.Add(time.Minute).Format(time.RFC3339)

	req := httptest.NewRequest("GET", "/api/results/"+strconv.Itoa(int(id))+"?start="+start+"&end="+end+"&raw=true", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v body: %s", rr.Code, rr.Body.String())
	}

	var results []APIResult
	if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}
	if len(results) != 900 {
		t.Errorf("Expected 900 results, got %d", len(results))
	}
	// Check one result
	if results[0].ProbeCount != 1 {
		t.Errorf("Expected ProbCount 1 for raw result, got %d", results[0].ProbeCount)
	}

	// Test 2: Add more results to exceed 1000
	var batch2 []db.RawResult
	for i := 0; i < 200; i++ {
		batch2 = append(batch2, db.RawResult{
			Time:     now.Add(time.Duration(-1000-i) * time.Minute),
			TargetID: id,
			Latency:  1.0,
		})
	}
	if err := database.AddRawResults(batch2); err != nil {
		t.Fatalf("Failed to add raw results batch 2: %v", err)
	}

	// Now total 1100. Range covers all.
	startWide := now.Add(-2000 * time.Minute).Format(time.RFC3339)
	req = httptest.NewRequest("GET", "/api/results/"+strconv.Itoa(int(id))+"?start="+startWide+"&end="+end+"&raw=true", nil)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200 for >1000 results (capped), got %v", rr.Code)
	}

	var cappedResults []APIResult
	if err := json.NewDecoder(rr.Body).Decode(&cappedResults); err != nil {
		t.Errorf("Failed to decode response: %v", err)
	}
	if len(cappedResults) != 1000 {
		t.Errorf("Expected 1000 capped results, got %d", len(cappedResults))
	}
}

func TestHandleGraph(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	// Add a target
	target := &db.Target{
		Name:              "Test Target",
		Address:           "example.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window": 0, "retention": 604800}, {"window": 60, "retention": 15768000}]`,
	}
	id, err := database.AddTarget(target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}

	// Test valid ID
	req := httptest.NewRequest("GET", "/graph/"+strconv.Itoa(int(id)), nil)
	rr := httptest.NewRecorder()

	// Need to register route properly in setupTestServer or use router
	// setupTestServer calls s.routes() which registers routes on s.router

	// chi router context needs to be set up if calling handler directly,
	// but ServeHTTP does it for us.
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v", rr.Code)
	}

	// Test invalid ID
	req = httptest.NewRequest("GET", "/graph/999", nil)
	rr = httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	// GetTarget returns error if not found, handler returns 404
	// Wait, GetTarget implementation:
	// err := d.QueryRow(...).Scan(...)
	// if err == sql.ErrNoRows ...
	// My GetTarget implementation returns err if Scan fails.
	// If id not found, Scan returns ErrNoRows.
	// Handler checks err and returns 404?
	// Handler:
	// 	target, err := s.db.GetTarget(id)
	// 	if err != nil { http.Error(w, ..., http.StatusNotFound) }
	// So 404 is expected.

	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected status 404 for non-existent target, got %v", rr.Code)
	}
}

func TestHandleStatus(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	req := httptest.NewRequest("GET", "/status", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v", rr.Code)
	}

	// Verify some content to ensure template rendered
	if !contains(rr.Body.String(), "System Status") {
		t.Errorf("Expected 'System Status' in response body, got:\n%s", rr.Body.String())
	}
	if !contains(rr.Body.String(), "Database Statistics") {
		t.Errorf("Expected 'Database Statistics' in response body")
	}
	if !contains(rr.Body.String(), "Raw Data Statistics") {
		t.Errorf("Expected 'Raw Data Statistics' in response body, got:\n%s", rr.Body.String())
	}
}

func TestHandleDashboard(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %v", rr.Code)
	}

	if !contains(rr.Body.String(), "Dashboard") {
		t.Errorf("Expected 'Dashboard' in response body, got:\n%s", rr.Body.String())
	}
}

func TestPublicDashboard(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	// Create a target
	target := &db.Target{
		Name:              "Test Target",
		Address:           "example.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window": 0, "retention": 604800}, {"window": 60, "retention": 15768000}]`,
	}
	targetId, err := database.AddTarget(target)
	if err != nil {
		t.Fatalf("Failed to add target: %v", err)
	}

	// Create another target (not in dashboard)
	otherTarget := &db.Target{
		Name:              "Other Target",
		Address:           "other.com",
		ProbeType:         "http",
		RetentionPolicies: `[{"window": 60, "retention": 604800}]`,
	}
	otherTargetId, err := database.AddTarget(otherTarget)
	if err != nil {
		t.Fatalf("Failed to add other target: %v", err)
	}

	// Create a dashboard
	dash := &db.Dashboard{
		Name:     "Test Dashboard",
		IsPublic: true,
	}
	dashId, err := database.AddDashboard(dash)
	if err != nil {
		t.Fatalf("Failed to add dashboard: %v", err)
	}

	// Generate a slug for the dashboard
	slug, err := database.RegenerateDashboardSlug(dashId)
	if err != nil {
		t.Fatalf("Failed to generate slug: %v", err)
	}

	// Update dashboard to be public
	dash.ID = dashId
	dash.IsPublic = true
	dash.PublicSlug = slug
	if err := database.UpdateDashboard(dash); err != nil {
		t.Fatalf("Failed to update dashboard: %v", err)
	}

	// Add a graph to the dashboard with the first target only
	graph := &db.DashboardGraph{
		DashboardID: dashId,
		Title:       "Test Graph",
		Position:    0,
	}
	graphId, err := database.AddDashboardGraph(graph)
	if err != nil {
		t.Fatalf("Failed to add graph: %v", err)
	}

	// Set graph targets (only the first target)
	if err := database.SetGraphTargets(graphId, []int64{targetId}); err != nil {
		t.Fatalf("Failed to set graph targets: %v", err)
	}

	// Add some aggregated results for both targets
	now := time.Now().UTC().Truncate(time.Second)
	td, _ := tdigest.New(tdigest.Compression(100))
	td.Add(100)
	tdBytes, _ := db.SerializeTDigest(td)

	r1 := &db.AggregatedResult{
		Time:          now.Add(-30 * time.Minute),
		TargetID:      targetId,
		WindowSeconds: 60,
		TDigestData:   tdBytes,
		TimeoutCount:  0,
	}
	if err := database.AddAggregatedResult(r1); err != nil {
		t.Fatalf("Failed to add result: %v", err)
	}

	r2 := &db.AggregatedResult{
		Time:          now.Add(-30 * time.Minute),
		TargetID:      otherTargetId,
		WindowSeconds: 60,
		TDigestData:   tdBytes,
		TimeoutCount:  0,
	}
	if err := database.AddAggregatedResult(r2); err != nil {
		t.Fatalf("Failed to add result: %v", err)
	}

	t.Run("Public dashboard page returns 200", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/public/"+slug, nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %v", rr.Code)
		}
	})

	t.Run("Public dashboard page returns 404 for invalid slug", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/public/invalidslug123", nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %v", rr.Code)
		}
	})

	t.Run("Public graphs endpoint returns graphs", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/public/"+slug+"/graphs", nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %v", rr.Code)
		}

		var graphs []db.DashboardGraph
		if err := json.NewDecoder(rr.Body).Decode(&graphs); err != nil {
			t.Errorf("Failed to decode response: %v", err)
		}
		if len(graphs) != 1 {
			t.Errorf("Expected 1 graph, got %d", len(graphs))
		}
	})

	t.Run("Public graphs endpoint returns 404 for invalid slug", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/public/invalidslug123/graphs", nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("Expected status 404, got %v", rr.Code)
		}
	})

	t.Run("Public results returns data for allowed target", func(t *testing.T) {
		start := now.Add(-1 * time.Hour).Format(time.RFC3339)
		end := now.Format(time.RFC3339)
		req := httptest.NewRequest("GET", "/public/"+slug+"/results/"+strconv.FormatInt(targetId, 10)+"?start="+start+"&end="+end, nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("Expected status 200, got %v", rr.Code)
		}
	})

	t.Run("Public results returns 404 for target not in dashboard", func(t *testing.T) {
		start := now.Add(-1 * time.Hour).Format(time.RFC3339)
		end := now.Format(time.RFC3339)
		req := httptest.NewRequest("GET", "/public/"+slug+"/results/"+strconv.FormatInt(otherTargetId, 10)+"?start="+start+"&end="+end, nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for target not in dashboard, got %v", rr.Code)
		}
	})

	t.Run("Public results returns 404 for invalid slug", func(t *testing.T) {
		start := now.Add(-1 * time.Hour).Format(time.RFC3339)
		end := now.Format(time.RFC3339)
		req := httptest.NewRequest("GET", "/public/invalidslug123/results/"+strconv.FormatInt(targetId, 10)+"?start="+start+"&end="+end, nil)
		rr := httptest.NewRecorder()
		s.router.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for invalid slug, got %v", rr.Code)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[0:len(substr)] == substr || len(s) > len(substr) && contains(s[1:], substr)
}
