package engine

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// fakeGeo answers from a table; addresses not in it have no country.
type fakeGeo struct {
	loaded bool
	by     map[string]string
}

func (f fakeGeo) Country(a netip.Addr) (string, bool) { return f.by[a.Unmap().String()], f.loaded }

func useGeo(t *testing.T, g CountryLookup) {
	old := GeoIP
	GeoIP = g
	t.Cleanup(func() { GeoIP = old })
}

func policyFor(t *testing.T, mutate func(*models.LoadBalancer)) *accessPolicy {
	t.Helper()
	lb := models.LoadBalancer{Name: "p"}
	mutate(&lb)
	lb.Normalize()
	p := newAccessPolicy(lb)
	if p == nil {
		t.Fatal("no policy for a configuration with rules")
	}
	return p
}

var world = fakeGeo{loaded: true, by: map[string]string{
	"5.255.255.77": "RU", "93.125.0.1": "BY", "103.54.46.15": "HK", "43.224.30.68": "HK", "8.8.8.8": "US",
	"203.0.113.7": "HK", "198.51.100.9": "RU",
}}

func TestAccessOnlyListedCountries(t *testing.T) {
	useGeo(t, world)
	p := policyFor(t, func(lb *models.LoadBalancer) {
		lb.AccessMode, lb.AccessCountries = models.AccessOnly, "ru, by"
		lb.AccessAllow = "203.0.113.7"
		lb.AccessDeny = "198.51.100.0/24"
	})
	cases := []struct {
		ip      string
		allowed bool
		reason  string
		country string
	}{
		{"5.255.255.77", true, ReasonCountryOK, "RU"},
		{"93.125.0.1", true, ReasonCountryOK, "BY"},
		{"103.54.46.15", false, ReasonCountryDenied, "HK"}, // the attacker from the screenshots
		{"8.8.8.8", false, ReasonCountryDenied, "US"},
		{"91.204.224.105", false, ReasonUnknown, ""},              // listed by no registry: refused when only some countries are welcome
		{"203.0.113.7", true, ReasonAllowList, ""},                // the allow list beats the country
		{"198.51.100.9", false, ReasonDenyList, ""},               // the deny list beats the country
		{"::ffff:103.54.46.15", false, ReasonCountryDenied, "HK"}, // IPv4 written as IPv6
		{"10.0.1.8", true, ReasonInternal, ""},                    // the servers' own network
		{"127.0.0.1", true, ReasonInternal, ""},
		{"192.168.50.2", true, ReasonInternal, ""},
		{"100.64.3.3", true, ReasonInternal, ""}, // carrier-grade NAT, VPNs
		{"fe80::1", true, ReasonInternal, ""},
		{"::1", true, ReasonInternal, ""},
	}
	for _, c := range cases {
		d := p.decide(netip.MustParseAddr(c.ip))
		if d.Allowed != c.allowed || d.Reason != c.reason || d.Country != c.country {
			t.Errorf("%s: %+v, want allowed=%v reason=%s country=%q", c.ip, d, c.allowed, c.reason, c.country)
		}
	}
	if d := p.decide(netip.MustParseAddr("198.51.100.9")); d.Rule != "198.51.100.0/24" {
		t.Errorf("rule %q", d.Rule)
	}
}

func TestAccessAllExceptListedCountries(t *testing.T) {
	useGeo(t, world)
	p := policyFor(t, func(lb *models.LoadBalancer) {
		lb.AccessMode, lb.AccessCountries = models.AccessExcept, "HK,US"
	})
	for ip, want := range map[string]bool{
		"103.54.46.15": false, "43.224.30.68": false, "8.8.8.8": false,
		"5.255.255.77": true, "91.204.224.105": true, // unknown country is let in
	} {
		if d := p.decide(netip.MustParseAddr(ip)); d.Allowed != want {
			t.Errorf("%s allowed=%v, want %v (%+v)", ip, d.Allowed, want, d)
		}
	}
}

func TestAccessListsOnlyNeedNoDatabase(t *testing.T) {
	useGeo(t, nil)
	p := policyFor(t, func(lb *models.LoadBalancer) { lb.AccessDeny = "198.51.100.0/24\n2001:db8::/32" })
	if d := p.decide(netip.MustParseAddr("198.51.100.200")); d.Allowed || d.Reason != ReasonDenyList {
		t.Fatalf("%+v", d)
	}
	if d := p.decide(netip.MustParseAddr("2001:db8::5")); d.Allowed {
		t.Fatalf("%+v", d)
	}
	if d := p.decide(netip.MustParseAddr("8.8.8.8")); !d.Allowed || d.Reason != ReasonNoRules {
		t.Fatalf("%+v", d)
	}
}

