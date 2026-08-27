package api

import (
	"encoding/json"
	"net/http"
	"log"
	"gorm.io/gorm"
	"github.com/balacer/backend/internal/engine"
	"github.com/balacer/backend/internal/models"
)

type Server struct {
	db     *gorm.DB
	engine *engine.Engine
}

func NewServer(db *gorm.DB, eng *engine.Engine) *Server {
	return &Server{
		db:     db,
		engine: eng,
	}
}

func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()

	// Public Routes
	mux.HandleFunc("/api/v1/auth/login", s.corsMiddleware(s.login))

	// Protected Routes
	mux.HandleFunc("/api/v1/load-balancers", s.corsMiddleware(AuthMiddleware(s.getLoadBalancers)))
	mux.HandleFunc("/api/v1/backends", s.corsMiddleware(AuthMiddleware(s.getBackends)))
	mux.HandleFunc("/api/v1/metrics/overview", s.corsMiddleware(AuthMiddleware(s.getMetricsOverview)))

	log.Printf("API Server listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

func (s *Server) getBackends(w http.ResponseWriter, r *http.Request) {
	var backends []models.BackendServer
	if err := s.db.Find(&backends).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(backends)
}

func (s *Server) getMetricsOverview(w http.ResponseWriter, r *http.Request) {
	// Let's get backend status from the DB to count total vs healthy
	var allBackends []models.BackendServer
	s.db.Find(&allBackends)

	total := len(allBackends)
	healthy := 0
	for _, b := range allBackends {
		if b.Status == "UP" {
			healthy++
		}
	}

	data := engine.Metrics.GetOverview(healthy, total)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) getLoadBalancers(w http.ResponseWriter, r *http.Request) {
	var lbs []models.LoadBalancer
	if err := s.db.Preload("BackendGroup.Backends").Find(&lbs).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range lbs {
		lbs[i].Metrics = engine.Metrics.GetMetricsForLB(lbs[i].ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lbs)
}
