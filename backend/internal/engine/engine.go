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
	e.healthChecker.Start()
	e.healthChecker.SetOnChange(func() {
		e.mu.RLock()
		defer e.mu.RUnlock()
		for _, inst := range e.activeBalancers {
			inst.updateBackends()
		}
	})
	return e.ReloadConfig()
}

func (e *Engine) ReloadConfig() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	var lbs []models.LoadBalancer
	if err := e.db.Preload("BackendGroup.Backends").Find(&lbs).Error; err != nil {
		return err
	}

	// For simplicity in this version, we will stop all and start new ones
	// A production zero-downtime would swap routing tables inside existing listeners.
	for _, inst := range e.activeBalancers {
		inst.Stop()
	}
	e.activeBalancers = make(map[uint]*LoadBalancerInstance)

	for _, lb := range lbs {
		inst := NewLoadBalancerInstance(lb, e.db, e.healthChecker)
		e.activeBalancers[lb.ID] = inst
		go func(l models.LoadBalancer, instance *LoadBalancerInstance) {
			if err := instance.Start(); err != nil {
				Logger.Error(fmt.Sprintf("Failed to start LB %s: %v", l.Name, err))
			}
		}(lb, inst)
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
