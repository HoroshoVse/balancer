package engine

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// Set BALANCER_TEST_XRAY to an official Xray binary to exercise a real VPN
// client and node. Every address is loopback; keys and UUIDs are test-only.
func TestXrayXHTTPRealityIPv4ToIPv6(t *testing.T) {
	binary := os.Getenv("BALANCER_TEST_XRAY")
	if binary == "" {
		t.Skip("set BALANCER_TEST_XRAY to run the Xray integration test")
	}
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	nodePort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := base64.RawURLEncoding.EncodeToString(key.Bytes())
	publicKey := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	const uuid = "449d56a7-d144-49bc-8226-9d984ab5ed38"
	const shortID = "1122334455667788"

	cover := httptest.NewUnstartedServer(http.NotFoundHandler())
	cover.EnableHTTP2 = true
	cover.StartTLS()
	t.Cleanup(cover.Close)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(w, r.Body)
	}))
	t.Cleanup(target.Close)

	startTestXray(t, binary, map[string]any{
		"log": map[string]any{"loglevel": "debug"},
		"inbounds": []any{map[string]any{
			"listen": "::1", "port": nodePort, "protocol": "vless",
			"settings": map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": uuid}}},
			"streamSettings": map[string]any{
				"network": "xhttp", "security": "reality",
				"realitySettings": map[string]any{
					"dest": cover.Listener.Addr().String(), "serverNames": []string{"example.com"},
					"privateKey": privateKey, "shortIds": []string{shortID},
				},
				"xhttpSettings": map[string]any{"path": "/balancer-test/", "mode": "auto"},
				"sockopt":       map[string]any{"acceptProxyProtocol": true},
			},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "freedom",
			"settings": map[string]any{"finalRules": []any{map[string]any{
				"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"},
				"port": target.Listener.Addr().(*net.TCPAddr).Port,
			}}},
		}},
	}, backendAddr("::1", nodePort))

	for _, version := range []int{1, 2} {
		for _, mode := range []string{"auto", "packet-up"} {
			t.Run(fmt.Sprintf("proxy=%d/mode=%s", version, mode), func(t *testing.T) {
				port := freePort(t, "tcp")
				startInstance(t, models.LoadBalancer{
					ID: 1950, Name: "xray-ipv4-to-ipv6", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
					ProxyProtocolEnabled: true, ProxyProtocolVersion: version,
					BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
						{ID: 1951, Address: "::1", Port: nodePort, Enabled: true},
					}},
				})
				clientPort := freePort(t, "tcp")
				startTestXray(t, binary, map[string]any{
					"log":      map[string]any{"loglevel": "debug"},
					"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "http"}},
					"outbounds": []any{map[string]any{
						"protocol": "vless",
						"settings": map[string]any{"vnext": []any{map[string]any{
							"address": "127.0.0.1", "port": port,
							"users": []any{map[string]any{"id": uuid, "encryption": "none"}},
						}}},
						"streamSettings": map[string]any{
							"network": "xhttp", "security": "reality",
							"realitySettings": map[string]any{
								"serverName": "example.com", "fingerprint": "chrome", "shortId": shortID,
								"publicKey": publicKey, "password": publicKey,
							},
							"xhttpSettings": map[string]any{"path": "/balancer-test/", "mode": mode},
						},
					}},
				}, backendAddr("127.0.0.1", clientPort))
				proxyURL, _ := url.Parse("http://" + backendAddr("127.0.0.1", clientPort))
				transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
				t.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
				payload := bytes.Repeat([]byte("VLESS/XHTTP/REALITY IPv4 -> IPv6\n"), 40000)
				resp, err := client.Post(target.URL, "application/octet-stream", bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
					t.Fatalf("VPN transfer: status=%d bytes=%d want=%d err=%v", resp.StatusCode, len(body), len(payload), err)
				}
			})
		}
	}
}

func startTestXray(t *testing.T, binary string, config any, addr string) {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "xray.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "run", "-c", path)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		logFile.Close()
		if t.Failed() {
			logs, _ := os.ReadFile(logPath)
			t.Logf("Xray logs:\n%s", logs)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("Xray did not listen on %s", addr)
}
