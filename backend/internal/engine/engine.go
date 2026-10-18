package engine

import (
	"fmt"
	"sync"

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
	e.healthChecker.SetOnChange(func() {
		e.mu.RLock()
		defer e.mu.RUnlock()
		for _, inst := range e.activeBalancers {
			inst.updateBackends()
		}
	})
	e.healthChecker.Start()
	Metrics.StartSnapshotLoop()
	Host.Start()
	return e.ReloadConfig()
}

// ReloadConfig syncs running balancers with the database: new and changed
// balancers (and ones that failed to start) are restarted, deleted ones stopped.
func (e *Engine) ReloadConfig() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var lbs []models.LoadBalancer
	if err := e.db.Preload("BackendGroup.Backends").Find(&lbs).Error; err != nil {
		return err
	}

	currentLBs := make(map[uint]bool, len(lbs))
	currentBackends := make(map[uint]bool)
	var targets []HealthTarget
	for _, lb := range lbs {
		currentLBs[lb.ID] = true
		for _, b := range lb.BackendGroup.Backends {
			currentBackends[b.ID] = true
			targets = append(targets, HealthTarget{Backend: b, LBName: lb.Name})
		}
	}
	e.healthChecker.SetTargets(targets)

	// Phase 1: stop deleted and changed balancers so their ports are free,
	// even if another balancer now wants one of them.
	var toStart []models.LoadBalancer
	for _, lb := range lbs {
		inst, exists := e.activeBalancers[lb.ID]
		if exists {
			status, _ := inst.Status()
			if lb.UpdatedAt.Equal(inst.Config.UpdatedAt) && status != statusError {
				continue
			}
			inst.Stop()
		}
		toStart = append(toStart, lb)
	}
	for id, inst := range e.activeBalancers {
		if !currentLBs[id] {
			inst.Stop()
			delete(e.activeBalancers, id)
		}
	}

	// Phase 2: start
	for _, lb := range toStart {
		newInst := NewLoadBalancerInstance(lb, e.healthChecker)
		e.activeBalancers[lb.ID] = newInst
		go func(name string, instance *LoadBalancerInstance) {
			if err := instance.Start(); err != nil {
				Logger.ErrorLB(name, fmt.Sprintf("Failed to start LB %s: %v", name, err))
			}
		}(lb.Name, newInst)
	}

	Metrics.Prune(currentLBs, currentBackends)
	return nil
}

func (e *Engine) Stop() {
	e.healthChecker.Stop()
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

// GetLBStatus returns the runtime state of a balancer and its start error.
func (e *Engine) GetLBStatus(lbID uint) (status, errStr string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if inst, ok := e.activeBalancers[lbID]; ok {
		return inst.Status()
	}
	return statusStopped, ""
}
