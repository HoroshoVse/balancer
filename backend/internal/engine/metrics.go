package engine

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsSnapshot struct {
	Timestamp      string  `json:"timestamp"`
	RPS            float64 `json:"rps"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	ErrorRate      float64 `json:"error_rate"`
}

type LBMetrics struct {
	TotalRequests uint64
	TotalErrors   uint64
	TotalLatency  uint64 // In milliseconds

	mu            sync.RWMutex
	History       []MetricsSnapshot
	lastRequests  uint64
	lastErrors    uint64
	lastLatency   uint64
	lastSnapshot  time.Time
}

type MetricsRegistry struct {
	lbs sync.Map // map[uint]*LBMetrics
}

var (
	Metrics = &MetricsRegistry{}

	promTotalRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "balancer_requests_total",
			Help: "Total number of requests processed by the load balancer",
		},
		[]string{"lb_id"},
	)

	promTotalErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "balancer_errors_total",
			Help: "Total number of failed requests",
		},
		[]string{"lb_id"},
	)

	promRequestLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "balancer_request_duration_seconds",
			Help:    "Latency of requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"lb_id"},
	)
)

func (m *MetricsRegistry) getOrCreate(lbID uint) *LBMetrics {
	val, ok := m.lbs.Load(lbID)
	if ok {
		return val.(*LBMetrics)
	}
	newMetrics := &LBMetrics{}
	actual, _ := m.lbs.LoadOrStore(lbID, newMetrics)
	return actual.(*LBMetrics)
}

func (m *MetricsRegistry) RecordRequest(lbID uint, latency time.Duration, isError bool) {
	lbMetrics := m.getOrCreate(lbID)
	atomic.AddUint64(&lbMetrics.TotalRequests, 1)
	atomic.AddUint64(&lbMetrics.TotalLatency, uint64(latency.Milliseconds()))
	
	idStr := strconv.Itoa(int(lbID))
	promTotalRequests.WithLabelValues(idStr).Inc()
	promRequestLatency.WithLabelValues(idStr).Observe(latency.Seconds())
	
	if isError {
		atomic.AddUint64(&lbMetrics.TotalErrors, 1)
		promTotalErrors.WithLabelValues(idStr).Inc()
	}
}

func (m *MetricsRegistry) GetOverview(healthyBackends, totalBackends int) map[string]interface{} {
	var totalReq, totalErr, totalLat uint64
	
	m.lbs.Range(func(key, value interface{}) bool {
		lbm := value.(*LBMetrics)
		totalReq += atomic.LoadUint64(&lbm.TotalRequests)
		totalErr += atomic.LoadUint64(&lbm.TotalErrors)
		totalLat += atomic.LoadUint64(&lbm.TotalLatency)
		return true
	})

	avgLatency := uint64(0)
	if totalReq > 0 {
		avgLatency = totalLat / totalReq
	}

	errorRate := float64(0)
	if totalReq > 0 {
		errorRate = float64(totalErr) / float64(totalReq) * 100.0
	}

	return map[string]interface{}{
		"total_requests":     totalReq,
		"healthy_backends":   healthyBackends,
		"total_backends":     totalBackends,
		"average_latency_ms": avgLatency,
		"error_rate_percent": errorRate,
	}
}

func (m *MetricsRegistry) GetMetricsForLB(lbID uint) map[string]interface{} {
	lbm := m.getOrCreate(lbID)
	req := atomic.LoadUint64(&lbm.TotalRequests)
	errs := atomic.LoadUint64(&lbm.TotalErrors)
	lat := atomic.LoadUint64(&lbm.TotalLatency)
	
	avg := uint64(0)
	if req > 0 { avg = lat / req }
	
	errRate := float64(0)
	if req > 0 { errRate = float64(errs) / float64(req) * 100.0 }
	
	return map[string]interface{}{
		"total_requests": req,
		"average_latency_ms": avg,
		"error_rate_percent": errRate,
	}
}

func (m *MetricsRegistry) GetMetricsHistoryForLB(lbID uint) []MetricsSnapshot {
	lbm := m.getOrCreate(lbID)
	lbm.mu.RLock()
	defer lbm.mu.RUnlock()
	historyCopy := make([]MetricsSnapshot, len(lbm.History))
	copy(historyCopy, lbm.History)
	return historyCopy
}

func (m *MetricsRegistry) StartSnapshotLoop() {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			now := time.Now()
			m.lbs.Range(func(key, value interface{}) bool {
				lbm := value.(*LBMetrics)
				
				req := atomic.LoadUint64(&lbm.TotalRequests)
				errs := atomic.LoadUint64(&lbm.TotalErrors)
				lat := atomic.LoadUint64(&lbm.TotalLatency)

				lbm.mu.Lock()
				
				var rps float64 = 0
				var avgLat float64 = 0
				var errRate float64 = 0

				deltaReq := req - lbm.lastRequests
				deltaErr := errs - lbm.lastErrors
				deltaLat := lat - lbm.lastLatency
				
				timeDelta := now.Sub(lbm.lastSnapshot).Seconds()
				if lbm.lastSnapshot.IsZero() {
					timeDelta = 5.0
				}
				
				if timeDelta > 0 && deltaReq > 0 {
					rps = float64(deltaReq) / timeDelta
					avgLat = float64(deltaLat) / float64(deltaReq)
					errRate = float64(deltaErr) / float64(deltaReq) * 100.0
				}

				snap := MetricsSnapshot{
					Timestamp:    now.Format("15:04:05"),
					RPS:          rps,
					AvgLatencyMs: avgLat,
					ErrorRate:    errRate,
				}

				lbm.History = append(lbm.History, snap)
				if len(lbm.History) > 60 {
					lbm.History = lbm.History[len(lbm.History)-60:] // keep last 60
				}

				lbm.lastRequests = req
				lbm.lastErrors = errs
				lbm.lastLatency = lat
				lbm.lastSnapshot = now

				lbm.mu.Unlock()
				return true
			})
		}
	}()
}
