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
	database, err := db.New(":memory:")
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
		Name:      "Test Target",
		Address:   "example.com",
		ProbeType: "http",
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

func TestHandleGraph(t *testing.T) {
	s, database := setupTestServer(t)
	defer database.Close()

	// Add a target
	target := &db.Target{
		Name:      "Test Target",
		Address:   "example.com",
		ProbeType: "http",
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
