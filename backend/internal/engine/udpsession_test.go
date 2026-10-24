package engine

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// udpEchoNode answers every datagram with "echo:" and the same bytes.
func udpEchoNode(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, a, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			c.WriteTo(append([]byte("echo:"), buf[:n]...), a)
		}
	}()
	return c.LocalAddr().(*net.UDPAddr).Port
}

// udpTestLB starts a plain UDP balancer (a session per client) in front of one node.
func udpTestLB(t *testing.T, id uint, mutate func(*models.LoadBalancer)) (*LoadBalancerInstance, string) {
	t.Helper()
	cfg := models.LoadBalancer{
		ID: id, Name: fmt.Sprintf("udp-%d", id), Protocol: "udp", ListenIP: "127.0.0.1", ListenPort: freePort(t, "udp"),
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: id + 1, Address: "127.0.0.1", Port: udpEchoNode(t), Enabled: true},
		}},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	inst := startInstance(t, cfg)
	return inst, backendAddr("127.0.0.1", cfg.ListenPort)
}

// ask sends one datagram from a new client socket and returns the client with the answer, if any.
func ask(addr, msg string, wait time.Duration) (net.Conn, string, error) {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return nil, "", err
	}
	if _, err := c.Write([]byte(msg)); err != nil {
		return c, "", err
	}
	c.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 1500)
	n, err := c.Read(buf)
	return c, string(buf[:n]), err
}

// Each client must get the answer to its own question, also with the replies
// now relayed through shared buffers.
func TestUDPRepliesReachTheRightClient(t *testing.T) {
	_, addr := udpTestLB(t, 1201, nil)
	const clients = 150
	var wg sync.WaitGroup
	errs := make(chan string, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := fmt.Sprintf("client-%d", i)
			c, got, err := ask(addr, msg, 3*time.Second)
			if c != nil {
				defer c.Close()
			}
			if err != nil || got != "echo:"+msg {
				errs <- fmt.Sprintf("client %d got %q (%v)", i, got, err)
				return
			}
			// a second question on the same session works too
			c.Write([]byte(msg + "!"))
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 1500)
			if n, err := c.Read(buf); err != nil || string(buf[:n]) != "echo:"+msg+"!" {
				errs <- fmt.Sprintf("client %d second answer %q (%v)", i, buf[:n], err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// Beyond the limit new clients are not served, those already served are, and
// room comes back when sessions end.
func TestUDPSessionLimit(t *testing.T) {
	old := maxUDPSessions
	maxUDPSessions = 20
	t.Cleanup(func() { maxUDPSessions = old })
	inst, addr := udpTestLB(t, 1211, func(lb *models.LoadBalancer) { lb.IdleTimeout = 400 })

	const clients = 60
	var mu sync.Mutex
	var served []net.Conn
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, got, err := ask(addr, fmt.Sprintf("c%d", i), 700*time.Millisecond)
			if c != nil {
				t.Cleanup(func() { c.Close() })
			}
			if err == nil && got != "" {
				mu.Lock()
				served = append(served, c)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(served) != 20 {
		t.Fatalf("%d clients served with a limit of 20", len(served))
	}
	if n := inst.udpCount.Load(); n != 20 {
		t.Fatalf("%d live sessions, want 20", n)
	}
	if n := inst.udpDropped.Load(); n != clients-20 {
		t.Fatalf("%d datagrams counted as dropped, want %d", n, clients-20)
	}

	// A client that already has a session keeps being served at the limit
	served[0].Write([]byte("again"))
	served[0].SetReadDeadline(time.Now().Add(time.Second))
	if n, err := served[0].Read(make([]byte, 100)); err != nil || n == 0 {
		t.Fatalf("existing session stopped working at the limit: %v", err)
	}

	// Idle sessions end and the room is free again
	deadline := time.Now().Add(6 * time.Second)
	for inst.udpCount.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := inst.udpCount.Load(); n != 0 {
		t.Fatalf("%d sessions still counted after the idle timeout", n)
	}
	c, got, err := ask(addr, "late", time.Second)
	if c != nil {
		defer c.Close()
	}
	if err != nil || got != "echo:late" {
		t.Fatalf("a new client was not served once room was free: %q (%v)", got, err)
	}
}

// A session that waits for its next reply must not hold a copy buffer: that
// was 64 KiB per session, thousands of them in an attack.
func TestUDPIdleSessionsHoldNoBuffers(t *testing.T) {
	const n = 1500
	inst, addr := udpTestLB(t, 1221, nil)
	measure := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse + ms.StackInuse
	}

	before := measure()
	for i := 0; i < n; i++ {
		c, err := net.Dial("udp", addr)
		if err != nil {
			t.Skipf("cannot open that many sockets here: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		c.Write([]byte("q"))
	}
	deadline := time.Now().Add(10 * time.Second)
	for inst.udpCount.Load() < n && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := inst.udpCount.Load(); got < n {
		t.Fatalf("only %d of %d sessions were created", got, n)
	}
	time.Sleep(300 * time.Millisecond) // let each relay goroutine reach its wait
	perSession := (measure() - before) / n
	t.Logf("%d idle sessions: %d bytes each (heap + stacks)", n, perSession)
	if perSession > 28<<10 {
		t.Fatalf("%d bytes per idle UDP session, want under 28 KiB: reply buffers are held while waiting", perSession)
	}
}
