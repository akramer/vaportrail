package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vaportrail/internal/config"
	"vaportrail/internal/db"
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

	// Add some results
	now := time.Now()
	r1 := &db.Result{
		Time:       now.Add(-2 * time.Hour),
		TargetID:   id,
		MinNS:      1000,
		MaxNS:      2000,
		AvgNS:      1500,
		ProbeCount: 1,
	}
	r2 := &db.Result{
		Time:       now.Add(-30 * time.Minute),
		TargetID:   id,
		MinNS:      1000,
		MaxNS:      2000,
		AvgNS:      1500,
		ProbeCount: 1,
	}
	if err := database.AddResult(r1); err != nil {
		t.Fatalf("Failed to add result 1: %v", err)
	}
	if err := database.AddResult(r2); err != nil {
		t.Fatalf("Failed to add result 2: %v", err)
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
	// r1 is -2h, r2 is -30m.
	// Query: -1h to now. Should skip r1.
	start := now.Add(-1 * time.Hour).Format(time.RFC3339)
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
