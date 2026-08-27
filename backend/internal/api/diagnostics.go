package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type TestConnectionRequest struct {
	Address      string `json:"address"`
	Port         int    `json:"port"`
	TLSEnabled   bool   `json:"tls_enabled"`
	HTTP2Enabled bool   `json:"http2_enabled"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	logs := ""

	target := fmt.Sprintf("%s:%d", req.Address, req.Port)
	logs += fmt.Sprintf("Testing connection to %s...\n", target)

	// Step 1: TCP Connect
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		logs += fmt.Sprintf("❌ TCP Connection failed: %v\n", err)
		s.respondJSON(w, TestConnectionResponse{Success: false, Logs: logs})
		return
	}
	logs += fmt.Sprintf("✅ TCP Connection successful (took %v)\n", time.Since(start))
	
	if !req.TLSEnabled {
		conn.Close()
		logs += "✅ Configuration complete (HTTP only).\n"
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
	logs += "✅ Configuration complete.\n"
	
	s.respondJSON(w, TestConnectionResponse{Success: true, Logs: logs})
}

func (s *Server) respondJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
