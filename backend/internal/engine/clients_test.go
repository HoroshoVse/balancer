package engine

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// newTestTracker is a tracker whose clock the test moves by hand.
func newTestTracker(unix int64) (*clientTracker, *atomic.Int64) {
	var now atomic.Int64
	now.Store(unix)
	t := newClientTracker()
	t.clock = now.Load
	return t, &now
}

func rec(t *clientTracker, ip string, accepted, refused int) {
	a := netip.MustParseAddr(ip)
	for i := 0; i < accepted; i++ {
		t.record(a, false)
	}
	for i := 0; i < refused; i++ {
		t.record(a, true)
	}
}

func randomPublicAddr(rng *rand.Rand) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(11 + rng.IntN(200)), byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))})
}

func TestClientTrackerCountsASmallSetExactly(t *testing.T) {
	tr, _ := newTestTracker(1_000_000)
	rec(tr, "192.0.2.1", 9, 0)
	rec(tr, "192.0.2.2", 2, 3)
	rec(tr, "198.51.100.7", 0, 7)
	rec(tr, "2001:db8::5", 4, 1)
	// The same address spelled as IPv4-mapped IPv6 is the same client
	for i := 0; i < 2; i++ {
		tr.record(netip.MustParseAddr("::ffff:192.0.2.1"), false)
	}

	got := tr.top(10)
	want := []clientCount{
		{netip.MustParseAddr("192.0.2.1"), 11, 0},
		{netip.MustParseAddr("198.51.100.7"), 0, 7},
		// 192.0.2.2 and 2001:db8::5 both have 5 hits: IPv4 sorts before IPv6
		{netip.MustParseAddr("192.0.2.2"), 2, 3},
		{netip.MustParseAddr("2001:db8::5"), 4, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	if top := tr.top(2); len(top) != 2 || top[0] != want[0] {
		t.Fatalf("top(2) = %+v", top)
	}
}

func TestClientTrackerKeepsHeavyHittersUnderSpoofedFlood(t *testing.T) {
	heavy := []struct {
		ip     string
		period int // one hit every `period` spoofed packets
	}{{"203.0.113.1", 10}, {"203.0.113.2", 16}, {"203.0.113.3", 25}, {"203.0.113.4", 40}, {"203.0.113.5", 50}}

	for _, c := range []struct {
		late  bool
		flood int // spoofed packets, each from a new random source
	}{{false, 1_000_000}, {true, 300_000}} {
		late, flood := c.late, c.flood
		t.Run(fmt.Sprintf("start after the flood=%v", late), func(t *testing.T) {
			tr, _ := newTestTracker(1_000_000)
			rng := rand.New(rand.NewPCG(1, 2))
			want := map[string]int{}
			start := 0
			if late {
				start = flood / 2
			}
			for i := 0; i < flood; i++ { // a million distinct random sources
				tr.record(randomPublicAddr(rng), false)
				if i < start {
					continue
				}
				for _, h := range heavy {
					if (i-start)%h.period == 0 {
						tr.record(netip.MustParseAddr(h.ip), false)
						want[h.ip]++
					}
				}
			}
			top := tr.top(len(heavy))
			if len(top) != len(heavy) {
				t.Fatalf("got %d rows", len(top))
			}
			for i, h := range heavy {
				row := top[i]
				if row.addr.String() != h.ip {
					t.Fatalf("row %d is %s, want %s (top: %+v)", i, row.addr, h.ip, top)
				}
				// The count is a lower bound; for an address that never leaves the table it is nearly exact
				if got := int(row.accepted); got > want[h.ip] || got < want[h.ip]*98/100 {
					t.Fatalf("%s counted %d, true %d", h.ip, got, want[h.ip])
				}
			}
			if n, max := tr.size(), clientShards*clientShardCap; n > max {
				t.Fatalf("%d addresses held after a flood of %d sources, limit %d", n, flood, max)
			}
		})
	}
}

func TestClientTrackerWindowExpires(t *testing.T) {
	const slice = clientSliceSecs
	start := int64(1000 * slice)
	tr, now := newTestTracker(start)
	a := netip.MustParseAddr("192.0.2.1")
	count := func() uint64 {
		for _, c := range tr.top(10) {
			if c.addr == a {
				return c.accepted
			}
		}
		return 0
	}

	rec(tr, a.String(), 2, 0) // minute 0
	now.Store(start + 5*slice)
	rec(tr, a.String(), 4, 0) // minute 5
	if got := count(); got != 6 {
		t.Fatalf("both minutes are in the window, counted %d", got)
	}
	now.Store(start + (clientSlices-1)*slice + slice - 1) // last second of the window of minute 0
	if got := count(); got != 6 {
		t.Fatalf("still inside the window, counted %d", got)
	}
	now.Store(start + clientSlices*slice) // minute 0 falls out, minute 5 stays
	if got := count(); got != 4 {
		t.Fatalf("after minute 0 expired, counted %d", got)
	}
	now.Store(start + (5+clientSlices)*slice)
	if got := count(); got != 0 {
		t.Fatalf("after everything expired, counted %d", got)
	}
	if n := tr.size(); n != 0 {
		t.Fatalf("%d entries left after the window passed", n)
	}

	// A long silence, then traffic again, then the clock stepping back
	rec(tr, a.String(), 3, 0)
	now.Store(start + 1000*slice)
	rec(tr, a.String(), 1, 0)
	if got := count(); got != 1 {
		t.Fatalf("after a long gap, counted %d", got)
	}
	now.Store(start)
	rec(tr, a.String(), 1, 0)
	if got := count(); got != 2 {
		t.Fatalf("after the clock stepped back, counted %d", got)
	}
}

func TestClientTrackerConcurrent(t *testing.T) {
	tr, _ := newTestTracker(1_000_000)
	addrs := make([]netip.Addr, 40)
	for i := range addrs {
		addrs[i] = netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)})
	}
	const writers, rounds = 8, 500

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < 2; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					tr.top(10)
					tr.size()
				}
			}
		}()
	}
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				for _, a := range addrs {
					tr.record(a, false)
					tr.record(a, true)
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()

	top := tr.top(len(addrs) + 5)
	if len(top) != len(addrs) {
		t.Fatalf("%d addresses, want %d", len(top), len(addrs))
	}
	for _, c := range top {
		if c.accepted != writers*rounds || c.refused != writers*rounds {
			t.Fatalf("%s: accepted %d refused %d, want %d each", c.addr, c.accepted, c.refused, writers*rounds)
		}
	}
}

