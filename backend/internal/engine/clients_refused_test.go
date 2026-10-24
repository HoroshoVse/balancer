package engine

import (
	"net/netip"
	"testing"
)

// A few refused addresses must be listed even when many served ones are busier.
func TestTopRefusedIsNotCrowdedOutByServedClients(t *testing.T) {
	tr, _ := newTestTracker(1_000_000)
	for i := 1; i <= 15; i++ {
		rec(tr, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}).String(), 100+i, 0) // busy, never refused
	}
	rec(tr, "203.0.113.9", 0, 4)
	rec(tr, "203.0.113.10", 2, 9)
	rec(tr, "2001:db8::7", 0, 1)

	if all := tr.top(10); len(all) != 10 || all[0].refused != 0 {
		t.Fatalf("the busiest overall are the served ones, got %+v", all)
	}
	got := tr.topBy(10, true)
	want := []clientCount{
		{netip.MustParseAddr("203.0.113.10"), 2, 9},
		{netip.MustParseAddr("203.0.113.9"), 0, 4},
		{netip.MustParseAddr("2001:db8::7"), 0, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	var absent *clientTracker // a balancer that never saw anybody has no tracker yet
	if absent.topBy(5, true) != nil {
		t.Fatal("no tracker lists nobody")
	}
	if fresh, _ := newTestTracker(1_000_000); len(fresh.topBy(5, true)) != 0 {
		t.Fatal("an empty tracker lists nobody")
	}
}

func TestRefusedClientsAreInTheBalancerMetrics(t *testing.T) {
	m := &LBMetrics{clients: newClientTracker()}
	for i := 0; i < 30; i++ {
		m.clients.record(netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}), true)
	}
	m.clients.record(netip.MustParseAddr("192.0.2.1"), false)
	reg := &MetricsRegistry{}
	reg.lbs.Store(uint(777), m)
	out := reg.GetMetricsForLB(777)
	refused, ok := out["top_refused_clients"].([]ClientStat)
	if !ok || len(refused) != topRefusedShown {
		t.Fatalf("top_refused_clients = %v", out["top_refused_clients"])
	}
	for _, c := range refused {
		if c.Refused == 0 {
			t.Fatalf("a never-refused client in the refused list: %+v", c)
		}
	}
	if all, _ := out["top_clients"].([]ClientStat); len(all) != topClientsShown {
		t.Fatalf("top_clients has %d rows", len(all))
	}
}
