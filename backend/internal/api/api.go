package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/models"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Server struct {
	db        *gorm.DB
	engine    *engine.Engine
	jwtSecret []byte
	limiter   *loginLimiter
	dummyHash []byte // compared against when the user doesn't exist, to keep timing uniform
	info      SystemInfo
}

// SystemInfo describes how this instance runs, for the security checklist in the UI.
type SystemInfo struct {
	Version    string
	APIAddr    string
	JWTFromEnv bool
}

// SetInfo records how the server was started.
func (s *Server) SetInfo(info SystemInfo) {
	s.info = info
}

func NewServer(db *gorm.DB, eng *engine.Engine, jwtSecret []byte) *Server {
	return &Server{
		db:        db,
		engine:    eng,
		jwtSecret: jwtSecret,
		limiter:   newLoginLimiter(),
		dummyHash: mustDummyHash(),
	}
}

// Handler returns the API routes. The UI reaches them through the
// frontend's nginx on the same origin, so no CORS headers are sent.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public Routes
	mux.HandleFunc("/api/v1/auth/login", s.login)

	// Protected Routes
	mux.HandleFunc("/api/v1/auth/me", s.authed(models.RoleViewer, s.me))
	mux.HandleFunc("/api/v1/auth/password", s.authed(models.RoleViewer, s.changePassword))
	mux.HandleFunc("/api/v1/load-balancers", s.authed(models.RoleViewer, s.handleLoadBalancers))
	mux.HandleFunc("/api/v1/load-balancers/update", s.authed(models.RoleOperator, s.updateLoadBalancer))
	mux.HandleFunc("/api/v1/load-balancers/delete", s.authed(models.RoleOperator, s.deleteLoadBalancer))
	mux.HandleFunc("/api/v1/backends", s.authed(models.RoleViewer, s.getBackends))
	mux.HandleFunc("/api/v1/metrics/overview", s.authed(models.RoleViewer, s.getMetricsOverview))
	mux.HandleFunc("/api/v1/settings", s.authed(models.RoleAdmin, s.getSettings))
	mux.HandleFunc("/api/v1/settings/update", s.authed(models.RoleAdmin, s.updateSettings))
	mux.HandleFunc("/api/v1/settings/telegram/test", s.authed(models.RoleAdmin, s.testTelegram))
	mux.HandleFunc("/api/v1/system", s.authed(models.RoleAdmin, s.getSystem))
	mux.HandleFunc("/api/v1/load-balancers/history", s.authed(models.RoleViewer, s.getLBMetricsHistory)) // Expected query param ?id=X
	mux.HandleFunc("/api/v1/metrics/history", s.authed(models.RoleViewer, s.getGlobalMetricsHistory))
	mux.HandleFunc("/api/v1/metrics/host", s.authed(models.RoleViewer, s.getHostMetrics))
	mux.HandleFunc("/api/v1/logs", s.authed(models.RoleViewer, s.getLogs))
	mux.HandleFunc("/api/v1/tools/test-connection", s.authed(models.RoleOperator, s.testConnection))

	// Metrics
	mux.Handle("/metrics", promhttp.Handler())
	return mux
}

// Start serves the API until ctx is cancelled, then shuts down gracefully.
func (s *Server) Start(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	engine.Logger.Info("API Server listening on " + addr)

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	return false
}

type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

