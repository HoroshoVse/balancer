package engine

import (
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

func TestClientSNIRoutingPreservesClientHello(t *testing.T) {
	var listeners []net.Listener
	for range 2 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, ln)
		defer ln.Close()
	}
	seen := make(chan int, 2)
	for i, ln := range listeners {
		go func(i int, ln net.Listener) {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			name, raw := peekClientSNI(conn)
			if name == "two.example" && len(raw) > 5 && raw[0] == 22 {
				seen <- i
			}
		}(i, ln)
	}
	port := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 991, Name: "sni-route", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 991, Name: "one", Address: "127.0.0.1", Port: listeners[0].Addr().(*net.TCPAddr).Port, Enabled: true, ClientSNI: "one.example"},
			{ID: 992, Name: "two", Address: "127.0.0.1", Port: listeners[1].Addr().(*net.TCPAddr).Port, Enabled: true, ClientSNI: "two.example"},
		}},
	})
	conn, err := net.Dial("tcp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = tls.Client(conn, &tls.Config{ServerName: "two.example", InsecureSkipVerify: true}).Handshake()
	select {
	case i := <-seen:
		if i != 1 {
			t.Fatalf("reached backend %d, want 1", i)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ClientHello did not reach matching backend")
	}
}

func TestClientSNIMatchesExactNamesOnly(t *testing.T) {
	if !matchesClientSNI("one.example, TWO.example", "two.example") {
		t.Fatal("second configured name should match")
	}
	if matchesClientSNI("one.example, two.example", "sub.two.example") {
		t.Fatal("a subdomain must not match an exact name")
	}
}

func TestClientSNIRoutingPreservesHalfClose(t *testing.T) {
	// Capture a genuine ClientHello without needing a TLS server certificate.
	server, client := net.Pipe()
	hello := make(chan []byte, 1)
	go func() {
		_, raw := peekClientSNI(server)
		hello <- raw
		server.Close()
	}()
	_ = tls.Client(client, &tls.Config{ServerName: "half.example"}).Handshake()
	client.Close()
	raw := <-hello
	if len(raw) == 0 {
		t.Fatal("empty ClientHello")
	}

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := backend.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, len(raw))
		if _, err := io.ReadFull(conn, buf); err != nil {
			got <- "read hello: " + err.Error()
			return
		}
		conn.Write([]byte("ready"))
		conn.(*net.TCPConn).CloseWrite()
		var more [5]byte
		if _, err := io.ReadFull(conn, more[:]); err != nil {
			got <- "read after: " + err.Error()
			return
		}
		got <- string(more[:])
	}()
	port := freePort(t, "tcp")
	startInstance(t, models.LoadBalancer{
		ID: 993, Name: "sni-half-close", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{ID: 993, Address: "127.0.0.1", Port: backend.Addr().(*net.TCPAddr).Port, Enabled: true, ClientSNI: "half.example"},
		}},
	})
	upstream, err := net.Dial("tcp", backendAddr("127.0.0.1", port))
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstream.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := upstream.Write(raw); err != nil {
		t.Fatal(err)
	}
	var ready [5]byte
	if _, err := io.ReadFull(upstream, ready[:]); err != nil || string(ready[:]) != "ready" {
		t.Fatalf("ready=%q err=%v", ready, err)
	}
	if _, err := upstream.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	if result := <-got; result != "after" {
		t.Fatalf("backend received %q", result)
	}
}
