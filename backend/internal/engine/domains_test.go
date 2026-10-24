package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

func dnsPacket(response bool, name string, qtype uint16) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint16(p[0:], 0x1234)
	p[2] = 0x01 // recursion desired
	if response {
		p[2] = 0x81
	}
	binary.BigEndian.PutUint16(p[4:], 1)
	for _, l := range strings.Split(strings.Trim(name, "."), ".") {
		if l == "" {
			continue
		}
		p = append(p, byte(len(l)))
		p = append(p, l...)
	}
	return append(p, 0, byte(qtype>>8), byte(qtype), 0, 1)
}

func parsed(p []byte) (string, bool) {
	var out [maxQueryLabel]byte
	n, ok := queryLabel(p, &out)
	if !ok {
		return "", false
	}
	domain, qtype := splitLabel(string(out[:n]))
	return domain + " " + qtype, true
}

func TestQueryLabelReadsTheFirstQuestion(t *testing.T) {
	for _, c := range []struct {
		name  string
		qtype uint16
		want  string
	}{
		{"example.com", 1, "example.com A"},
		{"Example.COM.", 28, "example.com AAAA"},
		{"xk3j9q.heimaoip.com", 16, "heimaoip.com TXT"},
		{"a.b.c.d.example.com", 255, "example.com ANY"},
		{"example.org", 15, "example.org MX"},
		{"example.org", 2, "example.org NS"},
		{"www.example.org", 5, "example.org CNAME"},
		{"example.org", 6, "example.org SOA"},
		{"1.0.0.127.in-addr.arpa", 12, "in-addr.arpa PTR"},
		{"_sip._tcp.example.com", 33, "example.com SRV"},
		{"example.com", 999, "example.com TYPE999"},
		{"example.com", 65535, "example.com TYPE65535"},
		{".", 2, ". NS"},
		{"localhost", 1, "localhost A"},
		{"_dmarc.example.com", 16, "example.com TXT"},
		{"a-b_c.example.com", 1, "example.com A"},
	} {
		got, ok := parsed(dnsPacket(false, c.name, c.qtype))
		if !ok || got != c.want {
			t.Errorf("%s type %d: %q (%v), want %q", c.name, c.qtype, got, ok, c.want)
		}
	}
}

func TestQueryLabelKeepsTheRegistrableTail(t *testing.T) {
	for name, want := range map[string]string{
		"example.com":              "example.com",
		"a.b.example.com":          "example.com",
		"example.co.uk":            "example.co.uk",
		"foo.co.uk":                "foo.co.uk",
		"a.b.foo.co.uk":            "foo.co.uk",
		"co.uk":                    "co.uk",
		"www.cam.ac.uk":            "cam.ac.uk",
		"x.y.shop.com.au":          "shop.com.au",
		"foo.or.jp":                "foo.or.jp",
		"foo.gov.cn":               "foo.gov.cn",
		"foo.edu.pl":               "foo.edu.pl",
		"foo.net.ru":               "foo.net.ru",
		"foo.org.br":               "foo.org.br",
		"deep.sub.example.co.com":  "co.com", // a three-letter TLD is not a country code
		"xyz.example.org":          "example.org",
		"sub.example.cn":           "example.cn",
		"sub.example.uk":           "example.uk",
		"sub.foo.biz.uk":           "biz.uk", // "biz" is not one of the generic second levels
		"com":                      "com",
		"UPPER.Mixed.CaSe.Com":     "case.com",
		"a.b.c.d.e.f.g.h.i.j.k.eu": "k.eu",
	} {
		got, ok := parsed(dnsPacket(false, name, 1))
		if want += " A"; !ok || got != want {
			t.Errorf("%s: %q (%v), want %q", name, got, ok, want)
		}
	}
}

