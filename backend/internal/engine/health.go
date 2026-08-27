package engine

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/balacer/backend/internal/models"
	"gorm.io/gorm"
)

type HealthChecker struct {
	db     *gorm.DB
	cancel context.CancelFunc
	mu     sync.RWMutex
}

func NewHealthChecker(db *gorm.DB) *HealthChecker {
	return &HealthChecker{db: db}
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
	target := fmt.Sprintf("%s:%d", backend.Address, backend.Port)
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
	if !isUp {
		log.Printf("HealthCheck FAILED for %s (%s)", backend.Name, target)
	} else {
		// log.Printf("HealthCheck OK for %s (%s)", backend.Name, target)
	}
}
