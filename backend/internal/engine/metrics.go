package engine

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsSnapshot struct {
	Timestamp      string  `json:"timestamp"` // HH:MM:SS, kept for compatibility
	TS             int64   `json:"ts"`        // unix milliseconds
	RPS            float64 `json:"rps"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	ErrorRate      float64 `json:"error_rate"`
}

type LBMetrics struct {
	TotalRequests  uint64
	TotalErrors    uint64
	TotalLatencyUs uint64 // In microseconds
	TotalBlocked   uint64   // connections refused by the access rules
	blockedBy      sync.Map // country code ("" = none) -> *uint64

	mu            sync.RWMutex
	History       []MetricsSnapshot
	lastRequests  uint64
	lastErrors    uint64
	lastLatency   uint64
	lastSnapshot  time.Time
}

type BackendMetrics struct {
	TotalRequests  uint64
	TotalErrors    uint64
	TotalLatencyUs uint64 // In microseconds

	mu           sync.RWMutex
	lastRequests uint64
	lastErrors   uint64
	lastLatency  uint64
	lastSnapshot time.Time

	currentRPS     float64
	currentAvgLat  float64
	currentErrRate float64
}

type MetricsRegistry struct {
	lbs      sync.Map // map[uint]*LBMetrics
	backends sync.Map // map[uint]*BackendMetrics
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

	promBlocked = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "balancer_blocked_total",
			Help: "Connections refused by the access rules, by reason",
		},
		[]string{"lb_id", "reason"},
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

func (m *MetricsRegistry) getOrCreateBackend(backendID uint) *BackendMetrics {
	val, ok := m.backends.Load(backendID)
	if ok {
		return val.(*BackendMetrics)
	}
	newMetrics := &BackendMetrics{}
	actual, _ := m.backends.LoadOrStore(backendID, newMetrics)
	return actual.(*BackendMetrics)
}

func (m *MetricsRegistry) RecordRequest(lbID uint, latency time.Duration, isError bool) {
	lbMetrics := m.getOrCreate(lbID)
	atomic.AddUint64(&lbMetrics.TotalRequests, 1)
	atomic.AddUint64(&lbMetrics.TotalLatencyUs, uint64(latency.Microseconds()))

	idStr := strconv.Itoa(int(lbID))
	promTotalRequests.WithLabelValues(idStr).Inc()
	promRequestLatency.WithLabelValues(idStr).Observe(latency.Seconds())

	if isError {
		atomic.AddUint64(&lbMetrics.TotalErrors, 1)
		promTotalErrors.WithLabelValues(idStr).Inc()
	}
}

// RecordBlocked counts a connection refused by the access rules.
func (m *MetricsRegistry) RecordBlocked(lbID uint, country, reason string) {
	lbm := m.getOrCreate(lbID)
	atomic.AddUint64(&lbm.TotalBlocked, 1)
	v, ok := lbm.blockedBy.Load(country)
	if !ok {
		v, _ = lbm.blockedBy.LoadOrStore(country, new(uint64))
	}
	atomic.AddUint64(v.(*uint64), 1)
	promBlocked.WithLabelValues(strconv.Itoa(int(lbID)), reason).Inc()
}

func (m *MetricsRegistry) RecordBackendRequest(backendID uint, latency time.Duration, isError bool) {
	bm := m.getOrCreateBackend(backendID)
	atomic.AddUint64(&bm.TotalRequests, 1)
	atomic.AddUint64(&bm.TotalLatencyUs, uint64(latency.Microseconds()))

	if isError {
		atomic.AddUint64(&bm.TotalErrors, 1)
	}
}

// Prune drops metrics of balancers and backends that no longer exist.
func (m *MetricsRegistry) Prune(lbIDs, backendIDs map[uint]bool) {
	m.lbs.Range(func(key, _ interface{}) bool {
		if id := key.(uint); !lbIDs[id] {
			m.lbs.Delete(id)
			idStr := strconv.Itoa(int(id))
			promTotalRequests.DeleteLabelValues(idStr)
			promTotalErrors.DeleteLabelValues(idStr)
			promRequestLatency.DeleteLabelValues(idStr)
		}
		return true
	})
	m.backends.Range(func(key, _ interface{}) bool {
		if id := key.(uint); !backendIDs[id] {
			m.backends.Delete(id)
		}
		return true
	})
}

func avgMs(totalUs, count uint64) float64 {
	if count == 0 {
		return 0
	}
	return float64(totalUs) / float64(count) / 1000.0
}

func (m *MetricsRegistry) GetOverview(healthyBackends, totalBackends int) map[string]interface{} {
	var totalReq, totalErr, totalLat uint64

	m.lbs.Range(func(key, value interface{}) bool {
		lbm := value.(*LBMetrics)
		totalReq += atomic.LoadUint64(&lbm.TotalRequests)
		totalErr += atomic.LoadUint64(&lbm.TotalErrors)
		totalLat += atomic.LoadUint64(&lbm.TotalLatencyUs)
		return true
	})

	errorRate := float64(0)
	if totalReq > 0 {
		errorRate = float64(totalErr) / float64(totalReq) * 100.0
	}

	return map[string]interface{}{
		"total_requests":     totalReq,
		"healthy_backends":   healthyBackends,
		"total_backends":     totalBackends,
		"average_latency_ms": avgMs(totalLat, totalReq),
		"error_rate_percent": errorRate,
	}
}

func (m *MetricsRegistry) GetMetricsForLB(lbID uint) map[string]interface{} {
	lbm := m.getOrCreate(lbID)
	req := atomic.LoadUint64(&lbm.TotalRequests)
	errs := atomic.LoadUint64(&lbm.TotalErrors)
	lat := atomic.LoadUint64(&lbm.TotalLatencyUs)

	errRate := float64(0)
	if req > 0 { errRate = float64(errs) / float64(req) * 100.0 }

	out := map[string]interface{}{
		"total_requests": req,
		"average_latency_ms": avgMs(lat, req),
		"error_rate_percent": errRate,
	}
	if blocked := atomic.LoadUint64(&lbm.TotalBlocked); blocked > 0 {
		out["blocked"] = blocked
		out["blocked_top"] = lbm.topBlocked(5)
	}
	return out
}

func (m *MetricsRegistry) GetMetricsHistoryForLB(lbID uint) []MetricsSnapshot {
	lbm := m.getOrCreate(lbID)
	lbm.mu.RLock()
	defer lbm.mu.RUnlock()
	historyCopy := make([]MetricsSnapshot, len(lbm.History))
	copy(historyCopy, lbm.History)
	return historyCopy
}

// GetGlobalMetricsHistory sums RPS across balancers; latency and error
// rate are averaged weighted by each balancer's RPS.
func (m *MetricsRegistry) GetGlobalMetricsHistory() []MetricsSnapshot {
	type aggData struct {
		Timestamp string
		TotalRPS  float64
		WeightedLat float64
		WeightedErr float64
	}

	aggMap := make(map[int64]*aggData)

	m.lbs.Range(func(key, value interface{}) bool {
		lbm := value.(*LBMetrics)
		lbm.mu.RLock()
		for _, snap := range lbm.History {
			a, exists := aggMap[snap.TS]
			if !exists {
				a = &aggData{Timestamp: snap.Timestamp}
				aggMap[snap.TS] = a
			}
			a.TotalRPS += snap.RPS
			a.WeightedLat += snap.RPS * snap.AvgLatencyMs
			a.WeightedErr += snap.RPS * snap.ErrorRate
		}
		lbm.mu.RUnlock()
		return true
	})

	result := make([]MetricsSnapshot, 0, len(aggMap))
	for ts, a := range aggMap {
		snap := MetricsSnapshot{Timestamp: a.Timestamp, TS: ts, RPS: a.TotalRPS}
		if a.TotalRPS > 0 {
			snap.AvgLatencyMs = a.WeightedLat / a.TotalRPS
			snap.ErrorRate = a.WeightedErr / a.TotalRPS
		}
		result = append(result, snap)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].TS < result[j].TS })
	return result
}

func (m *MetricsRegistry) GetBackendMetrics(backendID uint) map[string]interface{} {
	bm := m.getOrCreateBackend(backendID)
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	return map[string]interface{}{
		"rps":            bm.currentRPS,
		"avg_latency_ms": bm.currentAvgLat,
		"error_rate":     bm.currentErrRate,
		"total_requests": atomic.LoadUint64(&bm.TotalRequests),
	}
}

// windowStats computes rate, average latency (ms) and error rate (%) since the last snapshot.
func windowStats(deltaReq, deltaErr, deltaLatUs uint64, seconds float64) (rps, avgLat, errRate float64) {
	if seconds <= 0 || deltaReq == 0 {
		return 0, 0, 0
	}
	return float64(deltaReq) / seconds,
		float64(deltaLatUs) / float64(deltaReq) / 1000.0,
		float64(deltaErr) / float64(deltaReq) * 100.0
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
				lat := atomic.LoadUint64(&lbm.TotalLatencyUs)

				lbm.mu.Lock()

				timeDelta := now.Sub(lbm.lastSnapshot).Seconds()
				if lbm.lastSnapshot.IsZero() {
					timeDelta = 5.0
				}
				rps, avgLat, errRate := windowStats(req-lbm.lastRequests, errs-lbm.lastErrors, lat-lbm.lastLatency, timeDelta)

				snap := MetricsSnapshot{
					Timestamp:    now.Format("15:04:05"),
					TS:           now.UnixMilli(),
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

			m.backends.Range(func(key, value interface{}) bool {
				bm := value.(*BackendMetrics)

				req := atomic.LoadUint64(&bm.TotalRequests)
				errs := atomic.LoadUint64(&bm.TotalErrors)
				lat := atomic.LoadUint64(&bm.TotalLatencyUs)

				bm.mu.Lock()

				timeDelta := now.Sub(bm.lastSnapshot).Seconds()
				if bm.lastSnapshot.IsZero() {
					timeDelta = 5.0
				}
				bm.currentRPS, bm.currentAvgLat, bm.currentErrRate = windowStats(req-bm.lastRequests, errs-bm.lastErrors, lat-bm.lastLatency, timeDelta)

				bm.lastRequests = req
				bm.lastErrors = errs
				bm.lastLatency = lat
				bm.lastSnapshot = now

				bm.mu.Unlock()
				return true
			})
		}
	}()
}

type blockedCountry struct {
	Country string `json:"country"` // "" when the address has none
	Count   uint64 `json:"count"`
}

// topBlocked lists the countries most refused connections came from.
func (lbm *LBMetrics) topBlocked(n int) []blockedCountry {
	var all []blockedCountry
	lbm.blockedBy.Range(func(k, v any) bool {
		all = append(all, blockedCountry{k.(string), atomic.LoadUint64(v.(*uint64))})
		return true
	})
	sort.Slice(all, func(i, j int) bool { return all[i].Count > all[j].Count || all[i].Count == all[j].Count && all[i].Country < all[j].Country })
	if len(all) > n {
		all = all[:n]
	}
	return all
}
