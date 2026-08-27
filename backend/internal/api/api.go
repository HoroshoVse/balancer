package api

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/models"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
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
	mux.HandleFunc("/api/v1/settings", s.corsMiddleware(AuthMiddleware(s.getSettings)))
	mux.HandleFunc("/api/v1/settings/update", s.corsMiddleware(AuthMiddleware(s.updateSettings)))

	// Prometheus metrics endpoint (unprotected for scraping, or you can protect it)
	mux.Handle("/metrics", promhttp.Handler())

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

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	var settings models.Settings
	if err := s.db.First(&settings).Error; err != nil {
		settings = models.Settings{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var input models.Settings
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var settings models.Settings
	if err := s.db.First(&settings).Error; err != nil {
		if err := s.db.Create(&input).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		settings.TelegramBotToken = input.TelegramBotToken
		settings.TelegramChatID = input.TelegramChatID
		settings.NotificationsEnabled = input.NotificationsEnabled
		if err := s.db.Save(&settings).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
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
