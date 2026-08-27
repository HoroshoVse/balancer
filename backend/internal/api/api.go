package api

import (
	"encoding/json"
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
	mux.HandleFunc("/api/v1/load-balancers", s.corsMiddleware(AuthMiddleware(s.handleLoadBalancers)))
	mux.HandleFunc("/api/v1/load-balancers/update", s.corsMiddleware(AuthMiddleware(s.updateLoadBalancer)))
	mux.HandleFunc("/api/v1/load-balancers/delete", s.corsMiddleware(AuthMiddleware(s.deleteLoadBalancer)))
	mux.HandleFunc("/api/v1/backends", s.corsMiddleware(AuthMiddleware(s.getBackends)))
	mux.HandleFunc("/api/v1/metrics/overview", s.corsMiddleware(AuthMiddleware(s.getMetricsOverview)))
	mux.HandleFunc("/api/v1/settings", s.corsMiddleware(AuthMiddleware(s.getSettings)))
	mux.HandleFunc("/api/v1/settings/update", s.corsMiddleware(AuthMiddleware(s.updateSettings)))
	mux.HandleFunc("/api/v1/logs", s.corsMiddleware(AuthMiddleware(s.getLogs)))

	// Metrics
	mux.Handle("/metrics", promhttp.Handler())

	engine.Logger.Info("API Server listening on " + addr)
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

func (s *Server) handleLoadBalancers(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		s.getLoadBalancers(w, r)
		return
	}
	if r.Method == "POST" {
		s.createLoadBalancer(w, r)
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) getLoadBalancers(w http.ResponseWriter, r *http.Request) {
	var lbs []models.LoadBalancer
	if err := s.db.Preload("BackendGroup.Backends").Find(&lbs).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range lbs {
		lbs[i].Metrics = engine.Metrics.GetMetricsForLB(lbs[i].ID)
		for j := range lbs[i].BackendGroup.Backends {
			b := &lbs[i].BackendGroup.Backends[j]
			isUp := s.engine.GetHealthState(b.ID)
			if isUp {
				b.Status = "UP"
			} else {
				b.Status = "DOWN"
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lbs)
}

func (s *Server) createLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var lb models.LoadBalancer
	if err := json.NewDecoder(r.Body).Decode(&lb); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.db.Create(&lb).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reload config asynchronously so API returns quickly
	go func() {
		if err := s.engine.ReloadConfig(); err != nil {
			engine.Logger.Error("Failed to reload config: " + err.Error())
		}
	}()

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) deleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}

	if err := s.db.Delete(&models.LoadBalancer{}, id).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	go func() {
		if err := s.engine.ReloadConfig(); err != nil {
			engine.Logger.Error("Failed to reload config: " + err.Error())
		}
	}()

	w.WriteHeader(http.StatusOK)
}

func (s *Server) updateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var input models.LoadBalancer
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Load existing LB
	var existing models.LoadBalancer
	if err := s.db.Preload("BackendGroup.Backends").First(&existing, input.ID).Error; err != nil {
		http.Error(w, "Load balancer not found", http.StatusNotFound)
		return
	}

	// Update LB fields
	existing.Name = input.Name
	existing.ListenIP = input.ListenIP
	existing.ListenPort = input.ListenPort
	existing.Protocol = input.Protocol
	existing.Algorithm = input.Algorithm
	existing.SSLEnabled = input.SSLEnabled
	existing.ACMEEnabled = input.ACMEEnabled
	existing.ACMEEmail = input.ACMEEmail
	existing.HTTP3Enabled = input.HTTP3Enabled
	existing.ProxyProtocolEnabled = input.ProxyProtocolEnabled
	existing.ProxyProtocolVersion = input.ProxyProtocolVersion
	existing.StickySessionsEnabled = input.StickySessionsEnabled
	existing.StickySessionType = input.StickySessionType

	if err := s.db.Save(&existing).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Update backend group
	existing.BackendGroup.Name = input.BackendGroup.Name
	s.db.Save(&existing.BackendGroup)

	// Delete old backends and create new ones
	s.db.Where("group_id = ?", existing.BackendGroup.ID).Delete(&models.BackendServer{})
	for _, b := range input.BackendGroup.Backends {
		b.GroupID = existing.BackendGroup.ID
		b.ID = 0 // Force insert
		s.db.Create(&b)
	}

	go func() {
		if err := s.engine.ReloadConfig(); err != nil {
			engine.Logger.Error("Failed to reload config: " + err.Error())
		}
	}()

	w.WriteHeader(http.StatusOK)
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(engine.Logger.GetLogs()); err != nil {
		http.Error(w, "Failed to encode logs", http.StatusInternalServerError)
	}
}
