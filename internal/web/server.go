package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"time"
	"vaportrail/internal/config"
	"vaportrail/internal/db"

	"sort"
	"vaportrail/internal/probe"
	"vaportrail/internal/scheduler"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed templates/*.html
var templatesJS embed.FS

type Server struct {
	cfg       *config.ServerConfig
	db        *db.DB
	scheduler *scheduler.Scheduler
	router    *chi.Mux
	templates *template.Template
}

func New(cfg *config.ServerConfig, database *db.DB, sched *scheduler.Scheduler) *Server {
	tmpl, err := template.ParseFS(templatesJS, "templates/*.html")
	if err != nil {
		panic(err)
	}

	s := &Server{
		cfg:       cfg,
		db:        database,
		scheduler: sched,
		router:    chi.NewRouter(),
		templates: tmpl,
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Get("/", s.handleDashboard)
	s.router.Get("/api/targets", s.handleGetTargets)
	s.router.Post("/api/targets", s.handleCreateTarget)
	s.router.Put("/api/targets/{id}", s.handleUpdateTarget)
	s.router.Delete("/api/targets/{id}", s.handleDeleteTarget)
	s.router.Get("/api/results/{id}", s.handleGetResults)
	s.router.Get("/graph/{id}", s.handleGraph)
}

func (s *Server) Start() error {
	return http.ListenAndServe(":"+strconv.Itoa(s.cfg.HTTPPort), s.router)
}

func (s *Server) handleCreateTarget(w http.ResponseWriter, r *http.Request) {
	var t db.Target
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Basic validation

	if t.RetentionPolicies != "" {
		var policies []scheduler.RetentionPolicy
		// First unmarshal to check JSON validity
		if err := json.Unmarshal([]byte(t.RetentionPolicies), &policies); err != nil {
			http.Error(w, "Invalid retention policies JSON", http.StatusBadRequest)
			return
		}
		// Then validate policies logic
		if err := scheduler.ValidateRetentionPolicies(policies); err != nil {
			http.Error(w, "Invalid retention policies: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	if t.Name == "" || t.Address == "" || t.ProbeType == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
	}
	if t.CommitInterval <= 0 {
		t.CommitInterval = 60.0
	}
	if t.Timeout <= 0 {
		t.Timeout = 5.0
	}

	// Check for valid probe type
	if _, err := probe.GetConfig(t.ProbeType, t.Address); err != nil {
		http.Error(w, "Invalid probe type", http.StatusBadRequest)
		return
	}

	// Drop any user provided config
	t.ProbeConfig = ""

	id, err := s.db.AddTarget(&t)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	t.ID = id
	// Notify scheduler
	if s.scheduler != nil {
		s.scheduler.AddTarget(t)
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(t)
}

func (s *Server) handleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteTarget(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.scheduler != nil {
		s.scheduler.RemoveTarget(id)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleUpdateTarget(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var t db.Target
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.ID = id

	if t.RetentionPolicies != "" {
		var policies []scheduler.RetentionPolicy
		if err := json.Unmarshal([]byte(t.RetentionPolicies), &policies); err != nil {
			http.Error(w, "Invalid retention policies JSON", http.StatusBadRequest)
			return
		}
		if err := scheduler.ValidateRetentionPolicies(policies); err != nil {
			http.Error(w, "Invalid retention policies: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	if t.Name == "" || t.Address == "" || t.ProbeType == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}
	if t.ProbeInterval == 0 {
		t.ProbeInterval = 1.0
	}
	if t.CommitInterval == 0 {
		t.CommitInterval = 60.0
	}
	if t.Timeout == 0 {
		t.Timeout = 5.0
	}

	if _, err := probe.GetConfig(t.ProbeType, t.Address); err != nil {
		http.Error(w, "Invalid probe type", http.StatusBadRequest)
		return
	}

	if err := s.db.UpdateTarget(&t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Update scheduler
	if s.scheduler != nil {
		s.scheduler.RemoveTarget(id)
		s.scheduler.AddTarget(t)
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(t)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if err := s.templates.ExecuteTemplate(w, "dashboard.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleGetTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.db.GetTargets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(targets)
}

type APIResult struct {
	Time         time.Time
	TargetID     int64
	MinNS        int64
	MaxNS        int64
	AvgNS        int64
	P0           float64
	P1           float64
	P25          float64
	P50          float64
	P75          float64
	P99          float64
	P100         float64
	Percentiles  []float64 // 0th, 5th, 10th... 100th
	TimeoutCount int64
	ProbeCount   int64
}

func sanitizeFloat(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0.0
	}
	return f
}

func (s *Server) handleGetResults(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	var start, end time.Time
	var window int
	// Fetch target to get retention policies
	target, err := s.db.GetTarget(id)
	if err != nil {
		// If target not found, we can't really determine policies.
		// Return 404 or just fail? The ID validation passed int parsing but DB check might fail.
		http.Error(w, "Target not found: "+err.Error(), http.StatusNotFound)
		return
	}

	if startStr != "" && endStr != "" {
		start, err = time.Parse(time.RFC3339, startStr)
		if err != nil {
			http.Error(w, "Invalid start time", http.StatusBadRequest)
			return
		}
		end, err = time.Parse(time.RFC3339, endStr)
		if err != nil {
			http.Error(w, "Invalid end time", http.StatusBadRequest)
			return
		}
	} else {
		// Default view (last hour)
		end = time.Now().UTC()
		start = end.Add(-1 * time.Hour)
	}

	// Dynamic Window Selection
	// Goal: < 1000 datapoints
	durationSeconds := end.Sub(start).Seconds()
	desiredWindow := max(int(durationSeconds/1000.0), 1)

	policies := scheduler.GetRetentionPolicies(*target)

	// Collect available windows from policies (and 0 for raw if 0 exists)
	// Actually policies usually define what we HAVE.
	// We want to pick the smallest window >= desiredWindow
	// availableWindows should include those defined in policies.

	var availableWindows []int
	for _, p := range policies {
		if p.Window > 0 {
			availableWindows = append(availableWindows, p.Window)
		}
	}
	sort.Ints(availableWindows)

	// Pick best window
	window = -1
	for _, w := range availableWindows {
		if w >= desiredWindow {
			window = w
			break
		}
	}

	// If no window found (all smaller than desired? Or desired is huge?)
	// If window is still -1, it means desiredWindow > all available windows.
	// Pick the largest one.
	if window == -1 && len(availableWindows) > 0 {
		window = availableWindows[len(availableWindows)-1]
	}

	// If we still don't have a window (e.g. policies empty?), default to 60
	if window == -1 {
		window = 60
	}

	var apiResults []APIResult

	results, err := s.db.GetAggregatedResults(id, window, start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, res := range results {
		apiRes := APIResult{
			Time:         res.Time,
			TargetID:     res.TargetID,
			TimeoutCount: res.TimeoutCount,
			ProbeCount:   0, // Will be populated from TDigest if available
		}

		if len(res.TDigestData) > 0 {
			td, err := db.DeserializeTDigest(res.TDigestData)
			if err == nil {
				// Compute average from centroids
				var totalMass, weightedSum float64
				td.ForEachCentroid(func(mean float64, count uint64) bool {
					totalMass += float64(count)
					weightedSum += mean * float64(count)
					return true
				})
				if totalMass > 0 {
					apiRes.AvgNS = int64(weightedSum / totalMass)
				}

				apiRes.ProbeCount = int64(td.Count())
				apiRes.P0 = sanitizeFloat(td.Quantile(0.0))
				apiRes.P1 = sanitizeFloat(td.Quantile(0.01))
				apiRes.P25 = sanitizeFloat(td.Quantile(0.25))
				apiRes.P50 = sanitizeFloat(td.Quantile(0.5))
				apiRes.P75 = sanitizeFloat(td.Quantile(0.75))
				apiRes.P99 = sanitizeFloat(td.Quantile(0.99))
				apiRes.P100 = sanitizeFloat(td.Quantile(1.0))

				apiRes.MinNS = int64(apiRes.P0)
				apiRes.MaxNS = int64(apiRes.P100)

				// Calculate every 5th percentile
				apiRes.Percentiles = make([]float64, 21)
				for i := 0; i <= 20; i++ {
					p := float64(i) * 0.05
					apiRes.Percentiles[i] = sanitizeFloat(td.Quantile(p))
				}
			}
		}
		apiResults = append(apiResults, apiRes)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiResults)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	target, err := s.db.GetTarget(id)
	if err != nil {
		http.Error(w, "Target not found: "+err.Error(), http.StatusNotFound)
		return
	}

	if err := s.templates.ExecuteTemplate(w, "graph.html", target); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
