package engine

import (
	"sync"
	"sync/atomic"
	"time"
)

type LBMetrics struct {
	TotalRequests uint64
	TotalErrors   uint64
	TotalLatency  uint64 // In milliseconds
}

type MetricsRegistry struct {
	lbs sync.Map // map[uint]*LBMetrics
}

var Metrics = &MetricsRegistry{}

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
	
	if isError {
		atomic.AddUint64(&lbMetrics.TotalErrors, 1)
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