func TestClientTrackerConcurrentWhileTheWindowMoves(t *testing.T) {
	tr, now := newTestTracker(1_000_000)
	rng := rand.New(rand.NewPCG(3, 4))
	addrs := make([]netip.Addr, 5000)
	for i := range addrs {
		addrs[i] = randomPublicAddr(rng)
	}
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 30_000; i++ {
				tr.record(addrs[(i*7+w)%len(addrs)], i%5 == 0)
				if w == 0 && i%2000 == 0 {
					now.Add(clientSliceSecs) // a minute passes
				}
				if w == 1 && i%5000 == 0 {
					tr.top(10)
				}
			}
		}(w)
	}
	wg.Wait()
	if n, max := tr.size(), clientShards*clientShardCap*clientSlices; n > max {
		t.Fatalf("%d addresses held, limit %d", n, max)
	}
}

func TestClientTrackerHoldsFewAddressesNoMatterTheSources(t *testing.T) {
	tr, now := newTestTracker(1_000_000)
	rng := rand.New(rand.NewPCG(5, 6))
	for minute := 0; minute < 3*clientSlices; minute++ {
		for i := 0; i < 10_000; i++ {
			tr.record(randomPublicAddr(rng), false)
		}
		now.Add(clientSliceSecs)
	}
	if n, max := tr.size(), clientShards*clientShardCap*clientSlices; n > max {
		t.Fatalf("%d addresses held, limit %d", n, max)
	}
}

func TestRecordDoesNotAllocate(t *testing.T) {
	tr, _ := newTestTracker(1_000_000)
	rng := rand.New(rand.NewPCG(7, 8))
	addrs := make([]netip.Addr, 4096)
	for i := range addrs {
		addrs[i] = randomPublicAddr(rng)
	}
	for _, a := range addrs { // warm up: slices take their memory on first use
		tr.record(a, false)
	}
	i := 0
	if n := testing.AllocsPerRun(20000, func() {
		tr.record(addrs[i%len(addrs)], i%3 == 0)
		i++
	}); n != 0 {
		t.Fatalf("%v allocations per hit", n)
	}
}

