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
	StdDevNS     float64
	SumSqNS      float64
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

	var dbResults []db.Result
	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")

	if startStr != "" && endStr != "" {
		start, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			http.Error(w, "Invalid start time", http.StatusBadRequest)
			return
		}
		end, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			http.Error(w, "Invalid end time", http.StatusBadRequest)
			return
		}
		dbResults, err = s.db.GetResultsByTime(id, start, end)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Default to last 100 results for simple views
		dbResults, err = s.db.GetResults(id, 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Initialize as empty slice so it marshals to [] instead of null
	apiResults := []APIResult{}
	for _, res := range dbResults {
		apiRes := APIResult{
			Time:         res.Time,
			TargetID:     res.TargetID,
			MinNS:        res.MinNS,
			MaxNS:        res.MaxNS,
			AvgNS:        res.AvgNS,
			StdDevNS:     res.StdDevNS,
			SumSqNS:      res.SumSqNS,
			TimeoutCount: res.TimeoutCount,
			ProbeCount:   res.ProbeCount,
		}

		if len(res.TDigestData) > 0 {
			td, err := db.DeserializeTDigest(res.TDigestData)
			if err == nil {
				apiRes.P0 = sanitizeFloat(td.Quantile(0.0))
				apiRes.P1 = sanitizeFloat(td.Quantile(0.01))
				apiRes.P25 = sanitizeFloat(td.Quantile(0.25))
				apiRes.P50 = sanitizeFloat(td.Quantile(0.5))
				apiRes.P75 = sanitizeFloat(td.Quantile(0.75))
				apiRes.P99 = sanitizeFloat(td.Quantile(0.99))
				apiRes.P100 = sanitizeFloat(td.Quantile(1.0))

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
