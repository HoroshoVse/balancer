package api

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/models"
)

type TestConnectionRequest struct {
	Address              string `json:"address"`
	Port                 int    `json:"port"`
	TLSEnabled           bool   `json:"tls_enabled"`
	HTTP2Enabled         bool   `json:"http2_enabled"`
	SNI                  string `json:"sni"`
	ProxyProtocolEnabled bool   `json:"proxy_protocol_enabled"`
	ProxyProtocolVersion int    `json:"proxy_protocol_version"`
}

type TestConnectionResponse struct {
	Success bool   `json:"success"`
	Logs    string `json:"logs"`
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req TestConnectionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Address = models.NormalizeBackendAddress(req.Address)
	if req.Address == "" || req.Port < 1 || req.Port > 65535 {
		http.Error(w, "address and a port between 1 and 65535 are required", http.StatusBadRequest)
		return
	}
	if req.ProxyProtocolVersion == 0 {
		req.ProxyProtocolVersion = 2
	}
	if req.ProxyProtocolEnabled && req.ProxyProtocolVersion != 1 && req.ProxyProtocolVersion != 2 {
		http.Error(w, "proxy protocol version must be 1 or 2", http.StatusBadRequest)
		return
	}

	logs := ""

	target := net.JoinHostPort(req.Address, strconv.Itoa(req.Port))
	logs += fmt.Sprintf("Testing connection to %s...\n", target)

	// Step 1: TCP Connect
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		logs += fmt.Sprintf("❌ TCP Connection failed: %v\n", err)
		s.respondJSON(w, TestConnectionResponse{Success: false, Logs: logs})
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	logs += fmt.Sprintf("✅ TCP Connection successful (took %v)\n", time.Since(start))
	logs += fmt.Sprintf("ℹ️ Connected from %s to %s.\n", conn.LocalAddr(), conn.RemoteAddr())

	if req.ProxyProtocolEnabled {
		if err := engine.WriteProxyHeader(conn, req.ProxyProtocolVersion, conn.LocalAddr(), conn.RemoteAddr()); err != nil {
			logs += fmt.Sprintf("❌ PROXY protocol header failed: %v\n", err)
			s.respondJSON(w, TestConnectionResponse{Success: false, Logs: logs})
			return
		}
		logs += fmt.Sprintf("ℹ️ PROXY protocol v%d header sent; acceptance by the node is not verified.\n", req.ProxyProtocolVersion)
	}

	if !req.TLSEnabled {
		logs += "ℹ️ TCP reachability only: TLS, VLESS/REALITY authentication and VPN traffic were not tested.\n"
		s.respondJSON(w, TestConnectionResponse{Success: true, Logs: logs})
		return
	}

	// Step 2: TLS Handshake
	logs += "Attempting TLS Handshake...\n"
	start = time.Now()

	nextProtos := []string{"http/1.1"}
	if req.HTTP2Enabled {
		nextProtos = []string{"h2", "http/1.1"}
		logs += "ℹ️ HTTP/2 (ALPN h2) is enabled.\n"
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         nextProtos,
		ServerName:         strings.TrimSpace(req.SNI),
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = req.Address
	}

	tlsConn := tls.Client(conn, tlsConfig)
	// Workaround: tlsConn.Handshake() does not take a context natively easily without context dialer
	// Use deadline
	tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	err = tlsConn.Handshake()
	if err != nil {
		logs += fmt.Sprintf("❌ TLS Handshake failed: %v\n", err)
		logs += "⚠️ The backend node might not be running HTTPS, or it is dropping the connection.\n"
		s.respondJSON(w, TestConnectionResponse{Success: false, Logs: logs})
		return
	}

	state := tlsConn.ConnectionState()
	logs += fmt.Sprintf("✅ TLS Handshake successful (took %v)\n", time.Since(start))
	logs += fmt.Sprintf("ℹ️ Negotiated Protocol (ALPN): %s\n", state.NegotiatedProtocol)

	if req.HTTP2Enabled && state.NegotiatedProtocol != "h2" {
		logs += "⚠️ Warning: HTTP/2 was requested, but backend negotiated " + state.NegotiatedProtocol + "\n"
	}

	tlsConn.Close()
	logs += "ℹ️ TLS reachability only: VLESS/REALITY authentication and VPN traffic were not tested.\n"

	s.respondJSON(w, TestConnectionResponse{Success: true, Logs: logs})
}

func (s *Server) respondJSON(w http.ResponseWriter, data interface{}) {
	writeJSON(w, http.StatusOK, data)
}
