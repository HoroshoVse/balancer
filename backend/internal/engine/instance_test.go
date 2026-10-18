package engine

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/pires/go-proxyproto"
)

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		c, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		return c.LocalAddr().(*net.UDPAddr).Port
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitStatus(t *testing.T, inst *LoadBalancerInstance, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if status, _ := inst.Status(); status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, errStr := inst.Status()
	t.Fatalf("instance status %q (%s), want %q", status, errStr, want)
}

func startInstance(t *testing.T, cfg models.LoadBalancer) *LoadBalancerInstance {
	t.Helper()
	inst := NewLoadBalancerInstance(cfg, nil)
	go inst.Start()
	t.Cleanup(inst.Stop)
	waitStatus(t, inst, statusRunning)
	return inst
}

func TestBuildProxyHeaderCoercesDestinationFamily(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("203.0.113.5"), Port: 40000}
	dst := &net.TCPAddr{IP: net.IPv6unspecified, Port: 443} // e.g. listener bound to [::]
	h := buildProxyHeader(1, src, dst)
	out, err := h.Format()
	if err != nil {
		t.Fatal(err)
	}
	if want := "PROXY TCP4 203.0.113.5 0.0.0.0 40000 443\r\n"; string(out) != want {
		t.Fatalf("header %q, want %q", out, want)
	}

	udp := buildProxyHeader(2, &net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 5353}, nil)
	if udp.TransportProtocol != proxyproto.UDPv6 {
		t.Fatalf("transport %v, want UDPv6", udp.TransportProtocol)
	}
	if _, err := udp.Format(); err != nil {
		t.Fatalf("UDP v2 header: %v", err)
	}
}

// The PROXY header must reach the backend in plaintext, before the TLS handshake.
func TestTCPProxyProtocolBeforeTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := generateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	backendLn := tls.NewListener(&proxyproto.Listener{Listener: ln}, serverTLS)
	defer backendLn.Close()

	seen := make(chan string, 1)
	go func() {
		conn, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		seen <- conn.RemoteAddr().String()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		conn.Write([]byte("echo:" + line))
	}()

	port := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 101, Name: "tcp-pp-tls", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
		ProxyProtocolEnabled: true, ProxyProtocolVersion: 2,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 101, Name: "b", Address: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Enabled: true, TLSEnabled: true},
		}},
	})

	conn, err := net.Dial("tcp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Write([]byte("ping\n"))
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || reply != "echo:ping\n" {
		t.Fatalf("reply %q, err %v", reply, err)
	}
	if got := <-seen; got != conn.LocalAddr().String() {
		t.Fatalf("backend saw client %s, want %s", got, conn.LocalAddr())
	}
}

func TestHTTPForwardingHeadersAndStickyCookie(t *testing.T) {
	newBackend := func(name string) (*httptest.Server, int) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Backend", name)
			fmt.Fprintf(w, "%s|%s|%s", r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-IP"), r.Header.Get("X-Forwarded-Proto"))
		}))
		u, _ := url.Parse(srv.URL)
		port, _ := strconv.Atoi(u.Port())
		return srv, port
	}
	b1, p1 := newBackend("one")
	defer b1.Close()
	b2, p2 := newBackend("two")
	defer b2.Close()

	port := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 102, Name: "http-test", Protocol: "http", ListenIP: "127.0.0.1", ListenPort: port,
		StickySessionsEnabled: true, StickySessionType: "cookie",
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 201, Name: "one", Address: "127.0.0.1", Port: p1, Enabled: true},
			{ID: 202, Name: "two", Address: "127.0.0.1", Port: p2, Enabled: true},
		}},
	})
	lbURL := "http://" + backendAddr("127.0.0.1", port) + "/"

	req, _ := http.NewRequest(http.MethodGet, lbURL, nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := "10.0.0.1, 127.0.0.1|127.0.0.1|http"; string(body) != want {
		t.Fatalf("backend saw headers %q, want %q", body, want)
	}

	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == stickyCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no sticky cookie set")
	}
	backend := resp.Header.Get("X-Backend")

	for i := 0; i < 6; i++ {
		req, _ := http.NewRequest(http.MethodGet, lbURL, nil)
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Backend"); got != backend {
			t.Fatalf("request %d went to %s, want sticky %s", i, got, backend)
		}
		if len(resp.Cookies()) != 0 {
			t.Fatalf("cookie re-sent although unchanged")
		}
	}
}

func TestHTTPNoBackendsReturns503(t *testing.T) {
	port := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 103, Name: "http-empty", Protocol: "http", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 301, Address: "127.0.0.1", Port: 1, Enabled: false},
		}},
	})
	resp, err := http.Get("http://" + backendAddr("127.0.0.1", port) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
}

func TestUDPEchoThroughBalancer(t *testing.T) {
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
			backend.WriteTo(append([]byte("echo:"), buf[:n]...), addr)
		}
	}()

	port := freePort(t, "udp")
	startInstance(t, models.LoadBalancer{
		ID: 104, Name: "udp-test", Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 401, Address: "127.0.0.1", Port: backend.LocalAddr().(*net.UDPAddr).Port, Enabled: true},
		}},
	})

	conn, err := net.Dial("udp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	for _, msg := range []string{"a", "b", "c"} {
		conn.Write([]byte(msg))
		n, err := conn.Read(buf)
		if err != nil || string(buf[:n]) != "echo:"+msg {
			t.Fatalf("got %q, err %v", buf[:n], err)
		}
	}
}

func TestStopReleasesPortAndStartErrorsAreReported(t *testing.T) {
	port := freePort(t, "tcp")
	cfg := models.LoadBalancer{
		ID: 105, Name: "lifecycle", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: 501, Address: "127.0.0.1", Port: 1, Enabled: true}}},
	}
	inst := NewLoadBalancerInstance(cfg, nil)
	go inst.Start()
	waitStatus(t, inst, statusRunning)
	inst.Stop()

	// The port must be free as soon as Stop returns
	blocker, err := net.Listen("tcp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatalf("port still busy after Stop: %v", err)
	}
	defer blocker.Close()

	// Now the port is taken: the next instance must report the error
	failing := NewLoadBalancerInstance(cfg, nil)
	go failing.Start()
	waitStatus(t, failing, statusError)
	if _, errStr := failing.Status(); !strings.Contains(errStr, "address already in use") {
		t.Fatalf("unexpected start error %q", errStr)
	}
	failing.Stop()

	// Stop racing a just-launched Start (rapid double save) must not leak the listener
	cfg.ListenPort = freePort(t, "tcp")
	early := NewLoadBalancerInstance(cfg, nil)
	go early.Start()
	early.Stop()
	free, err := net.Listen("tcp", backendAddr("127.0.0.1", cfg.ListenPort))
	if err != nil {
		t.Fatalf("listener leaked after Stop: %v", err)
	}
	free.Close()
}