func writeError(w http.ResponseWriter, err error) {
	var se *statusError
	if errors.As(err, &se) {
		http.Error(w, se.msg, se.status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *Server) reloadEngine() {
	// Reload config asynchronously so API returns quickly
	go func() {
		if err := s.engine.ReloadConfig(); err != nil {
			engine.Logger.Error("Failed to reload config: " + err.Error())
		}
	}()
}

func (s *Server) backendStatus(b *models.BackendServer) string {
	if !b.Enabled {
		return "DISABLED"
	}
	if s.engine.GetHealthState(b.ID) {
		return "UP"
	}
	return "DOWN"
}

func (s *Server) getBackends(w http.ResponseWriter, r *http.Request) {
	var backends []models.BackendServer
	if err := s.db.Find(&backends).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range backends {
		backends[i].Metrics = engine.Metrics.GetBackendMetrics(backends[i].ID)
		backends[i].Status = s.backendStatus(&backends[i])
	}

	writeJSON(w, http.StatusOK, backends)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := db.LoadSettings(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings.TelegramBotTokenSet = settings.TelegramBotToken != ""
	settings.TelegramBotToken = ""
	writeJSON(w, http.StatusOK, struct {
		*models.Settings
		TelegramLast *engine.TelegramDelivery `json:"telegram_last,omitempty"` // latest alert since the server started
	}{settings, s.engine.LastTelegram()})
}

// testTelegram sends a test message with the token and chat from the form,
// falling back to the saved ones, and returns Telegram's answer.
func (s *Server) testTelegram(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	var input struct {
		Token  string `json:"telegram_bot_token"`
		ChatID string `json:"telegram_chat_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	settings, err := db.LoadSettings(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token, chatID := strings.TrimSpace(input.Token), strings.TrimSpace(input.ChatID)
	if token == "" {
		token = settings.TelegramBotToken
	}
	if chatID == "" {
		chatID = settings.TelegramChatID
	}
	if token == "" || chatID == "" {
		http.Error(w, "Bot token and chat ID are required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, engine.SendTelegram(token, chatID, "✅ Тестовое сообщение от Balancer. Уведомления работают — сюда придут сообщения о сбоях узлов."))
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost, http.MethodPut) {
		return
	}
	var input models.Settings
	if !decodeJSON(w, r, &input) {
		return
	}

	settings, err := db.LoadSettings(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The token is never sent to the UI, so an empty value means "keep it"
	if input.TelegramBotToken != "" {
		settings.TelegramBotToken = input.TelegramBotToken
	}
	settings.TelegramChatID = input.TelegramChatID
	settings.NotificationsEnabled = input.NotificationsEnabled
	if err := s.db.Model(settings).Select("telegram_bot_token", "telegram_chat_id", "notifications_enabled").Updates(settings).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) getSystem(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(s.info.APIAddr)
	if err != nil {
		host = s.info.APIAddr
	}
	ip := net.ParseIP(host)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":                s.info.Version,
		"api_addr":               s.info.APIAddr,
		"api_loopback":           host == "localhost" || (ip != nil && ip.IsLoopback()),
		"jwt_from_env":           s.info.JWTFromEnv,
		"db_driver":              s.db.Dialector.Name(),
		"default_admin_password": db.HasDefaultAdminPassword(s.db),
		"login_max_failures":     maxLoginFailures,
		"login_lockout_minutes":  int(loginLockout.Minutes()),
	})
}

func (s *Server) getMetricsOverview(w http.ResponseWriter, r *http.Request) {
	var lbs []models.LoadBalancer
	if err := s.db.Preload("BackendGroup.Backends", models.OrderedBackends).Find(&lbs).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	total := 0
	healthy := 0
	for _, lb := range lbs {
		for _, b := range lb.BackendGroup.Backends {
			if !b.Enabled {
				continue
			}
			total++
			if s.engine.GetHealthState(b.ID) {
				healthy++
			}
		}
	}

	writeJSON(w, http.StatusOK, engine.Metrics.GetOverview(healthy, total))
}

func (s *Server) handleLoadBalancers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getLoadBalancers(w, r)
	case http.MethodPost:
		if requireRole(w, r, models.RoleOperator) {
			s.createLoadBalancer(w, r)
		}
	case http.MethodPut:
		if requireRole(w, r, models.RoleOperator) {
			s.updateLoadBalancer(w, r)
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getLoadBalancers(w http.ResponseWriter, r *http.Request) {
	var lbs []models.LoadBalancer
	if err := s.db.Preload("BackendGroup.Backends", models.OrderedBackends).Order("id").Find(&lbs).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	for i := range lbs {
		lb := &lbs[i]
		// Private keys are write-only
		lb.HasKeyData = lb.KeyData != ""
		lb.KeyData = ""

		lb.Metrics = engine.Metrics.GetMetricsForLB(lb.ID)
		for j := range lb.BackendGroup.Backends {
			b := &lb.BackendGroup.Backends[j]
			b.Status = s.backendStatus(b)
			b.Metrics = engine.Metrics.GetBackendMetrics(b.ID)
		}
		lb.ACMEStatus, lb.ACMEError = s.engine.GetACMEStatus(lb.ID)
		lb.Status, lb.StatusError = s.engine.GetLBStatus(lb.ID)
	}

	writeJSON(w, http.StatusOK, lbs)
}

// checkPortConflict rejects a balancer that would bind a socket another one uses.
func checkPortConflict(tx *gorm.DB, lb *models.LoadBalancer) error {
	var others []models.LoadBalancer
	if err := tx.Where("id <> ?", lb.ID).Find(&others).Error; err != nil {
		return err
	}
	for i := range others {
		if port := lb.ConflictsWith(&others[i]); port != 0 {
			return &statusError{http.StatusConflict, fmt.Sprintf("port %d is already used by load balancer %q", port, others[i].Name)}
		}
	}
	return nil
}

func (s *Server) createLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var lb models.LoadBalancer
	if !decodeJSON(w, r, &lb) {
		return
	}

	lb.ID, lb.BackendGroupID, lb.BackendGroup.ID = 0, 0, 0
	for i := range lb.BackendGroup.Backends {
		lb.BackendGroup.Backends[i].ID = 0
		lb.BackendGroup.Backends[i].GroupID = 0
	}
	lb.Normalize()
	if err := lb.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := checkPortConflict(tx, &lb); err != nil {
			return err
		}
		return tx.Create(&lb).Error
	})
	if err != nil {
		writeError(w, err)
		return
	}

	s.reloadEngine()
	writeJSON(w, http.StatusCreated, map[string]uint{"id": lb.ID})
}

func (s *Server) deleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost, http.MethodDelete) {
		return
	}
	id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 32)
	if err != nil {
		http.Error(w, "Missing or invalid id", http.StatusBadRequest)
		return
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		var lb models.LoadBalancer
		if err := tx.Limit(1).Find(&lb, id).Error; err != nil {
			return err
		}
		if lb.ID == 0 {
			return &statusError{http.StatusNotFound, "Load balancer not found"}
		}
		if err := tx.Where("group_id = ?", lb.BackendGroupID).Delete(&models.BackendServer{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.BackendGroup{}, lb.BackendGroupID).Error; err != nil {
			return err
		}
		return tx.Delete(&models.LoadBalancer{}, lb.ID).Error
	})
	if err != nil {
		writeError(w, err)
		return
	}

	s.reloadEngine()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) updateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost, http.MethodPut) {
		return
	}
	var input models.LoadBalancer
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Normalize()
	if err := input.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.LoadBalancer
		if err := tx.Preload("BackendGroup.Backends", models.OrderedBackends).Limit(1).Find(&existing, input.ID).Error; err != nil {
			return err
		}
		if existing.ID == 0 {
			return &statusError{http.StatusNotFound, "Load balancer not found"}
		}
		if err := checkPortConflict(tx, &input); err != nil {
			return err
		}

		// Update LB fields
		existing.Name = input.Name
		existing.ListenIP = input.ListenIP
		existing.ListenPort = input.ListenPort
		existing.Protocol = input.Protocol
		existing.Algorithm = input.Algorithm
		existing.BackupMode = input.BackupMode
		existing.SSLEnabled = input.SSLEnabled
		existing.AutoSSL = input.AutoSSL
		existing.CertPath = input.CertPath
		existing.KeyPath = input.KeyPath
		existing.CertData = input.CertData
		// The UI never receives the key, so an empty key keeps the stored one
		// as long as there is still a certificate to go with it.
		if input.KeyData != "" || input.CertData == "" {
			existing.KeyData = input.KeyData
		}
		existing.ACMEEnabled = input.ACMEEnabled
		existing.ACMEEmail = input.ACMEEmail
		existing.ACMEDomains = input.ACMEDomains
		existing.HTTP3Enabled = input.HTTP3Enabled
		existing.BackendHTTP2Enabled = input.BackendHTTP2Enabled
		existing.ProxyProtocolEnabled = input.ProxyProtocolEnabled
		existing.ProxyProtocolVersion = input.ProxyProtocolVersion
		existing.DNSMode = input.DNSMode
		existing.HTTPRedirect = input.HTTPRedirect
		existing.ConnectTimeout = input.ConnectTimeout
		existing.IdleTimeout = input.IdleTimeout
		existing.ResponseTimeout = input.ResponseTimeout
		existing.StickySessionsEnabled = input.StickySessionsEnabled
		existing.StickySessionType = input.StickySessionType

		// Update backend group
		existing.BackendGroup.Name = input.BackendGroup.Name
		if err := tx.Omit(clause.Associations).Save(&existing.BackendGroup).Error; err != nil {
			return err
		}
		existing.BackendGroupID = existing.BackendGroup.ID
		if err := tx.Omit(clause.Associations).Save(&existing).Error; err != nil {
			return err
		}

		// Sync backends, keeping IDs of existing nodes so health state,
		// metrics and sticky-session cookies survive the edit.
		known := make(map[uint]bool, len(existing.BackendGroup.Backends))
		for _, b := range existing.BackendGroup.Backends {
			known[b.ID] = true
		}
		keep := make([]uint, 0, len(input.BackendGroup.Backends))
		for _, b := range input.BackendGroup.Backends {
			b.GroupID = existing.BackendGroup.ID
			if !known[b.ID] {
				b.ID = 0 // Force insert
			}
			if err := tx.Save(&b).Error; err != nil {
				return err
			}
			keep = append(keep, b.ID)
		}
		return tx.Where("group_id = ? AND id NOT IN ?", existing.BackendGroup.ID, keep).Delete(&models.BackendServer{}).Error
	})
	if err != nil {
		writeError(w, err)
		return
	}

	s.reloadEngine()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	lbName := r.URL.Query().Get("lb_name")

	allLogs := engine.Logger.GetLogs()
	if lbName == "" {
		writeJSON(w, http.StatusOK, allLogs)
		return
	}

	filtered := make([]engine.LogEntry, 0)
	for _, l := range allLogs {
		if l.LBName == lbName {
			filtered = append(filtered, l)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (s *Server) getLBMetricsHistory(w http.ResponseWriter, r *http.Request) {
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		http.Error(w, "Invalid id", http.StatusBadRequest)
		return
	}

	writeJSON(w, http.StatusOK, engine.Metrics.GetMetricsHistoryForLB(uint(id)))
}

func (s *Server) getGlobalMetricsHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, engine.Metrics.GetGlobalMetricsHistory())
}

// getHostMetrics reports CPU, memory and network load of the server itself.
func (s *Server) getHostMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, engine.Host.Stats())
}
