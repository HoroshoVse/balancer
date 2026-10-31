package engine

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/pires/go-proxyproto"
)

// The client reaches the balancer over IPv4, while Xray is reached over IPv6.
// The PROXY header must still describe the original IPv4 connection, and the
// byte stream (including large XHTTP uploads) must arrive unchanged.
func TestIPv4ClientToIPv6Backend(t *testing.T) {
	for _, host := range []string{"::1", "[::1]"} {
		for _, version := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/proxy=%d", host, version), func(t *testing.T) {
				ln, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skipf("IPv6 loopback unavailable: %v", err)
				}
				t.Cleanup(func() { ln.Close() })
				var listener net.Listener = ln
				if version != 0 {
					listener = &proxyproto.Listener{Listener: ln, Policy: func(net.Addr) (proxyproto.Policy, error) {
						return proxyproto.REQUIRE, nil
					}}
				}
				seen := make(chan string, 1)
				go func() {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					seen <- conn.RemoteAddr().String()
					io.Copy(conn, conn)
				}()
				port := freePort(t, "tcp")
				startInstance(t, models.LoadBalancer{
					ID: 1900, Name: "ipv4-to-ipv6", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
					ProxyProtocolEnabled: version != 0, ProxyProtocolVersion: version,
					BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
						{ID: 1901, Address: host, Port: ln.Addr().(*net.TCPAddr).Port, Enabled: true},
					}},
				})
				conn, err := net.DialTimeout("tcp4", backendAddr("127.0.0.1", port), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				payload := bytes.Repeat([]byte("XHTTP IPv4 -> IPv6\x00\xff"), 100000)
				written := make(chan error, 1)
				go func() {
					_, err := conn.Write(payload)
					if err == nil {
						err = conn.(*net.TCPConn).CloseWrite()
					}
					written <- err
				}()
				reply, err := io.ReadAll(conn)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-written; err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(reply, payload) {
					t.Fatalf("byte stream changed: received %d bytes, want %d", len(reply), len(payload))
				}
				if got := <-seen; version != 0 && got != conn.LocalAddr().String() {
					t.Fatalf("PROXY source %s, want original IPv4 client %s", got, conn.LocalAddr())
				}
			})
		}
	}
}