func TestQueryLabelRejectsWhatIsNotAQuery(t *testing.T) {
	good := dnsPacket(false, "example.com", 1)
	if _, ok := parsed(good); !ok {
		t.Fatal("the control packet is rejected")
	}
	with := func(edit func(p []byte) []byte) []byte { return edit(append([]byte(nil), good...)) }

	long := func(labels int, size int) string {
		parts := make([]string, labels)
		for i := range parts {
			parts[i] = strings.Repeat("a", size)
		}
		return strings.Join(parts, ".")
	}
	// 3 labels of 63 and one of 61: 255 bytes on the wire, the longest legal name
	maxName := long(3, 63) + "." + strings.Repeat("b", 61)
	if got, ok := parsed(dnsPacket(false, maxName, 1)); !ok || !strings.HasSuffix(got, " A") {
		t.Errorf("the longest legal name was rejected: %q %v", got, ok)
	}

	rejected := map[string][]byte{
		"empty":                 nil,
		"shorter than a header": good[:11],
		"header only":           good[:12],
		"cut inside the name":   good[:20],
		"cut before the type":   good[:len(good)-4],
		"cut inside the class":  good[:len(good)-1],
		"a response":            dnsPacket(true, "example.com", 1),
		"no question":           with(func(p []byte) []byte { binary.BigEndian.PutUint16(p[4:], 0); return p }),
		"a compression pointer": with(func(p []byte) []byte { p[12] = 0xC0; return p }),
		"a label of 64":         dnsPacket(false, long(1, 64)+".com", 1),
		"a name of 256":         dnsPacket(false, long(4, 63)+".b", 1),
		"a name of 5 x 63":      dnsPacket(false, long(5, 63), 1),
		"a control byte":        with(func(p []byte) []byte { p[13] = 0x07; return p }),
		"a high byte":           with(func(p []byte) []byte { p[13] = 0xE9; return p }),
		"a space":               with(func(p []byte) []byte { p[13] = ' '; return p }),
		"a dot inside a label":  with(func(p []byte) []byte { p[13] = '.'; return p }),
		"text":                  []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"),
		"a QUIC-like packet":    append([]byte{0xC3, 0, 0, 0, 1, 8}, make([]byte, 1200)...),
		"zeros":                 make([]byte, 64),
	}
	for name, p := range rejected {
		if got, ok := parsed(p); ok {
			t.Errorf("%s: accepted as %q", name, got)
		}
	}

	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 20000; i++ { // random bytes may be accepted, but must never panic or overrun
		p := make([]byte, rng.IntN(80))
		for j := range p {
			p[j] = byte(rng.Uint32())
		}
		if len(p) > 5 && i%2 == 0 {
			p[2] &^= 0x80
			p[4], p[5] = 0, 1
		}
		parsed(p)
	}
}

func TestQueryLabelDoesNotAllocate(t *testing.T) {
	p := dnsPacket(false, "xk3j9q.sub.heimaoip.com", 16)
	var out [maxQueryLabel]byte
	if n := testing.AllocsPerRun(1000, func() { queryLabel(p, &out) }); n != 0 {
		t.Fatalf("%v allocations", n)
	}
	tr := newDomainTracker()
	n, _ := queryLabel(p, &out)
	tr.record(out[:n]) // the first sight of a name keeps a copy of it
	if a := testing.AllocsPerRun(1000, func() {
		n, _ := queryLabel(p, &out)
		tr.record(out[:n])
	}); a != 0 {
		t.Fatalf("%v allocations per refused query of a known name", a)
	}
}

func recordQuery(tr *domainTracker, name string, qtype uint16, times int) {
	var out [maxQueryLabel]byte
	n, ok := queryLabel(dnsPacket(false, name, qtype), &out)
	if !ok {
		panic("not a query: " + name)
	}
	for i := 0; i < times; i++ {
		tr.record(out[:n])
	}
}

