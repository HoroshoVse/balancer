package engine

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

func tcpLB(t *testing.T, backendPort int, mutate func(*models.LoadBalancer)) string {
	t.Helper()
	cfg := models.LoadBalancer{
		ID: 950, Name: "timeouts", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: freePort(t, "tcp"),
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 951, Address: "127.0.0.1", Port: backendPort, Enabled: true},
		}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	startInstance(t, cfg)
	return backendAddr("127.0.0.1", cfg.ListenPort)
}

// echoServer echoes everything it reads.
func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestTCPIdleTimeoutClosesOnlyQuietConnections(t *testing.T) {
	addr := tcpLB(t, echoServer(t), func(lb *models.LoadBalancer) { lb.IdleTimeout = 300 })

	// Traffic every 100ms keeps the connection open well past the timeout
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	for i := 0; i < 10; i++ {
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte{'x'}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("active connection closed after %d round trips: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Once quiet, the balancer closes it
	start := time.Now()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("expected EOF from the balancer, got %v", err)
	}
	if waited := time.Since(start); waited > 1500*time.Millisecond {
		t.Fatalf("idle connection closed after %v, want about 300ms", waited)
	}
}

func TestTCPConnectTimeout(t *testing.T) {
	// 10.255.255.1 is normally unroutable: the dial hangs until the timeout
	cfgAddr := tcpLB(t, 81, func(lb *models.LoadBalancer) {
		lb.ConnectTimeout = 300
		lb.BackendGroup.Backends[0].Address = "10.255.255.1"
	})
	conn, err := net.Dial("tcp", cfgAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Read(make([]byte, 1))
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("client waited %v, want the 300ms connect timeout", waited)
	}
}

func TestHTTPResponseTimeoutReturns504(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	u, _ := url.Parse(slow.URL)
	port, _ := strconv.Atoi(u.Port())

	lbPort := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 960, Name: "slow", Protocol: "http", ListenIP: "127.0.0.1", ListenPort: lbPort, ResponseTimeout: 200,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: 961, Address: "127.0.0.1", Port: port, Enabled: true}}},
	})

	start := time.Now()
	resp, err := http.Get("http://" + backendAddr("127.0.0.1", lbPort) + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504", resp.StatusCode)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("took %v, want about 200ms", waited)
	}
}

func TestHTTPToHTTPSRedirect(t *testing.T) {
	old := httpRedirectPort
	httpRedirectPort = freePort(t, "tcp")
	t.Cleanup(func() { httpRedirectPort = old }) // runs after the instance stops

	lbPort := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 970, Name: "redirect", Protocol: "https", SSLEnabled: true, HTTPRedirect: true,
		ListenIP: "127.0.0.1", ListenPort: lbPort,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: 971, Address: "127.0.0.1", Port: 1, Enabled: true}}},
	})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://" + backendAddr("127.0.0.1", httpRedirectPort)
	want := "https://" + backendAddr("127.0.0.1", lbPort) + "/path?q=1"

	for method, code := range map[string]int{http.MethodGet: http.StatusMovedPermanently, http.MethodPost: http.StatusPermanentRedirect} {
		req, _ := http.NewRequest(method, base+"/path?q=1", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != code || resp.Header.Get("Location") != want {
			t.Fatalf("%s: %d %q, want %d %q", method, resp.StatusCode, resp.Header.Get("Location"), code, want)
		}
	}
}
