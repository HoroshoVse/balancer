package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/caddyserver/certmagic"
)

// testCertPEM returns a self-signed certificate and key for names.
func testCertPEM(t *testing.T, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// useTestCertStorage points certmagic storage at a temporary directory and the
// ACME CA at a local server that fails every request (no real network traffic).
// It returns the storage and the issuer key under which certificates are stored.
func useTestCertStorage(t *testing.T) (atomicFileStorage, string) {
	t.Helper()
	ca := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "test CA is down", http.StatusInternalServerError)
	}))
	t.Cleanup(ca.Close)

	oldPath, oldCA, oldWait, oldRetry := certStoragePath, acmeCA, acmeStartWait, acmeRetryMin
	certStoragePath = filepath.Join(t.TempDir(), "certmagic")
	acmeCA = ca.URL + "/directory"
	acmeStartWait = 5 * time.Second
	acmeRetryMin = 100 * time.Millisecond
	t.Cleanup(func() { certStoragePath, acmeCA, acmeStartWait, acmeRetryMin = oldPath, oldCA, oldWait, oldRetry })

	issuer := certmagic.NewACMEIssuer(&certmagic.Config{}, certmagic.ACMEIssuer{CA: acmeCA})
	return newCertStorage(), issuer.IssuerKey()
}

func writeSite(t *testing.T, s atomicFileStorage, issuerKey, domain string, certPEM, keyPEM, meta []byte) {
	t.Helper()
	files := map[string][]byte{
		certmagic.StorageKeys.SiteCert(issuerKey, domain):       certPEM,
		certmagic.StorageKeys.SitePrivateKey(issuerKey, domain): keyPEM,
		certmagic.StorageKeys.SiteMeta(issuerKey, domain):       meta,
	}
	for key, data := range files {
		if data == nil {
			continue
		}
		name := s.Filename(key)
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// loadsInCertmagic reports whether certmagic itself can load the stored certificate.
func loadsInCertmagic(t *testing.T, s atomicFileStorage, domain string) error {
	t.Helper()
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil }})
	t.Cleanup(cache.Stop)
	cfg = certmagic.New(cache, certmagic.Config{Storage: s})
	cfg.Issuers = []certmagic.Issuer{certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{CA: acmeCA})}
	_, err := cfg.CacheManagedCertificate(context.Background(), domain)
	return err
}

func TestAtomicFileStorageStore(t *testing.T) {
	s, _ := useTestCertStorage(t)
	ctx := context.Background()
	for _, value := range []string{"first", "second, longer value"} {
		if err := s.Store(ctx, "certificates/x/a.json", []byte(value)); err != nil {
			t.Fatal(err)
		}
		got, err := s.Load(ctx, "certificates/x/a.json")
		if err != nil || string(got) != value {
			t.Fatalf("Load = %q, %v; want %q", got, err, value)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Filename("certificates/x/a.json")))
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only a.json (no temp files left)", len(entries))
	}
	info, _ := os.Stat(s.Filename("certificates/x/a.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
}

func TestRepairRebuildsBrokenMetadata(t *testing.T) {
	for name, meta := range map[string][]byte{"empty": {}, "truncated": []byte(`{"sans":["exa`), "missing": nil} {
		t.Run(name, func(t *testing.T) {
			s, issuerKey := useTestCertStorage(t)
			certPEM, keyPEM := testCertPEM(t, "example.test", "www.example.test")
			writeSite(t, s, issuerKey, "example.test", certPEM, keyPEM, meta)
			if name != "missing" && loadsInCertmagic(t, s, "example.test") == nil {
				t.Fatal("certmagic loaded the certificate despite broken metadata; the test no longer reproduces the failure")
			}

			notes := s.repair(context.Background(), issuerKey, []string{"example.test"})
			if len(notes) != 1 || !strings.Contains(notes[0], "восстановлен") {
				t.Fatalf("notes = %q", notes)
			}
			data, _ := s.Load(context.Background(), certmagic.StorageKeys.SiteMeta(issuerKey, "example.test"))
			var got struct{ SANs []string }
			if err := json.Unmarshal(data, &got); err != nil || !slices.Equal(got.SANs, []string{"example.test", "www.example.test"}) {
				t.Fatalf("metadata %s (%v)", data, err)
			}
			if err := loadsInCertmagic(t, s, "example.test"); err != nil {
				t.Fatalf("certmagic still cannot load the certificate: %v", err)
			}
		})
	}
}

func TestRepairMovesBrokenCertificate(t *testing.T) {
	s, issuerKey := useTestCertStorage(t)
	certPEM, _ := testCertPEM(t, "example.test")
	writeSite(t, s, issuerKey, "example.test", certPEM, []byte{}, []byte(`{"sans":["example.test"]}`))

	notes := s.repair(context.Background(), issuerKey, []string{"example.test"})
	if len(notes) != 1 || !strings.Contains(notes[0], "будет выпущен новый") {
		t.Fatalf("notes = %q", notes)
	}
	if _, err := os.Stat(s.Filename(certmagic.StorageKeys.SiteCert(issuerKey, "example.test"))); !os.IsNotExist(err) {
		t.Fatalf("broken certificate still in place (%v)", err)
	}
	moved, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path), "broken", issuerKey, "example.test-*", "example.test.crt"))
	if len(moved) != 1 {
		t.Fatalf("broken files not kept for inspection: %v", moved)
	}
}

