package engine

import (
	"hash/fnv"
	"sync"
	"sync/atomic"

	"github.com/balancer/backend/internal/models"
)

type Strategy interface {
	Next(backends []*models.BackendServer, clientIP string) *models.BackendServer
}

// connCounter tracks active connections per backend ID.
type connCounter struct {
	m sync.Map // map[uint]*int64
}

func (c *connCounter) ptr(id uint) *int64 {
	val, _ := c.m.LoadOrStore(id, new(int64))
	return val.(*int64)
}

// Active returns the number of open connections to a backend.
func (c *connCounter) Active(id uint) int64 {
	return atomic.LoadInt64(c.ptr(id))
}

// tryAcquire reserves a connection slot unless the backend is at max (0 = unlimited).
func (c *connCounter) tryAcquire(id uint, max int) bool {
	p := c.ptr(id)
	for {
		cur := atomic.LoadInt64(p)
		if max > 0 && cur >= int64(max) {
			return false
		}
		if atomic.CompareAndSwapInt64(p, cur, cur+1) {
			return true
		}
	}
}

func (c *connCounter) release(id uint) {
	atomic.AddInt64(c.ptr(id), -1)
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

// LeastConnections picks the backend with the fewest active connections,
// rotating the starting point so ties are spread evenly.
type LeastConnections struct {
	conns   *connCounter
	counter uint64
}

func (l *LeastConnections) Next(backends []*models.BackendServer, clientIP string) *models.BackendServer {
	if len(backends) == 0 {
		return nil
	}

	start := int(atomic.AddUint64(&l.counter, 1) % uint64(len(backends)))
	var bestBackend *models.BackendServer
	var minConns int64 = -1

	for i := range backends {
		b := backends[(start+i)%len(backends)]
		conns := l.conns.Active(b.ID)
		if minConns == -1 || conns < minConns {
			minConns = conns
			bestBackend = b
		}
	}

	return bestBackend
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
	return backends[hashIP(clientIP)%uint32(len(backends))]
}

func hashIP(clientIP string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(clientIP))
	return h.Sum32()
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

func newStrategy(algorithm string, conns *connCounter) Strategy {
	switch algorithm {
	case "least_conn":
		return &LeastConnections{conns: conns}
	case "weighted_round_robin":
		return &WeightedRoundRobin{}
	case "ip_hash":
		return &IPHash{}
	case "failover":
		return &Failover{}
	default:
		return &RoundRobin{}
	}
}