// A failed or missing download must not lock every user out
func TestAccessFailsOpenWithoutDatabase(t *testing.T) {
	for name, g := range map[string]CountryLookup{"nil": nil, "not loaded": fakeGeo{loaded: false}} {
		t.Run(name, func(t *testing.T) {
			useGeo(t, g)
			p := policyFor(t, func(lb *models.LoadBalancer) { lb.AccessMode, lb.AccessCountries = models.AccessOnly, "RU" })
			if d := p.decide(netip.MustParseAddr("103.54.46.15")); !d.Allowed || d.Reason != ReasonNoDatabase {
				t.Fatalf("%+v", d)
			}
			// ...but the explicit lists still apply
			p2 := policyFor(t, func(lb *models.LoadBalancer) {
				lb.AccessMode, lb.AccessCountries, lb.AccessDeny = models.AccessOnly, "RU", "103.54.46.15"
			})
			if d := p2.decide(netip.MustParseAddr("103.54.46.15")); d.Allowed {
				t.Fatalf("%+v", d)
			}
		})
	}
}

func TestNoPolicyWithoutRules(t *testing.T) {
	if p := newAccessPolicy(models.LoadBalancer{Name: "x", AccessCountries: "RU"}); p != nil {
		t.Fatal("countries without a mode must not create a policy")
	}
}

func TestPrefixSetMostSpecific(t *testing.T) {
	prefixes, err := models.ParsePrefixes("10.0.0.0/8, 10.1.0.0/16, 192.0.2.9, 2001:db8::/32, 2001:db8:1::/48")
	if err != nil {
		t.Fatal(err)
	}
	s := newPrefixSet(prefixes)
	for ip, want := range map[string]string{
		"10.9.9.9": "10.0.0.0/8", "10.1.2.3": "10.1.0.0/16", "192.0.2.9": "192.0.2.9", "192.0.2.10": "",
		"2001:db8:1::5": "2001:db8:1::/48", "2001:db8:2::5": "2001:db8::/32", "2001:db9::": "", "11.0.0.1": "",
	} {
		got, ok := s.match(netip.MustParseAddr(ip))
		if got != want || ok != (want != "") {
			t.Errorf("%s -> %q (%v), want %q", ip, got, ok, want)
		}
	}
}

func TestPrefixSetStaysFastWithLargeLists(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "%d.%d.%d.0/24\n", 20+i%200, i/200, i%250)
	}
	prefixes, err := models.ParsePrefixes(sb.String())
	if err != nil {
		t.Fatal(err)
	}
	s := newPrefixSet(prefixes)
	a := netip.MustParseAddr("8.8.8.8")
	start := time.Now()
	for i := 0; i < 200000; i++ {
		s.match(a)
	}
	if per := time.Since(start) / 200000; per > 20*time.Microsecond {
		t.Fatalf("%v per lookup with 5000 subnets", per)
	}
}

// ---- enforcement over real sockets ----

func blockedCount(lbID uint) uint64 {
	v, _ := Metrics.GetMetricsForLB(lbID)["blocked"].(uint64)
	return v
}

func TestTCPRefusesDeniedAddresses(t *testing.T) {
	port := echoServer(t)

	_, denied := dialTestLB(t, 1101, func(lb *models.LoadBalancer) { lb.AccessDeny = "127.0.0.1" },
		models.BackendServer{ID: 1102, Port: port})
	if err := echoOnce(denied); err == nil {
		t.Fatal("a denied address was served")
	}
	if blockedCount(1101) < 1 {
		t.Fatal("the refusal was not counted")
	}
	top, _ := Metrics.GetMetricsForLB(1101)["blocked_top"].([]blockedCountry)
	if len(top) != 1 || top[0].Count < 1 {
		t.Fatalf("blocked_top = %+v", top)
	}

	// The allow list is checked first
	_, allowed := dialTestLB(t, 1103, func(lb *models.LoadBalancer) {
		lb.AccessAllow, lb.AccessDeny = "127.0.0.0/8", "127.0.0.1"
	}, models.BackendServer{ID: 1104, Port: port})
	if err := echoOnce(allowed); err != nil {
		t.Fatalf("an allowed address was refused: %v", err)
	}
	if blockedCount(1103) != 0 {
		t.Fatal("counted a refusal that did not happen")
	}
}