// ---- through the balancer ----

func clientRows(lbID uint) []ClientStat {
	rows, _ := Metrics.GetMetricsForLB(lbID)["top_clients"].([]ClientStat)
	return rows
}

func clientRow(t *testing.T, lbID uint, ip string) ClientStat {
	t.Helper()
	for _, r := range clientRows(lbID) {
		if r.IP == ip {
			return r
		}
	}
	t.Fatalf("balancer %d has no row for %s: %+v", lbID, ip, clientRows(lbID))
	return ClientStat{}
}

func TestTCPClientsAreCountedWithoutAnyRules(t *testing.T) {
	_, addr := dialTestLB(t, 1201, nil, models.BackendServer{ID: 1202, Port: echoServer(t)})
	for i := 0; i < 3; i++ {
		if err := echoOnce(addr); err != nil {
			t.Fatal(err)
		}
	}
	row := clientRow(t, 1201, "127.0.0.1")
	if row.Accepted != 3 || row.Refused != 0 || !row.Internal || row.Country != "" {
		t.Fatalf("%+v", row)
	}
	if _, ok := Metrics.GetMetricsForLB(1201)["blocked"]; ok {
		t.Fatal("blocked reported for a balancer that refused nobody")
	}
}

func TestRefusedClientsAreCountedSeparately(t *testing.T) {
	_, addr := dialTestLB(t, 1211, func(lb *models.LoadBalancer) { lb.AccessDeny = "127.0.0.1" },
		models.BackendServer{ID: 1212, Port: echoServer(t)})
	for i := 0; i < 4; i++ {
		if echoOnce(addr) == nil {
			t.Fatal("a denied address was served")
		}
	}
	row := clientRow(t, 1211, "127.0.0.1")
	if row.Accepted != 0 || row.Refused != 4 {
		t.Fatalf("%+v", row)
	}
}

func TestHTTPClientsAreCountedWithoutAnyRules(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(node.Close)
	u, _ := url.Parse(node.URL)
	nodePort, _ := strconv.Atoi(u.Port())
	cfg := models.LoadBalancer{
		ID: 1221, Name: "http-clients", Protocol: "http", ListenIP: "127.0.0.1", ListenPort: freePort(t, "tcp"),
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: 1222, Address: "127.0.0.1", Port: nodePort, Enabled: true}}},
	}
	cfg.Normalize()
	startInstance(t, cfg)
	resp, err := http.Get("http://" + backendAddr("127.0.0.1", cfg.ListenPort) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if row := clientRow(t, 1221, "127.0.0.1"); row.Accepted < 1 {
		t.Fatalf("%+v", row)
	}
}

func TestUDPClientsCountPacketsNotSessions(t *testing.T) {
	backend, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := backend.ReadFrom(buf)
			if err != nil {
				return
			}
			backend.WriteTo(buf[:n], addr)
		}
	}()
	port := freePort(t, "udp")
	cfg := models.LoadBalancer{
		ID: 1231, Name: "udp-clients", Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 1232, Address: "127.0.0.1", Port: backend.LocalAddr().(*net.UDPAddr).Port, Enabled: true},
		}},
	}
	cfg.Normalize()
	startInstance(t, cfg)

	c, err := net.Dial("udp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 100)
	for i := 0; i < 5; i++ { // one client port, so one session, five datagrams
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Read(buf); err != nil {
			t.Fatal(err)
		}
	}
	if row := clientRow(t, 1231, "127.0.0.1"); row.Accepted != 5 {
		t.Fatalf("%+v, want 5 datagrams", row)
	}
	if req := Metrics.GetMetricsForLB(1231)["total_requests"].(uint64); req != 1 {
		t.Fatalf("%d requests, the session metric should count one", req)
	}
}

func TestDNSClientsCountQueries(t *testing.T) {
	node := newFakeDNS(t, 'A', false)
	_, addr := dnsLB(t, node)
	before := clientRow0(900, "127.0.0.1")
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	for id := uint16(1); id <= 4; id++ {
		dnsExchange(t, conn, id)
	}
	if got := clientRow0(900, "127.0.0.1").Accepted - before.Accepted; got != 4 {
		t.Fatalf("%d queries counted, want 4", got)
	}
}

