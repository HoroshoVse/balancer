package engine

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// fakeDNS answers on UDP and TCP of the same port by echoing the query with
// the QR bit set and RCODE = marker, so tests can tell nodes apart.
type fakeDNS struct {
	port   int
	silent bool
	udp    net.PacketConn
	tcp    net.Listener
}

func newFakeDNS(t *testing.T, marker byte, silent bool) *fakeDNS {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		udp, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := udp.LocalAddr().(*net.UDPAddr).Port
		tcp, err := net.Listen("tcp", backendAddr("127.0.0.1", port))
		if err != nil {
			udp.Close()
			continue
		}
		f := &fakeDNS{port: port, silent: silent, udp: udp, tcp: tcp}
		t.Cleanup(func() { udp.Close(); tcp.Close() })
		answer := func(q []byte) []byte {
			r := append([]byte(nil), q...)
			r[2] |= 0x80
			r[3] = r[3]&0xF0 | marker
			return r
		}
		go func() {
			buf := make([]byte, 4096)
			for {
				n, addr, err := udp.ReadFrom(buf)
				if err != nil {
					return
				}
				if !f.silent && n >= dnsHeaderLen {
					udp.WriteTo(answer(buf[:n]), addr)
				}
			}
		}()
		go func() {
			for {
				c, err := tcp.Accept()
				if err != nil {
					return
				}
				go func() {
					defer c.Close()
					var l [2]byte
					if _, err := io.ReadFull(c, l[:]); err != nil {
						return
					}
					q := make([]byte, binary.BigEndian.Uint16(l[:]))
					io.ReadFull(c, q)
					r := answer(q)
					c.Write(append([]byte{byte(len(r) >> 8), byte(len(r))}, r...))
				}()
			}
		}()
		return f
	}
	t.Fatal("could not bind matching UDP and TCP ports")
	return nil
}

func dnsLB(t *testing.T, backends ...*fakeDNS) (*LoadBalancerInstance, string) {
	t.Helper()
	cfg := models.LoadBalancer{
		ID: 900, Name: "dns-test", Protocol: "udp", DNSMode: true, ListenIP: "127.0.0.1",
		ListenPort: freePort(t, "udp"), Algorithm: "round_robin",
	}
	for i, b := range backends {
		cfg.BackendGroup.Backends = append(cfg.BackendGroup.Backends, models.BackendServer{
			ID: uint(901 + i), Address: "127.0.0.1", Port: b.port, Enabled: true,
		})
	}
	inst := startInstance(t, cfg)
	return inst, backendAddr("127.0.0.1", cfg.ListenPort)
}

func dnsExchange(t *testing.T, conn net.Conn, id uint16) (uint16, byte) {
	t.Helper()
	if _, err := conn.Write(buildDNSQuery(id, "example.test")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if n < dnsHeaderLen || buf[2]&0x80 == 0 {
		t.Fatalf("not a DNS response: %x", buf[:n])
	}
	return binary.BigEndian.Uint16(buf), buf[3] & 0x0F
}

func TestDNSModeRewritesIDsAndSharesSockets(t *testing.T) {
	b1, b2 := newFakeDNS(t, 1, false), newFakeDNS(t, 2, false)
	inst, addr := dnsLB(t, b1, b2)

	// 300 clients, each on its own port, all using the same DNS ID
	var mu sync.Mutex
	markers := map[byte]int{}
	var wg sync.WaitGroup
	for w := 0; w < 30; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				conn, err := net.Dial("udp", addr)
				if err != nil {
					t.Error(err)
					return
				}
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				id, marker := dnsExchange(t, conn, 0x1234)
				conn.Close()
				if id != 0x1234 {
					t.Errorf("client got ID %#x, want its own 0x1234", id)
				}
				mu.Lock()
				markers[marker]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if markers[1] != 150 || markers[2] != 150 {
		t.Errorf("answers by node: %v, want 150/150", markers)
	}
	if n := inst.dns.Load().socketCount(); n > 2*dnsSocketsPerNode {
		t.Errorf("%d backend sockets for 300 clients, want at most %d", n, 2*dnsSocketsPerNode)
	}
	for _, id := range []uint{901, 902} {
		if active := inst.conns.Active(id); active != 0 {
			t.Errorf("backend %d still has %d queries in flight", id, active)
		}
	}
}

func TestDNSModeConcurrentQueriesOnOneSocketAndTCP(t *testing.T) {
	inst, addr := dnsLB(t, newFakeDNS(t, 1, false))
	_ = inst

	// glibc sends A and AAAA back to back from the same socket
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write(buildDNSQuery(1111, "example.test"))
	conn.Write(buildDNSQuery(2222, "example.test"))
	got := map[uint16]bool{}
	buf := make([]byte, 4096)
	for len(got) < 2 {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("answers so far %v: %v", got, err)
		}
		got[binary.BigEndian.Uint16(buf[:n])] = true
	}
	if !got[1111] || !got[2222] {
		t.Fatalf("answers %v, want IDs 1111 and 2222", got)
	}

	// DNS over TCP on the same port (fallback for truncated answers)
	tc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("no TCP listener in DNS mode: %v", err)
	}
	defer tc.Close()
	tc.SetDeadline(time.Now().Add(3 * time.Second))
	q := buildDNSQuery(4242, "example.test")
	tc.Write(append([]byte{0, byte(len(q))}, q...))
	var l [2]byte
	if _, err := io.ReadFull(tc, l[:]); err != nil {
		t.Fatal(err)
	}
	resp := make([]byte, binary.BigEndian.Uint16(l[:]))
	io.ReadFull(tc, resp)
	if binary.BigEndian.Uint16(resp) != 4242 {
		t.Fatalf("TCP answer ID %d", binary.BigEndian.Uint16(resp))
	}
}

func TestDNSModeExpiresUnansweredQueries(t *testing.T) {
	old := dnsQueryTimeout
	dnsQueryTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dnsQueryTimeout = old }) // runs after the instance is stopped

	inst, addr := dnsLB(t, newFakeDNS(t, 1, true))
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write(buildDNSQuery(7, "example.test"))

	waitFor := func(cond func() bool, what string) {
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (%d in flight)", what, inst.conns.Active(901))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor(func() bool { return inst.conns.Active(901) == 1 }, "the query to be in flight")
	waitFor(func() bool { return inst.conns.Active(901) == 0 }, "the slot to be released")
}

func TestProbeDNS(t *testing.T) {
	up := newFakeDNS(t, 0, false)
	if !probeDNS(backendAddr("127.0.0.1", up.port), "", time.Second) {
		t.Fatal("answering server reported down")
	}
	silent := newFakeDNS(t, 0, true)
	if probeDNS(backendAddr("127.0.0.1", silent.port), "example.com", 300*time.Millisecond) {
		t.Fatal("silent server reported up")
	}

	// "." queries NS of the root; a name queries A
	root := buildDNSQuery(1, "/")
	if len(root) != dnsHeaderLen+5 || binary.BigEndian.Uint16(root[dnsHeaderLen+1:]) != 2 {
		t.Fatalf("root query %x", root)
	}
	if q := buildDNSQuery(1, "example.com"); string(q[dnsHeaderLen:dnsHeaderLen+13]) != "\x07example\x03com\x00" {
		t.Fatalf("name query %x", q)
	}
}