func TestUDPDropsDeniedAddressesSilently(t *testing.T) {
	backend, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	reached := make(chan struct{}, 10)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := backend.ReadFrom(buf)
			if err != nil {
				return
			}
			reached <- struct{}{}
			backend.WriteTo(buf[:n], addr)
		}
	}()
	query := func(port int) error {
		c, err := net.Dial("udp", backendAddr("127.0.0.1", port))
		if err != nil {
			return err
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(400 * time.Millisecond))
		c.Write([]byte("ping"))
		_, err = c.Read(make([]byte, 100))
		return err
	}
	for _, c := range []struct {
		id       uint
		deny     string
		wantResp bool
	}{{1111, "127.0.0.1", false}, {1113, "", true}} {
		port := freePort(t, "udp")
		cfg := models.LoadBalancer{
			ID: c.id, Name: fmt.Sprintf("udp-acl-%d", c.id), Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: port, AccessDeny: c.deny,
			BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
				{ID: c.id + 1, Address: "127.0.0.1", Port: backend.LocalAddr().(*net.UDPAddr).Port, Enabled: true},
			}},
		}
		cfg.Normalize()
		startInstance(t, cfg)
		err := query(port)
		if (err == nil) != c.wantResp {
			t.Fatalf("balancer %d: answer err=%v, want answered=%v", c.id, err, c.wantResp)
		}
	}
	// Only the open balancer's query got through to the node
	if len(reached) != 1 {
		t.Fatalf("%d packets reached the node, want 1", len(reached))
	}
	if blockedCount(1111) < 1 {
		t.Fatal("refused datagram not counted")
	}
}

func TestHTTPRefusesDeniedAddressesBeforeParsing(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	t.Cleanup(node.Close)
	u, _ := url.Parse(node.URL)
	nodePort, _ := strconv.Atoi(u.Port())

	get := func(lbPort int) (string, error) {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + backendAddr("127.0.0.1", lbPort) + "/")
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}
	for _, c := range []struct {
		id   uint
		deny string
		ok   bool
	}{{1121, "127.0.0.1", false}, {1123, "", true}} {
		cfg := models.LoadBalancer{
			ID: c.id, Name: fmt.Sprintf("http-acl-%d", c.id), Protocol: "http", ListenIP: "127.0.0.1", ListenPort: freePort(t, "tcp"), AccessDeny: c.deny,
			BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: c.id + 1, Address: "127.0.0.1", Port: nodePort, Enabled: true}}},
		}
		cfg.Normalize()
		startInstance(t, cfg)
		body, err := get(cfg.ListenPort)
		if (err == nil) != c.ok || (c.ok && body != "ok") {
			t.Fatalf("balancer %d: body=%q err=%v, want served=%v", c.id, body, err, c.ok)
		}
	}
	if blockedCount(1121) < 1 {
		t.Fatal("refused connection not counted")
	}
}

func TestEngineCheckAccess(t *testing.T) {
	useGeo(t, world)
	inst, _ := dialTestLB(t, 1131, func(lb *models.LoadBalancer) {
		lb.AccessMode, lb.AccessCountries = models.AccessOnly, "RU"
	}, models.BackendServer{ID: 1132, Port: echoServer(t)})
	open, _ := dialTestLB(t, 1133, nil, models.BackendServer{ID: 1134, Port: echoServer(t)})
	e := &Engine{activeBalancers: map[uint]*LoadBalancerInstance{1131: inst, 1133: open}}

	if d, ok := e.CheckAccess(1131, netip.MustParseAddr("103.54.46.15")); !ok || d.Allowed || d.Country != "HK" {
		t.Fatalf("%+v %v", d, ok)
	}
	if d, ok := e.CheckAccess(1131, netip.MustParseAddr("5.255.255.77")); !ok || !d.Allowed {
		t.Fatalf("%+v %v", d, ok)
	}
	if d, ok := e.CheckAccess(1133, netip.MustParseAddr("103.54.46.15")); !ok || !d.Allowed || d.Reason != ReasonNoRules {
		t.Fatalf("%+v %v", d, ok)
	}
	if _, ok := e.CheckAccess(999, netip.MustParseAddr("8.8.8.8")); ok {
		t.Fatal("answered for a balancer that does not exist")
	}
}

func TestBlockLogSummarisesOncePerInterval(t *testing.T) {
	b := newBlockLog()
	for i := 0; i < 7; i++ {
		b.note("HK")
	}
	for i := 0; i < 3; i++ {
		b.note("US")
	}
	b.note("deny list")
	last := func() LogEntry {
		logs := Logger.GetLogs()
		return logs[len(logs)-1]
	}
	b.flush("shield", time.Minute)
	got := last()
	if got.Level != LevelWarn || got.LBName != "shield" || !strings.Contains(got.Message, "11 connections refused") || !strings.Contains(got.Message, "most from: HK ×7, US ×3, deny list ×1") {
		t.Fatalf("summary %+v", got)
	}
	b.flush("shield", time.Minute)
	if again := last(); again != got {
		t.Fatalf("an empty interval logged %+v", again)
	}
}
