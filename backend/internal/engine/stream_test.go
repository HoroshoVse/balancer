package engine

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// tcpPair returns two connected loopback TCP connections.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	c1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c2 := <-accepted
	if c2 == nil {
		t.Fatal("accept failed")
	}
	return c1, c2
}

func pipePair(t *testing.T) (net.Conn, net.Conn) {
	a, b := net.Pipe()
	return a, b
}

// rig is client <-> proxyStreams <-> server.
type rig struct {
	client, server net.Conn
	done           chan struct{} // closed when proxyStreams returns
}

func startRig(t *testing.T, idle time.Duration, pair func(*testing.T) (net.Conn, net.Conn)) *rig {
	t.Helper()
	client, a := pair(t)
	b, server := pair(t)
	r := &rig{client: client, server: server, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		proxyStreams(a, b, idle)
	}()
	t.Cleanup(func() {
		client.Close()
		server.Close()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Error("proxyStreams did not return after both ends closed")
		}
	})
	return r
}

func waitDone(t *testing.T, r *rig, within time.Duration) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(within):
		t.Fatalf("proxyStreams still running after %v", within)
	}
}

// Both copy paths (descriptor-based wait for TCP, plain blocking reads for
// anything else) and the splice path must move data intact in both directions.
func TestProxyStreamsMovesDataBothWays(t *testing.T) {
	pairs := map[string]func(*testing.T) (net.Conn, net.Conn){"tcp": tcpPair, "pipe": pipePair}
	for name, pair := range pairs {
		for _, idle := range []time.Duration{0, 5 * time.Second} {
			t.Run(fmt.Sprintf("%s/idle=%v", name, idle), func(t *testing.T) {
				if name == "pipe" && idle == 0 {
					t.Skip("splice path is for TCP connections")
				}
				r := startRig(t, idle, pair)
				up, down := make([]byte, 1<<20), make([]byte, 1<<20)
				rand.Read(up)
				rand.Read(down)

				var wg sync.WaitGroup
				send := func(c net.Conn, data []byte) {
					defer wg.Done()
					if _, err := c.Write(data); err != nil {
						t.Errorf("write: %v", err)
					}
				}
				recv := func(c net.Conn, want []byte) {
					defer wg.Done()
					got := make([]byte, len(want))
					c.SetReadDeadline(time.Now().Add(10 * time.Second))
					if _, err := io.ReadFull(c, got); err != nil {
						t.Errorf("read: %v", err)
					} else if !bytes.Equal(got, want) {
						t.Error("data arrived altered")
					}
				}
				wg.Add(4)
				go send(r.client, up)
				go send(r.server, down)
				go recv(r.server, up)
				go recv(r.client, down)
				wg.Wait()
			})
		}
	}
}

func TestProxyStreamsIdleTimeout(t *testing.T) {
	pairs := map[string]func(*testing.T) (net.Conn, net.Conn){"tcp": tcpPair, "pipe": pipePair}
	for name, pair := range pairs {
		t.Run(name, func(t *testing.T) {
			r := startRig(t, 300*time.Millisecond, pair)
			go io.Copy(r.server, r.server) // echo

			// Traffic every 100 ms keeps it open well past the idle timeout
			buf := make([]byte, 1)
			for i := 0; i < 10; i++ {
				r.client.SetDeadline(time.Now().Add(time.Second))
				if _, err := r.client.Write([]byte{'x'}); err != nil {
					t.Fatalf("write %d: %v", i, err)
				}
				if _, err := io.ReadFull(r.client, buf); err != nil {
					t.Fatalf("active connection closed after %d round trips: %v", i, err)
				}
				time.Sleep(100 * time.Millisecond)
			}

			// Once quiet it is closed
			start := time.Now()
			waitDone(t, r, 3*time.Second)
			if waited := time.Since(start); waited > 1500*time.Millisecond {
				t.Fatalf("quiet connection closed after %v, want about 300ms", waited)
			}
		})
	}
}

