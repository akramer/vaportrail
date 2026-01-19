package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
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

//go:embed static/*
var staticFS embed.FS

type Server struct {
	cfg       *config.ServerConfig
	db        *db.DB
	scheduler *scheduler.Scheduler
	router    *chi.Mux
	templates *template.Template
}

func New(cfg *config.ServerConfig, database *db.DB, sched *scheduler.Scheduler) *Server {
	funcMap := template.FuncMap{
		"byteSize": func(b int64) string {
			const unit = 1024
			if b < unit {
				return fmt.Sprintf("%d B", b)
			}
			div, exp := int64(unit), 0
			for n := b / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
		},
		"formatDuration": func(seconds int64) string {
			if seconds <= 0 {
				return "-"
			}
			const (
				minute = 60
				hour   = 60 * minute
				day    = 24 * hour
				month  = 30 * day
				year   = 365 * day
			)
			switch {
			case seconds >= year:
				years := float64(seconds) / float64(year)
				if years == float64(int(years)) {
					return fmt.Sprintf("%dy", int(years))
				}
				return fmt.Sprintf("%.1fy", years)
			case seconds >= month:
				months := float64(seconds) / float64(month)
				if months == float64(int(months)) {
					return fmt.Sprintf("%dmo", int(months))
				}
				return fmt.Sprintf("%.1fmo", months)
			case seconds >= day:
				days := float64(seconds) / float64(day)
				if days == float64(int(days)) {
					return fmt.Sprintf("%dd", int(days))
				}
				return fmt.Sprintf("%.1fd", days)
			case seconds >= hour:
				hours := float64(seconds) / float64(hour)
				if hours == float64(int(hours)) {
					return fmt.Sprintf("%dh", int(hours))
				}
				return fmt.Sprintf("%.1fh", hours)
			case seconds >= minute:
				mins := float64(seconds) / float64(minute)
				if mins == float64(int(mins)) {
					return fmt.Sprintf("%dm", int(mins))
				}
				return fmt.Sprintf("%.1fm", mins)
			default:
				return fmt.Sprintf("%ds", seconds)
			}
		},
		"printf": fmt.Sprintf,
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templatesJS, "templates/*.html")
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
	s.router.Get("/status", s.handleStatus)
	s.router.Get("/favicon.png", s.handleFavicon)

	// Dashboard routes
	s.router.Get("/api/dashboards", s.handleGetDashboards)
	s.router.Post("/api/dashboards", s.handleCreateDashboard)
	s.router.Put("/api/dashboards/{id}", s.handleUpdateDashboard)
	s.router.Delete("/api/dashboards/{id}", s.handleDeleteDashboard)
	s.router.Get("/api/dashboards/{id}/graphs", s.handleGetDashboardGraphs)
	s.router.Post("/api/dashboards/{id}/graphs", s.handleCreateDashboardGraph)
	s.router.Put("/api/dashboards/{dashboardId}/graphs/{graphId}", s.handleUpdateDashboardGraph)
	s.router.Delete("/api/dashboards/{dashboardId}/graphs/{graphId}", s.handleDeleteDashboardGraph)

	// Dashboard pages
	s.router.Get("/dashboards/create", s.handleDashboardCreatePage)
	s.router.Get("/dashboards/view", s.handleDashboardViewPage)
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
		// Then validate policies logic (this also sorts them)
		if err := scheduler.ValidateRetentionPolicies(policies); err != nil {
			http.Error(w, "Invalid retention policies: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Re-serialize sorted policies
		sortedJSON, _ := json.Marshal(policies)
		t.RetentionPolicies = string(sortedJSON)
	}

	if t.Name == "" || t.Address == "" || t.ProbeType == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	if t.ProbeInterval <= 0 {
		t.ProbeInterval = 1.0
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

	// Apply default retention policies if not provided
	if t.RetentionPolicies == "" {
		t.RetentionPolicies = scheduler.DefaultPoliciesJSON()
	}

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

	// Fetch existing target to compare retention policies
	existingTarget, err := s.db.GetTarget(id)
	if err != nil {
		http.Error(w, "Target not found", http.StatusNotFound)
		return
	}

	var t db.Target
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.ID = id

	var newPolicies []scheduler.RetentionPolicy
	if t.RetentionPolicies != "" {
		if err := json.Unmarshal([]byte(t.RetentionPolicies), &newPolicies); err != nil {
			http.Error(w, "Invalid retention policies JSON", http.StatusBadRequest)
			return
		}
		// Validate (this also sorts them)
		if err := scheduler.ValidateRetentionPolicies(newPolicies); err != nil {
			http.Error(w, "Invalid retention policies: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Re-serialize sorted policies
		sortedJSON, _ := json.Marshal(newPolicies)
		t.RetentionPolicies = string(sortedJSON)
	}

	if t.Name == "" || t.Address == "" || t.ProbeType == "" {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}
	if t.ProbeInterval == 0 {
		t.ProbeInterval = 1.0
	}
	if t.Timeout == 0 {
		t.Timeout = 5.0
	}

	if _, err := probe.GetConfig(t.ProbeType, t.Address); err != nil {
		http.Error(w, "Invalid probe type", http.StatusBadRequest)
		return
	}

	// Detect removed retention policies and delete their data
	oldPolicies, _ := scheduler.GetRetentionPolicies(*existingTarget)
	newWindowSet := make(map[int]bool)
	for _, p := range newPolicies {
		newWindowSet[p.Window] = true
	}
	for _, oldP := range oldPolicies {
		if !newWindowSet[oldP.Window] {
			// This window was removed, delete its aggregated data
			if oldP.Window == 0 {
				// Raw data - we could delete it, but typically raw is retained if any policy exists
				// For now, we skip raw data deletion on policy removal
				continue
			}
			s.db.DeleteAggregatedResultsByWindow(id, oldP.Window)
		}
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
	Time          time.Time
	TargetID      int64
	MinNS         int64
	MaxNS         int64
	AvgNS         int64
	P0            float64
	P1            float64
	P25           float64
	P50           float64
	P75           float64
	P99           float64
	P100          float64
	Percentiles   []float64 // 0th, 5th, 10th... 100th
	TimeoutCount  int64
	ProbeCount    int64
	WindowSeconds int
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

	policies, err := scheduler.GetRetentionPolicies(*target)
	if err != nil {
		http.Error(w, "Target has no retention policies configured", http.StatusInternalServerError)
		return
	}

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

	if r.URL.Query().Get("raw") == "true" {
		// User: "render the first 1000"
		// Just pull 1000. DB query is ordered by time ASC, so this gives first 1000.
		rawResults, err := s.db.GetRawResults(id, start, end, 1000)
		if err != nil {
			http.Error(w, "Failed to get raw results: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// No longer erroring on > 1000, just returning what we got (capped at 1000 by query limit)

		for _, rr := range rawResults {
			apiRes := APIResult{
				Time:       rr.Time,
				TargetID:   rr.TargetID,
				ProbeCount: 1,
				MinNS:      int64(rr.Latency),
				MaxNS:      int64(rr.Latency),
				AvgNS:      int64(rr.Latency), // Set Avg to latency for simple display usually
				P0:         rr.Latency,
				P100:       rr.Latency,
				P50:        rr.Latency, // Median is the value itself
			}
			apiResults = append(apiResults, apiRes)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(apiResults)
		return
	}

	results, err := s.db.GetAggregatedResults(id, window, start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for _, res := range results {
		apiRes := APIResult{
			Time:          res.Time,
			TargetID:      res.TargetID,
			TimeoutCount:  res.TimeoutCount,
			ProbeCount:    0, // Will be populated from TDigest if available
			WindowSeconds: res.WindowSeconds,
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

type StatusPageTimings struct {
	DBSize        time.Duration
	PageCount     time.Duration
	PageSize      time.Duration
	FreelistCount time.Duration
	TDigestStats  time.Duration
	RawStats      time.Duration
}

type StatusPageData struct {
	Name          string
	DBSize        int64
	PageCount     int64
	PageSize      int64
	FreelistCount int64
	TDigestStats  []db.TDigestStat
	RawStats      *db.RawStats
	Timings       StatusPageTimings
}

func (s StatusPageData) DBSizeString() string {
	const unit = 1024
	if s.DBSize < unit {
		return fmt.Sprintf("%d B", s.DBSize)
	}
	div, exp := int64(unit), 0
	for n := s.DBSize / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(s.DBSize)/float64(div), "KMGTPE"[exp])
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	dbSize, err := s.db.GetDBSizeBytes()
	dbSizeDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get DB size: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start = time.Now()
	pageCount, err := s.db.GetPageCount()
	pageCountDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get page count: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start = time.Now()
	pageSize, err := s.db.GetPageSize()
	pageSizeDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get page size: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start = time.Now()
	freelistCount, err := s.db.GetFreelistCount()
	freelistCountDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get freelist count: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start = time.Now()
	tdStats, err := s.db.GetTDigestStats()
	tdStatsDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get tdigest stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	start = time.Now()
	rawStats, err := s.db.GetRawStats()
	rawStatsDuration := time.Since(start)
	if err != nil {
		http.Error(w, "Failed to get raw stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Status Page Timings: DBSize=%v PageCount=%v PageSize=%v FreelistCount=%v TDigestStats=%v RawStats=%v",
		dbSizeDuration, pageCountDuration, pageSizeDuration, freelistCountDuration, tdStatsDuration, rawStatsDuration)

	data := StatusPageData{
		Name:          "System Status",
		DBSize:        dbSize,
		PageCount:     pageCount,
		PageSize:      pageSize,
		FreelistCount: freelistCount,
		TDigestStats:  tdStats,
		RawStats:      rawStats,
		Timings: StatusPageTimings{
			DBSize:        dbSizeDuration,
			PageCount:     pageCountDuration,
			PageSize:      pageSizeDuration,
			FreelistCount: freelistCountDuration,
			TDigestStats:  tdStatsDuration,
			RawStats:      rawStatsDuration,
		},
	}

	if err := s.templates.ExecuteTemplate(w, "status.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := staticFS.ReadFile("static/favicon.png")
	if err != nil {
		http.Error(w, "Favicon not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800") // Cache for 1 week
	w.Write(data)
}

// Dashboard API handlers

func (s *Server) handleGetDashboards(w http.ResponseWriter, r *http.Request) {
	dashboards, err := s.db.GetDashboards()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dashboards)
}

func (s *Server) handleCreateDashboard(w http.ResponseWriter, r *http.Request) {
	var dash db.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&dash); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if dash.Name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	id, err := s.db.AddDashboard(&dash)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	dash.ID = id
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(dash)
}

func (s *Server) handleUpdateDashboard(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var dash db.Dashboard
	if err := json.NewDecoder(r.Body).Decode(&dash); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dash.ID = id

	if dash.Name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	if err := s.db.UpdateDashboard(&dash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(dash)
}

func (s *Server) handleDeleteDashboard(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteDashboard(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetDashboardGraphs(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	dashboardID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid dashboard ID", http.StatusBadRequest)
		return
	}

	graphs, err := s.db.GetDashboardGraphs(dashboardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(graphs)
}

type CreateGraphRequest struct {
	Title     string  `json:"title"`
	Position  int     `json:"position"`
	TargetIDs []int64 `json:"targetIds"`
}

func (s *Server) handleCreateDashboardGraph(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	dashboardID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid dashboard ID", http.StatusBadRequest)
		return
	}

	var req CreateGraphRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	graph := &db.DashboardGraph{
		DashboardID: dashboardID,
		Title:       req.Title,
		Position:    req.Position,
	}

	graphID, err := s.db.AddDashboardGraph(graph)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set targets if provided
	if len(req.TargetIDs) > 0 {
		if err := s.db.SetGraphTargets(graphID, req.TargetIDs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	graph.ID = graphID
	graph.TargetIDs = req.TargetIDs
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(graph)
}

func (s *Server) handleUpdateDashboardGraph(w http.ResponseWriter, r *http.Request) {
	graphIdStr := chi.URLParam(r, "graphId")
	graphID, err := strconv.ParseInt(graphIdStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid graph ID", http.StatusBadRequest)
		return
	}

	var req CreateGraphRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Title == "" {
		http.Error(w, "Title is required", http.StatusBadRequest)
		return
	}

	graph := &db.DashboardGraph{
		ID:       graphID,
		Title:    req.Title,
		Position: req.Position,
	}

	if err := s.db.UpdateDashboardGraph(graph); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Update targets
	if err := s.db.SetGraphTargets(graphID, req.TargetIDs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	graph.TargetIDs = req.TargetIDs
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(graph)
}

func (s *Server) handleDeleteDashboardGraph(w http.ResponseWriter, r *http.Request) {
	graphIdStr := chi.URLParam(r, "graphId")
	graphID, err := strconv.ParseInt(graphIdStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid graph ID", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteDashboardGraph(graphID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// Dashboard page handlers

func (s *Server) handleDashboardCreatePage(w http.ResponseWriter, r *http.Request) {
	if err := s.templates.ExecuteTemplate(w, "dashboard_create.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleDashboardViewPage(w http.ResponseWriter, r *http.Request) {
	if err := s.templates.ExecuteTemplate(w, "dashboard_view.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