func TestDomainTrackerCollapsesRandomSubdomains(t *testing.T) {
	_, now := newTestTracker(1_000_000)
	dt := newDomainTracker()
	dt.clock = now.Load

	rng := rand.New(rand.NewPCG(3, 4))
	var out [maxQueryLabel]byte
	for i := 0; i < 50_000; i++ { // a water-torture flood: a fresh subdomain every time
		name := fmt.Sprintf("%x.%x.heimaoip.com", rng.Uint64(), rng.Uint32())
		n, _ := queryLabel(dnsPacket(false, name, 16), &out)
		dt.record(out[:n])
	}
	recordQuery(dt, "www.example.co.uk", 1, 300)
	recordQuery(dt, "example.com", 28, 20)
	recordQuery(dt, "heimaoip.com", 1, 7) // same domain, other type: its own row

	got := dt.top(10)
	want := []BlockedDomain{
		{"heimaoip.com", "TXT", 50_000},
		{"example.co.uk", "A", 300},
		{"example.com", "AAAA", 20},
		{"heimaoip.com", "A", 7},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
	if n := dt.size(); n > 8 {
		t.Fatalf("%d keys held for 4 distinct domains", n)
	}

	// The window moves like the client table's
	now.Add(ClientWindow.Milliseconds() / 1000)
	if left := dt.top(10); len(left) != 0 {
		t.Fatalf("%+v left after the window passed", left)
	}
}

func TestDomainTrackerStaysBoundedAndKeepsTheBusiestName(t *testing.T) {
	dt := newDomainTracker()
	dt.clock = func() int64 { return 1_000_000 } // one slice, whatever the wall clock says
	rng := rand.New(rand.NewPCG(5, 6))
	var out [maxQueryLabel]byte
	n, _ := queryLabel(dnsPacket(false, "attack.example", 255), &out)
	heavy := append([]byte(nil), out[:n]...)
	for i := 0; i < 200_000; i++ { // every query for a different registrable domain
		if i%5 == 0 {
			dt.record(heavy)
		}
		name := fmt.Sprintf("%x.%x.net", rng.Uint32(), rng.Uint32())
		n, _ := queryLabel(dnsPacket(false, name, 1), &out)
		dt.record(out[:n])
	}
	got := dt.top(3)
	if len(got) == 0 || got[0].Domain != "attack.example" || got[0].Type != "ANY" || got[0].Count < 39_000 || got[0].Count > 40_000 {
		t.Fatalf("top: %+v", got)
	}
	if n, max := dt.size(), clientShards*clientShardCap; n > max {
		t.Fatalf("%d keys held, limit %d", n, max)
	}
}

func TestBlockedDomainsInTheMetricsJSON(t *testing.T) {
	recordQuery(Metrics.getOrCreate(1301).domains, "a.example.com", 16, 3)
	Metrics.RecordBlocked(1301, "US", ReasonCountryDenied)
	rows, _ := Metrics.GetMetricsForLB(1301)["blocked_domains"].([]BlockedDomain)
	if len(rows) != 1 {
		t.Fatalf("%+v", rows)
	}
	b, _ := json.Marshal(rows)
	if string(b) != `[{"domain":"example.com","type":"TXT","count":3}]` {
		t.Fatalf("%s", b)
	}
	if _, ok := Metrics.GetMetricsForLB(1302)["blocked_domains"]; ok {
		t.Fatal("blocked_domains present for a balancer that refused nobody")
	}
}

func TestBlockLogNamesTheDomains(t *testing.T) {
	b := newBlockLog()
	var out [maxQueryLabel]byte
	for i, name := range []string{"x.heimaoip.com", "y.heimaoip.com", "z.heimaoip.com", "www.example.org"} {
		n, _ := queryLabel(dnsPacket(false, name, 16), &out)
		b.note("US")
		b.noteQuery(out[:n])
		if i == 3 {
			b.note("PL")
		}
	}
	b.flush("shield-dns", time.Minute)
	logs := Logger.GetLogs()
	got := logs[len(logs)-1]
	want := "5 connections refused by the access rules in the last 1m0s; most from: US ×4, PL ×1; domains: heimaoip.com TXT ×3, example.org TXT ×1"
	if got.LBName != "shield-dns" || !strings.HasSuffix(got.Message, want) {
		t.Fatalf("summary %q, want ...%q", got.Message, want)
	}

	// Without DNS in the refused traffic the line keeps its old form
	b.note("HK")
	b.flush("shield-dns", time.Minute)
	logs = Logger.GetLogs()
	if m := logs[len(logs)-1].Message; strings.Contains(m, "domains") || !strings.HasSuffix(m, "most from: HK ×1") {
		t.Fatalf("summary %q", m)
	}
}

// ---- through the balancer ----

func blockedDomains(lbID uint) []BlockedDomain {
	rows, _ := Metrics.GetMetricsForLB(lbID)["blocked_domains"].([]BlockedDomain)
	return rows
}

func waitBlockedDomain(t *testing.T, lbID uint, want BlockedDomain) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := blockedDomains(lbID)
		if len(rows) == 1 && rows[0] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("balancer %d: blocked_domains = %+v, want [%+v]", lbID, rows, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sendDatagrams(t *testing.T, addr string, packets ...[]byte) {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, p := range packets {
		if _, err := c.Write(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRefusedUDPQueriesAreCountedByDomain(t *testing.T) {
	node := newFakeDNS(t, 'A', false)
	for _, c := range []struct {
		id      uint
		dnsMode bool
	}{{1311, false}, {1313, true}} {
		cfg := models.LoadBalancer{
			ID: c.id, Name: fmt.Sprintf("udp-names-%d", c.id), Protocol: "udp", DNSMode: c.dnsMode, ListenIP: "127.0.0.1",
			ListenPort: freePort(t, "udp"), Algorithm: "round_robin", AccessDeny: "127.0.0.1",
			BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: c.id + 1, Address: "127.0.0.1", Port: node.port, Enabled: true}}},
		}
		cfg.Normalize()
		startInstance(t, cfg)
		sendDatagrams(t, backendAddr("127.0.0.1", cfg.ListenPort),
			dnsPacket(true, "answer.example.com", 1), // not a query
			[]byte("not dns at all, just a long enough datagram"),
			dnsPacket(false, "a1.sub.heimaoip.com", 16),
			dnsPacket(false, "b2.heimaoip.com", 16),
			dnsPacket(false, "c3.heimaoip.com", 16),
		)
		waitBlockedDomain(t, c.id, BlockedDomain{"heimaoip.com", "TXT", 3})
		if blockedCount(c.id) != 5 {
			t.Fatalf("balancer %d refused %d datagrams, want 5", c.id, blockedCount(c.id))
		}
	}
}

func TestAcceptedQueriesAreNotCountedByDomain(t *testing.T) {
	node := newFakeDNS(t, 'A', false)
	cfg := models.LoadBalancer{
		ID: 1321, Name: "udp-names-open", Protocol: "udp", DNSMode: true, ListenIP: "127.0.0.1",
		ListenPort: freePort(t, "udp"), Algorithm: "round_robin",
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: 1322, Address: "127.0.0.1", Port: node.port, Enabled: true}}},
	}
	cfg.Normalize()
	startInstance(t, cfg)
	conn, err := net.Dial("udp", backendAddr("127.0.0.1", cfg.ListenPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	dnsExchange(t, conn, 7)
	if rows := blockedDomains(1321); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func TestRefusedTCPConnectionsHaveNoDomain(t *testing.T) {
	_, addr := dialTestLB(t, 1331, func(lb *models.LoadBalancer) { lb.AccessDeny = "127.0.0.1" },
		models.BackendServer{ID: 1332, Port: echoServer(t)})
	if echoOnce(addr) == nil {
		t.Fatal("a denied address was served")
	}
	if blockedCount(1331) < 1 {
		t.Fatal("the refusal was not counted")
	}
	if rows := blockedDomains(1331); len(rows) != 0 {
		t.Fatalf("%+v", rows)
	}
}

func BenchmarkQueryLabel(b *testing.B) {
	p := dnsPacket(false, "xk3j9q.sub.heimaoip.com", 16)
	var out [maxQueryLabel]byte
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		queryLabel(p, &out)
	}
}

func BenchmarkDomainTrackerRandomSubdomains(b *testing.B) {
	dt := newDomainTracker()
	p := dnsPacket(false, "xk3j9q.sub.heimaoip.com", 16)
	var out [maxQueryLabel]byte
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		n, _ := queryLabel(p, &out)
		dt.record(out[:n])
	}
}