func TestRepairLeavesHealthyCertificate(t *testing.T) {
	s, issuerKey := useTestCertStorage(t)
	certPEM, keyPEM := testCertPEM(t, "example.test")
	meta := []byte(`{"sans":["example.test"],"issuer_data":{"url":"x"}}`)
	writeSite(t, s, issuerKey, "example.test", certPEM, keyPEM, meta)

	if notes := s.repair(context.Background(), issuerKey, []string{"example.test", "absent.test"}); len(notes) != 0 {
		t.Fatalf("notes = %q, want none", notes)
	}
	data, _ := s.Load(context.Background(), certmagic.StorageKeys.SiteMeta(issuerKey, "example.test"))
	if string(data) != string(meta) {
		t.Fatalf("metadata rewritten: %s", data)
	}
}

func acmeLB(t *testing.T, id uint) models.LoadBalancer {
	return models.LoadBalancer{
		ID: id, Name: "acme", Protocol: "https", SSLEnabled: true,
		ACMEEnabled: true, ACMEDomains: "example.test", ACMEEmail: "ops@example.test",
		ListenIP: "127.0.0.1", ListenPort: freePort(t, "tcp"),
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{{ID: id + 1, Address: "127.0.0.1", Port: 1, Enabled: true}}},
	}
}

func servedNames(t *testing.T, port int) ([]string, error) {
	t.Helper()
	conn, err := tls.Dial("tcp", backendAddr("127.0.0.1", port), &tls.Config{ServerName: "example.test", InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].DNSNames, nil
}

func waitACME(t *testing.T, inst *LoadBalancerInstance, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if inst.acmeStatus.Load() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ACME status %q (%v), want %q", inst.acmeStatus.Load(), inst.acmeError.Load(), want)
}

// The production incident: the stored certificate is valid but its metadata
// file is empty. The certificate must be served from the first connection.
func TestACMEServesStoredCertificateWithBrokenMetadata(t *testing.T) {
	s, issuerKey := useTestCertStorage(t)
	certPEM, keyPEM := testCertPEM(t, "example.test")
	writeSite(t, s, issuerKey, "example.test", certPEM, keyPEM, []byte{})

	cfg := acmeLB(t, 980)
	inst := startInstance(t, cfg)
	names, err := servedNames(t, cfg.ListenPort)
	if err != nil {
		t.Fatalf("TLS handshake right after start: %v", err)
	}
	if !slices.Contains(names, "example.test") {
		t.Fatalf("served certificate for %v", names)
	}
	waitACME(t, inst, "ok")
}

// When no certificate can be loaded or obtained, the instance keeps retrying
// and starts serving as soon as one becomes available.
func TestACMERetriesUntilCertificateAvailable(t *testing.T) {
	s, issuerKey := useTestCertStorage(t)
	cfg := acmeLB(t, 990)
	inst := startInstance(t, cfg)
	waitACME(t, inst, "error")
	if _, err := servedNames(t, cfg.ListenPort); err == nil {
		t.Fatal("handshake succeeded without any certificate")
	}

	// e.g. restored from a backup; the metadata is missing as well
	certPEM, keyPEM := testCertPEM(t, "example.test")
	writeSite(t, s, issuerKey, "example.test", certPEM, keyPEM, nil)
	waitACME(t, inst, "ok")
	if _, err := servedNames(t, cfg.ListenPort); err != nil {
		t.Fatalf("handshake after recovery: %v", err)
	}
}

func TestHandshakeLogSummary(t *testing.T) {
	h := newHandshakeLog("lb", []string{"DNS.example"})
	for i := 0; i < 5; i++ {
		h.Write([]byte("http: TLS handshake error from 203.0.113.9:4000: no certificate available for 'dns.example'\n"))
	}
	h.Write([]byte("http: TLS handshake error from [2001:db8::1]:443: read tcp 10.0.0.1:443->[2001:db8::1]:443: read: connection reset by peer\n"))
	h.Write([]byte("http: TLS handshake error from 198.51.100.1:5: EOF\n"))
	if h.count != 7 || h.reasons["no certificate available for 'dns.example'"] != 5 || h.reasons["connection reset by peer"] != 1 || h.reasons["EOF"] != 1 {
		t.Fatalf("count %d, reasons %v", h.count, h.reasons)
	}

	// The buffer is capped, so compare the newest entry rather than counts
	lastLog := func() LogEntry {
		logs := Logger.GetLogs()
		return logs[len(logs)-1]
	}
	h.flush(time.Minute)
	last := lastLog()
	if last.Level != "ERROR" || last.LBName != "lb" || !strings.Contains(last.Message, "7 failed TLS handshakes") || !strings.Contains(last.Message, "no certificate available for 'dns.example' ×5") {
		t.Fatalf("summary %+v", last)
	}
	h.flush(time.Minute)
	if got := lastLog(); got != last {
		t.Fatalf("empty interval logged %+v", got)
	}

	// Scanners connecting by IP address are not an outage
	h.Write([]byte("http: TLS handshake error from 198.51.100.1:5: no certificate available for '10.0.1.11'\n"))
	h.flush(time.Minute)
	if got := lastLog(); got.Level != "WARN" || !strings.Contains(got.Message, "'10.0.1.11' ×1") {
		t.Fatalf("scanner summary %+v", got)
	}
}
