package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/balancer/backend/internal/models"
	"gorm.io/gorm"
)

type HealthChecker struct {
	db     *gorm.DB
	cancel context.CancelFunc
	mu     sync.RWMutex
	states map[uint]bool
	onChange func() // callback when health state changes
}

func NewHealthChecker(db *gorm.DB) *HealthChecker {
	return &HealthChecker{
		db:     db,
		states: make(map[uint]bool),
	}
}

func (hc *HealthChecker) SetOnChange(fn func()) {
	hc.onChange = fn
}

func (hc *HealthChecker) IsHealthy(backendID uint) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	state, exists := hc.states[backendID]
	if !exists {
		return true // assume healthy if not yet checked
	}
	return state
}

func (hc *HealthChecker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	hc.cancel = cancel

	go hc.loop(ctx)
}

func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
}

func (hc *HealthChecker) loop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second) // Default global interval
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.checkAll()
		}
	}
}

func (hc *HealthChecker) checkAll() {
	var groups []models.BackendGroup
	if err := hc.db.Preload("Backends").Find(&groups).Error; err != nil {
		log.Printf("HealthChecker DB error: %v", err)
		return
	}

	for _, group := range groups {
		if !group.HCEnabled {
			continue
		}
		
		for _, backend := range group.Backends {
			if !backend.Enabled {
				continue
			}

			// In a real implementation we would track state across ticks
			// For this MVP, we perform synchronous checks just to show architecture
			go hc.checkBackend(group, backend)
		}
	}
}

func (hc *HealthChecker) checkBackend(group models.BackendGroup, backend models.BackendServer) {
	port := backend.Port
	if group.HCPort > 0 {
		port = group.HCPort
	}
	target := fmt.Sprintf("%s:%d", backend.Address, port)
	timeout := time.Duration(group.HCTimeout) * time.Second
	if timeout == 0 {
		timeout = 2 * time.Second
	}

	isUp := false

	if group.HCProtocol == "http" || group.HCProtocol == "https" {
		scheme := group.HCProtocol
		path := group.HCPath
		if path == "" {
			path = "/"
		}
		url := fmt.Sprintf("%s://%s%s", scheme, target, path)
		
		client := http.Client{Timeout: timeout}
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			isUp = true
		}
		if resp != nil {
			resp.Body.Close()
		}
	} else { // TCP fallback
		conn, err := net.DialTimeout("tcp", target, timeout)
		if err == nil {
			isUp = true
			conn.Close()
		}
	}

	// Update logic goes here (Trigger Engine Reload/Update active backends list)
	// Usually this is done via channels or atomic pointers to avoid locks.
	
	hc.mu.Lock()
	prevState, exists := hc.states[backend.ID]
	if !exists {
		prevState = true // Assume it was up initially to avoid spamming on start if it's dead
	}
	
	if isUp != prevState {
		hc.states[backend.ID] = isUp
		hc.mu.Unlock()
		
		statusStr := "UP 🟢"
		if !isUp {
			statusStr = "DOWN 🔴"
			log.Printf("HealthCheck FAILED for %s (%s)", backend.Name, target)
		} else {
			log.Printf("HealthCheck OK for %s (%s)", backend.Name, target)
		}
		
		msg := fmt.Sprintf("Backend **%s** (%s) is now %s", backend.Name, target, statusStr)
		hc.sendTelegramAlert(msg)
		if hc.onChange != nil {
			go hc.onChange()
		}
	} else {
		hc.mu.Unlock()
	}
}

func (hc *HealthChecker) sendTelegramAlert(message string) {
	var settings models.Settings
	if err := hc.db.First(&settings).Error; err != nil {
		return
	}
	
	if !settings.NotificationsEnabled || settings.TelegramBotToken == "" || settings.TelegramChatID == "" {
		return
	}
	
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", settings.TelegramBotToken)
	payload := map[string]interface{}{
		"chat_id":    settings.TelegramChatID,
		"text":       message,
		"parse_mode": "Markdown",
	}
	
	data, _ := json.Marshal(payload)
	go func() {
		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewBuffer(data))
		if err == nil && resp != nil {
			resp.Body.Close()
		}
	}()
}