// clientRow0 is clientRow without failing when the address is not there yet.
func clientRow0(lbID uint, ip string) ClientStat {
	for _, r := range clientRows(lbID) {
		if r.IP == ip {
			return r
		}
	}
	return ClientStat{}
}

type countingGeo struct {
	calls atomic.Int64
	by    map[string]string
}

func (g *countingGeo) Country(a netip.Addr) (string, bool) {
	g.calls.Add(1)
	return g.by[a.Unmap().String()], true
}

func TestCountriesAreLookedUpOnlyForTheRowsReturned(t *testing.T) {
	g := &countingGeo{by: map[string]string{"5.255.255.77": "RU"}}
	useGeo(t, g)
	tr := Metrics.getOrCreate(1241).clients
	rng := rand.New(rand.NewPCG(9, 10))
	for i := 0; i < 20000; i++ {
		tr.record(randomPublicAddr(rng), false)
	}
	rec(tr, "5.255.255.77", 500, 20)
	rec(tr, "10.1.2.3", 300, 0) // internal: no lookup, no country
	if n := g.calls.Load(); n != 0 {
		t.Fatalf("%d country lookups while counting", n)
	}

	rows := clientRows(1241)
	if len(rows) != topClientsShown {
		t.Fatalf("%d rows, want %d", len(rows), topClientsShown)
	}
	if n := g.calls.Load(); n > topClientsShown {
		t.Fatalf("%d country lookups for %d rows", n, len(rows))
	}
	if rows[0].IP != "5.255.255.77" || rows[0].Country != "RU" || rows[0].Accepted != 500 || rows[0].Refused != 20 {
		t.Fatalf("row 0: %+v", rows[0])
	}
	if rows[1].IP != "10.1.2.3" || !rows[1].Internal || rows[1].Country != "" {
		t.Fatalf("row 1: %+v", rows[1])
	}
}

func TestNoTopClientsBeforeAnyTraffic(t *testing.T) {
	if _, ok := Metrics.GetMetricsForLB(1251)["top_clients"]; ok {
		t.Fatal("top_clients present for a balancer nobody has contacted")
	}
}

func TestMetricsForgetClientsOfRemovedBalancers(t *testing.T) {
	rec(Metrics.getOrCreate(1261).clients, "192.0.2.1", 1, 0)
	Metrics.Prune(map[uint]bool{}, map[uint]bool{})
	if len(clientRows(1261)) != 0 {
		t.Fatal("clients of a removed balancer survived")
	}
}

// ---- benchmarks ----

func BenchmarkClientTrackerFewClients(b *testing.B) {
	tr := newClientTracker()
	addrs := make([]netip.Addr, 20)
	for i := range addrs {
		addrs[i] = netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.record(addrs[i%len(addrs)], false)
	}
}

// A flood from random spoofed sources: every packet is a new address and evicts one.
func BenchmarkClientTrackerSpoofedFlood(b *testing.B) {
	tr := newClientTracker()
	x := uint32(2463534242)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		tr.record(netip.AddrFrom4([4]byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)}), false)
	}
}

func BenchmarkClientTrackerSpoofedFloodParallel(b *testing.B) {
	tr := newClientTracker()
	var seed atomic.Uint32
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		x := 2463534242 + seed.Add(7919)
		for pb.Next() {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			tr.record(netip.AddrFrom4([4]byte{byte(x), byte(x >> 8), byte(x >> 16), byte(x >> 24)}), false)
		}
	})
}

// admit is what every datagram goes through: address conversion, the rules, the count.
func BenchmarkAdmitNoRules(b *testing.B) {
	inst := NewLoadBalancerInstance(models.LoadBalancer{ID: 1271, Name: "bench"}, nil)
	addr := &net.UDPAddr{IP: net.IPv4(203, 0, 113, 9), Port: 5353}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inst.admit(addr)
	}
}

func BenchmarkClientTrackerTop(b *testing.B) {
	tr := newClientTracker()
	rng := rand.New(rand.NewPCG(1, 1))
	for i := 0; i < 200_000; i++ {
		tr.record(randomPublicAddr(rng), false)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr.top(topClientsShown)
	}
}
