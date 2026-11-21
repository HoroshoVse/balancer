package engine

import (
	"bytes"
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

func TestReplayConn(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go client.Write([]byte("tail"))
	c := &replayConn{Conn: server, prefix: []byte("head")}
	buf := make([]byte, 8)
	if _, err := io.ReadFull(c, buf); err != nil || !bytes.Equal(buf, []byte("headtail")) {
		t.Fatalf("replayed %q: %v", buf, err)
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