// A client that shuts down its sending side after the request, then waits for
// the answer: the answer must still arrive. Before, the first EOF closed both.
func TestProxyStreamsHalfClose(t *testing.T) {
	for _, idle := range []time.Duration{0, 5 * time.Second} {
		t.Run(fmt.Sprintf("idle=%v", idle), func(t *testing.T) {
			r := startRig(t, idle, tcpPair)
			go func() {
				req, _ := io.ReadAll(r.server) // until the client's FIN
				r.server.Write(append([]byte("reply:"), req...))
				r.server.Close()
			}()

			r.client.Write([]byte("hello"))
			r.client.(*net.TCPConn).CloseWrite()
			r.client.SetReadDeadline(time.Now().Add(3 * time.Second))
			got, err := io.ReadAll(r.client)
			if err != nil || string(got) != "reply:hello" {
				t.Fatalf("client got %q, %v; want the reply after its half-close", got, err)
			}
			waitDone(t, r, 3*time.Second)
		})
	}
}

// A half-closed connection whose other side never finishes must not linger forever
func TestProxyStreamsHalfCloseIsBoundedWithoutIdleTimeout(t *testing.T) {
	old := halfCloseGrace
	halfCloseGrace = 300 * time.Millisecond
	t.Cleanup(func() { halfCloseGrace = old })

	r := startRig(t, 0, tcpPair)
	r.client.(*net.TCPConn).CloseWrite()
	start := time.Now()
	waitDone(t, r, 3*time.Second)
	if waited := time.Since(start); waited < 250*time.Millisecond {
		t.Fatalf("ended after %v, before the grace period", waited)
	}
}

// A backend that closes normally (FIN) is passed on to the client, which then
// closes its own side; nothing is cut in between.
func TestProxyStreamsBackendCloseReachesClient(t *testing.T) {
	r := startRig(t, 5*time.Second, tcpPair)
	r.server.Write([]byte("bye"))
	r.server.Close()

	r.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(r.client) // data, then EOF
	if err != nil || string(got) != "bye" {
		t.Fatalf("client got %q, %v; want the data followed by EOF", got, err)
	}
	r.client.Close()
	waitDone(t, r, 2*time.Second)
}

// An error (here a reset) ends both directions at once, even if the other side stays silent
func TestProxyStreamsErrorClosesBothSides(t *testing.T) {
	r := startRig(t, 5*time.Second, tcpPair)
	r.server.(*net.TCPConn).SetLinger(0) // close sends a reset
	r.server.Close()

	waitDone(t, r, 2*time.Second)
	r.client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := r.client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client still connected after the backend connection failed")
	}
}

// Quiet connections must not hold copy buffers: that was 64 KiB each, and the
// reason a server with ~12k clients used over a gigabyte.
func TestProxyStreamsIdleConnectionsHoldNoBuffers(t *testing.T) {
	const n = 600
	measure := func() uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse + ms.StackInuse
	}

	before := measure()
	for i := 0; i < n; i++ {
		client, a := dialPairOrSkip(t)
		b, server := dialPairOrSkip(t)
		done := make(chan struct{})
		go func() { proxyStreams(a, b, time.Hour); close(done) }()
		t.Cleanup(func() { client.Close(); server.Close(); <-done })
	}
	time.Sleep(300 * time.Millisecond) // let every goroutine reach its wait
	after := measure()

	perConn := (after - before) / n
	t.Logf("%d idle connections: %d bytes each (heap + stacks)", n, perConn)
	// Two 32 KiB buffers alone were 64 KiB; stacks and bookkeeping are well under half of that
	if perConn > 28<<10 {
		t.Fatalf("%d bytes per idle connection, want under 28 KiB: copy buffers are being held while waiting", perConn)
	}
}

func dialPairOrSkip(t *testing.T) (c1, c2 net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot open that many sockets here: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	c1, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		if strings.Contains(err.Error(), "too many open files") {
			t.Skipf("cannot open that many sockets here: %v", err)
		}
		t.Fatal(err)
	}
	return c1, <-accepted
}
