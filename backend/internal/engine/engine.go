package engine

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/balancer/backend/internal/models"
	"gorm.io/gorm"
)

type Engine struct {
	db             *gorm.DB
	mu             sync.RWMutex
	activeBalancers map[uint]*LoadBalancerInstance
	healthChecker  *HealthChecker
}

func NewEngine(db *gorm.DB) *Engine {
	return &Engine{
		db:             db,
		activeBalancers: make(map[uint]*LoadBalancerInstance),
		healthChecker:  NewHealthChecker(db),
	}
}

func (e *Engine) Start() error {
	Logger.Info("Starting Engine...")
	e.healthChecker.Start()
	e.healthChecker.SetOnChange(func() {
		e.mu.RLock()
		defer e.mu.RUnlock()
		for _, inst := range e.activeBalancers {
			inst.updateBackends()
		}
	})
	Metrics.StartSnapshotLoop()
	return e.ReloadConfig()
}

func (e *Engine) ReloadConfig() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var lbs []models.LoadBalancer
	if err := e.db.Preload("BackendGroup.Backends").Find(&lbs).Error; err != nil {
		return err
	}

	currentLBs := make(map[uint]bool)

	for _, lb := range lbs {
		currentLBs[lb.ID] = true
		inst, exists := e.activeBalancers[lb.ID]

		needsRestart := false
		if !exists {
			needsRestart = true
		} else if lb.UpdatedAt.Unix() != inst.Config.UpdatedAt.Unix() {
			// Configuration changed (updated in DB), so we restart this specific balancer
			needsRestart = true
		}

		if needsRestart {
			if exists {
				inst.Stop()
				time.Sleep(100 * time.Millisecond) // brief wait for port release
			}
			newInst := NewLoadBalancerInstance(lb, e.db, e.healthChecker)
			e.activeBalancers[lb.ID] = newInst
			go func(l models.LoadBalancer, instance *LoadBalancerInstance) {
				if err := instance.Start(); err != nil {
					if err == http.ErrServerClosed || strings.Contains(err.Error(), "use of closed network connection") {
						return
					}
					Logger.Error(fmt.Sprintf("Failed to start LB %s: %v", l.Name, err))
				}
			}(lb, newInst)
		}
	}

	// Stop deleted LBs
	for id, inst := range e.activeBalancers {
		if !currentLBs[id] {
			inst.Stop()
			delete(e.activeBalancers, id)
		}
	}

	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, inst := range e.activeBalancers {
		inst.Stop()
	}
}

func (e *Engine) GetHealthState(backendID uint) bool {
    return e.healthChecker.IsHealthy(backendID)
}

func (e *Engine) GetACMEStatus(lbID uint) (status, errStr string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	
	if inst, ok := e.activeBalancers[lbID]; ok {
		return inst.GetACMEStatus(), inst.GetACMEError()
	}
	return "", ""
}
