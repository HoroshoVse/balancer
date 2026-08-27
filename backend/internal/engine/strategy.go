package engine

import (
	"sync"
	"sync/atomic"

	"github.com/balancer/backend/internal/models"
)

type Strategy interface {
	Next(backends []*models.BackendServer, clientIP string) *models.BackendServer
}

// RoundRobin Strategy
type RoundRobin struct {
	counter uint64
}

func (r *RoundRobin) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&r.counter, 1) % uint64(len(backends))
	return backends[idx]
}

type LeastConnections struct {
	connections sync.Map // map[*models.BackendServer]*int64
}

func (l *LeastConnections) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}

	var bestBackend *models.BackendServer
	var minConns int64 = -1

	for _, b := range backends {
		val, _ := l.connections.LoadOrStore(b, new(int64))
		conns := atomic.LoadInt64(val.(*int64))

		if minConns == -1 || conns < minConns {
			minConns = conns
			bestBackend = b
		}
	}

	if bestBackend != nil {
		val, _ := l.connections.Load(bestBackend)
		atomic.AddInt64(val.(*int64), 1)
	}

	return bestBackend
}

// CompleteConnection should be called when a connection to a backend is closed.
// This is specific to strategies that track connection state.
func (l *LeastConnections) CompleteConnection(backend *models.BackendServer) {
	if val, ok := l.connections.Load(backend); ok {
		atomic.AddInt64(val.(*int64), -1)
	}
}

// WeightedRoundRobin Strategy
type WeightedRoundRobin struct {
	current uint64
}

func (w *WeightedRoundRobin) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}
	
	// Calculate total weight
	var totalWeight int
	for _, b := range backends {
		weight := b.Weight
		if weight <= 0 {
			weight = 1
		}
		totalWeight += weight
	}

	idx := atomic.AddUint64(&w.current, 1) % uint64(totalWeight)
	
	var currentWeight uint64
	for _, b := range backends {
		weight := b.Weight
		if weight <= 0 {
			weight = 1
		}
		currentWeight += uint64(weight)
		if idx < currentWeight {
			return b
		}
	}
	
	return backends[0]
}

// IPHash Strategy
type IPHash struct{}

func (h *IPHash) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}
	
	var hash uint32 = 0
	for i := 0; i < len(clientIP); i++ {
		hash = hash*31 + uint32(clientIP[i])
	}
	
	idx := hash % uint32(len(backends))
	return backends[idx]
}

// Failover Strategy (Active-Passive)
// Always returns the first backend in the list.
// Combined with the updateBackends() failover logic, this means:
// - Traffic goes to the first healthy primary node
// - If all primaries are down, traffic goes to the first healthy backup node
// - When a primary recovers, traffic returns to it automatically
type Failover struct{}

func (f *Failover) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}
	return backends[0]
}

