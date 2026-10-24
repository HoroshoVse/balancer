package engine

import (
	"net"
	"net/netip"
	"testing"
)

func recordFrom(dt *domainTracker, name string, qtype uint16, from string, refused bool, times int) {
	var out [maxQueryLabel]byte
	n, ok := queryLabel(dnsPacket(false, name, qtype), &out)
	if !ok {
		panic("not a query: " + name)
	}
	a := netip.MustParseAddr(from)
	for i := 0; i < times; i++ {
		dt.recordFrom(out[:n], a, refused)
	}
}

func findQuery(rep QueryReport, domain, qtype string) *QueryDomain {
	for i := range rep.Domains {
		if rep.Domains[i].Domain == domain && rep.Domains[i].Type == qtype {
			return &rep.Domains[i]
		}
	}
	return nil
}

// A name is listed with the addresses that asked for it and how each fared, so
// a refused attack and a client that slips through can be told apart.
func TestQueryReportListsWhoAskedAndWhatHappened(t *testing.T) {
	dt := newDomainTracker()
	dt.setClock(func() int64 { return 1_000_000 })
	recordFrom(dt, "x1.heimaoip.com", 16, "198.51.100.7", true, 30)
	recordFrom(dt, "x2.heimaoip.com", 16, "198.51.100.8", true, 20)
	recordFrom(dt, "x3.heimaoip.com", 16, "203.0.113.5", false, 4) // one gets through
	recordFrom(dt, "example.org", 1, "10.0.1.50", false, 9)

	rep := dt.report()
	if rep.WindowSeconds != int(ClientWindow.Seconds()) {
		t.Fatalf("window = %d s", rep.WindowSeconds)
	}
	d := findQuery(rep, "heimaoip.com", "TXT")
	if d == nil {
		t.Fatalf("heimaoip.com TXT missing from %+v", rep.Domains)
	}
	if d.Accepted != 4 || d.Refused != 50 {
		t.Fatalf("heimaoip.com = %d served / %d refused, want 4 / 50", d.Accepted, d.Refused)
	}
	if len(d.Clients) != 3 {
		t.Fatalf("clients = %+v, want 3", d.Clients)
	}
	if first := d.Clients[0]; first.IP != "198.51.100.7" || first.Refused != 30 || first.Accepted != 0 {
		t.Fatalf("busiest client = %+v", first)
	}
	var leaked *QueryClient
	for i := range d.Clients {
		if d.Clients[i].IP == "203.0.113.5" {
			leaked = &d.Clients[i]
		}
	}
	if leaked == nil || leaked.Accepted != 4 || leaked.Refused != 0 {
		t.Fatalf("the client let through = %+v", leaked)
	}
	if d.OtherAccepted != 0 || d.OtherRefused != 0 {
		t.Fatalf("nothing is left over, got %d/%d", d.OtherAccepted, d.OtherRefused)
	}
	if in := findQuery(rep, "example.org", "A"); in == nil || !in.Clients[0].Internal || in.Accepted != 9 {
		t.Fatalf("example.org = %+v", in)
	}
	if rep.Domains[0].Domain != "heimaoip.com" {
		t.Fatalf("the busiest name is not first: %+v", rep.Domains)
	}
}

// Under a flood from countless addresses the table of pairs cannot hold them
// all; the rest is reported as such instead of vanishing from the totals.
func TestQueryReportCountsAddressesItCannotListAsOthers(t *testing.T) {
	dt := newDomainTracker()
	dt.setClock(func() int64 { return 1_000_000 })
	const sources = 5000
	for i := 0; i < sources; i++ {
		recordFrom(dt, "flood.example", 255, netip.AddrFrom4([4]byte{45, byte(i >> 8), byte(i), 9}).String(), true, 1)
	}
	d := findQuery(dt.report(), "flood.example", "ANY")
	if d == nil {
		t.Fatal("flood.example missing")
	}
	if d.Refused != sources {
		t.Fatalf("refused = %d, want %d", d.Refused, sources)
	}
	if len(d.Clients) > queryClientsShown {
		t.Fatalf("%d clients listed, at most %d", len(d.Clients), queryClientsShown)
	}
	var listed uint64
	for _, c := range d.Clients {
		listed += c.Refused
	}
	if listed+d.OtherRefused != sources {
		t.Fatalf("listed %d + others %d != %d", listed, d.OtherRefused, sources)
	}
	if got, limit := dt.pairs.size(), clientShards*clientShardCap*clientSlices; got > limit {
		t.Fatalf("pair table holds %d keys, limit %d", got, limit)
	}
}

// Served names must not be pushed out of the report by a flood of refused ones.
func TestQueryReportKeepsServedNamesDuringAFlood(t *testing.T) {
	dt := newDomainTracker()
	dt.setClock(func() int64 { return 1_000_000 })
	recordFrom(dt, "legit.example", 1, "192.0.2.10", false, 50)
	for i := 0; i < 400; i++ {
		recordFrom(dt, "n"+string(rune('a'+i%26))+string(rune('a'+i/26))+".attack"+string(rune('a'+i%7))+".example", 16, "198.51.100.200", true, 100)
	}
	if findQuery(dt.report(), "legit.example", "A") == nil {
		t.Fatal("a served name was pushed out by refused ones")
	}
}

func TestQueryReportOfNobody(t *testing.T) {
	var nilTracker *domainTracker
	if rep := nilTracker.report(); rep.Domains == nil || len(rep.Domains) != 0 {
		t.Fatalf("nil tracker report = %+v", rep)
	}
	rep := newDomainTracker().report()
	if rep.Domains == nil || len(rep.Domains) != 0 {
		t.Fatalf("empty tracker report = %+v", rep)
	}
}

// A served query is counted, with its client, even when the balancer has no
// rules at all.
func TestAdmitPacketCountsServedQueriesToo(t *testing.T) {
	lbm := Metrics.getOrCreate(1401)
	inst := &LoadBalancerInstance{clients: lbm.clients, domains: lbm.domains}
	inst.Config.ID = 1401
	addr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 77), Port: 4000}
	for i := 0; i < 3; i++ {
		if !inst.admitPacket(addr, dnsPacket(false, "www.example.net", 1)) {
			t.Fatal("refused without rules")
		}
	}
	d := findQuery(Metrics.GetQueriesForLB(1401), "example.net", "A")
	if d == nil || d.Accepted != 3 || d.Refused != 0 || len(d.Clients) != 1 || d.Clients[0].IP != "192.0.2.77" {
		t.Fatalf("report = %+v", d)
	}
}

// The hot path of a flood: a name and address that are already in the tables
// cost no allocation.
func TestRecordFromDoesNotAllocateOnceKnown(t *testing.T) {
	dt := newDomainTracker()
	dt.setClock(func() int64 { return 1_000_000 })
	var out [maxQueryLabel]byte
	n, _ := queryLabel(dnsPacket(false, "x.heimaoip.com", 16), &out)
	a := netip.MustParseAddr("198.51.100.7")
	dt.recordFrom(out[:n], a, true) // the first sight keeps a copy of the name
	if got := testing.AllocsPerRun(1000, func() { dt.recordFrom(out[:n], a, true) }); got != 0 {
		t.Fatalf("%v allocations per query", got)
	}
}
