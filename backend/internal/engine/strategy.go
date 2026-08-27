package engine

import (
	"sync"
	"sync/atomic"

	"github.com/balacer/backend/internal/models"
)

type Strategy interface {
	Next(backends []*models.BackendServer) *models.BackendServer
}

// RoundRobin Strategy
type RoundRobin struct {
	counter uint64
}

func (r *RoundRobin) Next(backends []*models.BackendServer) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&r.counter, 1) % uint64(len(backends))
	return backends[idx]
}

type LeastConnections struct {
	connections sync.Map // map[*models.BackendServer]*int64
}

func (l *LeastConnections) Next(backends []*models.BackendServer) *models.BackendServer {
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

func (w *WeightedRoundRobin) Next(backends []*models.BackendServer) *models.BackendServer {
	// ... implementation
	return backends[0]
}

