package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
)

func connectionTest(t *testing.T, body any) (int, TestConnectionResponse) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/test-connection", &buf)
	(&Server{}).testConnection(rec, req)
	var response TestConnectionResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, response
}

func TestConnectionSendsProxyBeforeTLSOverIPv6(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ln, err := net.Listen("tcp6", "[::1]:0")
			if err != nil {
				t.Skipf("IPv6 loopback unavailable: %v", err)
			}
			t.Cleanup(func() { ln.Close() })
			// Reuse the test server's certificate without making an HTTP request.
			certServer := httptest.NewTLSServer(http.NotFoundHandler())
			t.Cleanup(certServer.Close)
			tlsConfig := certServer.TLS.Clone()
			tlsConfig.NextProtos = []string{"h2"}
			listener := tls.NewListener(&proxyproto.Listener{Listener: ln, Policy: func(net.Addr) (proxyproto.Policy, error) {
				return proxyproto.REQUIRE, nil
			}}, tlsConfig)
			seen := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					seen <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				c := conn.(*tls.Conn)
				if err := c.Handshake(); err != nil {
					seen <- err
					return
				}
				if got := c.ConnectionState().ServerName; got != "node.example" {
					seen <- fmt.Errorf("SNI %q, want node.example", got)
					return
				}
				seen <- nil
			}()
			code, response := connectionTest(t, TestConnectionRequest{
				Address: " [::1] ", Port: ln.Addr().(*net.TCPAddr).Port,
				TLSEnabled: true, HTTP2Enabled: true, SNI: "node.example",
				ProxyProtocolEnabled: true, ProxyProtocolVersion: version,
			})
			if code != http.StatusOK || !response.Success {
				t.Fatalf("diagnostics: %d %+v", code, response)
			}
			if !strings.Contains(response.Logs, fmt.Sprintf("PROXY protocol v%d header sent", version)) || !strings.Contains(response.Logs, "(ALPN): h2") {
				t.Fatalf("missing protocol diagnostics: %s", response.Logs)
			}
			select {
			case err := <-seen:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("backend did not complete its handshake")
			}
		})
	}
}

func TestConnectionTCPDoesNotClaimVPNWorks(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	code, response := connectionTest(t, TestConnectionRequest{Address: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port})
	if code != http.StatusOK || !response.Success || !strings.Contains(response.Logs, "VLESS/REALITY authentication and VPN traffic were not tested") {
		t.Fatalf("diagnostics: %d %+v", code, response)
	}
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestConnectionRejectsInvalidProxyVersion(t *testing.T) {
	code, _ := connectionTest(t, TestConnectionRequest{Address: "127.0.0.1", Port: 443, ProxyProtocolEnabled: true, ProxyProtocolVersion: 3})
	if code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", code)
	}
}
