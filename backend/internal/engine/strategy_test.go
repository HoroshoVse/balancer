package engine

import (
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

func backendsWithWeights(weights ...int) []*models.BackendServer {
	out := make([]*models.BackendServer, len(weights))
	for i, w := range weights {
		out[i] = &models.BackendServer{ID: uint(i + 1), Weight: w, Enabled: true}
	}
	return out
}

func countPicks(s Strategy, backends []*models.BackendServer, n int) map[uint]int {
	counts := make(map[uint]int)
	for i := 0; i < n; i++ {
		counts[s.Next(backends, "10.0.0.1").ID]++
	}
	return counts
}

func TestRoundRobinSpreadsEvenly(t *testing.T) {
	counts := countPicks(&RoundRobin{}, backendsWithWeights(1, 1, 1), 300)
	for id, c := range counts {
		if c != 100 {
			t.Errorf("backend %d got %d picks, want 100", id, c)
		}
	}
}

func TestWeightedRoundRobinFollowsWeights(t *testing.T) {
	counts := countPicks(&WeightedRoundRobin{}, backendsWithWeights(3, 1), 400)
	if counts[1] != 300 || counts[2] != 100 {
		t.Errorf("got %v, want 300/100", counts)
	}
}

func TestLeastConnectionsPicksFewestAndRotatesTies(t *testing.T) {
	var conns connCounter
	lc := &LeastConnections{conns: &conns}
	backends := backendsWithWeights(1, 1, 1)

	// All idle: ties must not always land on the first backend
	counts := countPicks(lc, backends, 30)
	if len(counts) != 3 {
		t.Errorf("idle picks went to %v, want all three backends", counts)
	}

	conns.tryAcquire(1, 0)
	conns.tryAcquire(1, 0)
	conns.tryAcquire(3, 0)
	for i := 0; i < 10; i++ {
		if got := lc.Next(backends, "").ID; got != 2 {
			t.Fatalf("picked %d, want backend 2 with no connections", got)
		}
	}
}

func TestIPHashIsStable(t *testing.T) {
	backends := backendsWithWeights(1, 1, 1, 1)
	h := &IPHash{}
	first := h.Next(backends, "192.0.2.10").ID
	for i := 0; i < 20; i++ {
		if got := h.Next(backends, "192.0.2.10").ID; got != first {
			t.Fatalf("same IP mapped to %d and %d", first, got)
		}
	}
}

func newTestInstance(cfg models.LoadBalancer) *LoadBalancerInstance {
	inst := NewLoadBalancerInstance(cfg, nil)
	inst.updateBackends()
	return inst
}

func TestPickBackendRespectsMaxConns(t *testing.T) {
	inst := newTestInstance(models.LoadBalancer{
		Name: "max", Algorithm: "round_robin",
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 1, Enabled: true, MaxConns: 1},
			{ID: 2, Enabled: true, MaxConns: 1},
		}},
	})

	a := inst.pickBackend("", 0)
	b := inst.pickBackend("", 0)
	if a == nil || b == nil || a.ID == b.ID {
		t.Fatalf("expected two different backends, got %v and %v", a, b)
	}
	if c := inst.pickBackend("", 0); c != nil {
		t.Fatalf("both backends are at max_conns, but got backend %d", c.ID)
	}
	inst.releaseBackend(a)
	if c := inst.pickBackend("", 0); c == nil || c.ID != a.ID {
		t.Fatalf("after release expected backend %d, got %v", a.ID, c)
	}
}

func TestPickBackendStickiness(t *testing.T) {
	cfg := models.LoadBalancer{
		Name: "sticky", Algorithm: "round_robin",
		StickySessionsEnabled: true, StickySessionType: "ip",
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 1, Enabled: true}, {ID: 2, Enabled: true}, {ID: 3, Enabled: true},
		}},
	}
	inst := newTestInstance(cfg)
	first := inst.pickBackend("198.51.100.7", 0)
	for i := 0; i < 10; i++ {
		b := inst.pickBackend("198.51.100.7", 0)
		if b.ID != first.ID {
			t.Fatalf("source-IP stickiness broken: %d then %d", first.ID, b.ID)
		}
		inst.releaseBackend(b)
	}

	// A preferred backend (sticky cookie) wins over the algorithm
	cfg.StickySessionType = "cookie"
	inst = newTestInstance(cfg)
	for i := 0; i < 5; i++ {
		if b := inst.pickBackend("", 3); b.ID != 3 {
			t.Fatalf("preferred backend ignored, got %d", b.ID)
		}
	}
}

func TestUpdateBackendsFailsOverToBackup(t *testing.T) {
	hc := NewHealthChecker(nil)
	hc.states[1] = &backendHealth{known: true, healthy: false}
	inst := NewLoadBalancerInstance(models.LoadBalancer{
		Name: "failover",
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 1, Enabled: true},
			{ID: 2, Enabled: true, Backup: true},
			{ID: 3, Enabled: false},
		}},
	}, hc)
	inst.updateBackends()

	active := inst.backends.Load().([]*models.BackendServer)
	if len(active) != 1 || active[0].ID != 2 {
		t.Fatalf("expected only the backup node, got %v", active)
	}
}

func TestHealthThresholds(t *testing.T) {
	hc := NewHealthChecker(nil)
	target := HealthTarget{Backend: models.BackendServer{
		ID: 7, Enabled: true, HCEnabled: true, HCFailureThreshold: 3, HCRecoveryThreshold: 2,
	}}
	hc.SetTargets([]HealthTarget{target})
	hc.states[7] = &backendHealth{healthy: true}
	now := time.Now()

	steps := []struct {
		up   bool
		want bool
	}{
		{true, true},   // first success confirms the assumed state
		{false, true},  // 1 failure
		{false, true},  // 2 failures
		{false, false}, // 3 failures -> DOWN
		{true, false},  // 1 success
		{true, true},   // 2 successes -> UP
	}
	for i, s := range steps {
		hc.record(target, s.up, now)
		if got := hc.IsHealthy(7); got != s.want {
			t.Fatalf("step %d: healthy=%v, want %v", i, got, s.want)
		}
	}

	// A backend that fails from the start goes down after the same threshold,
	// so one slow first answer doesn't remove it
	hc.states[7] = &backendHealth{healthy: true}
	for i, want := range []bool{true, true, false} {
		hc.record(target, false, now)
		if got := hc.IsHealthy(7); got != want {
			t.Fatalf("initial failure %d: healthy=%v, want %v", i+1, got, want)
		}
	}
}
