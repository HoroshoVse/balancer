package engine

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"html"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/caddyserver/certmagic"
)

// buildTLSConfig returns the certificate setup for SSL termination.
func (l *LoadBalancerInstance) buildTLSConfig() (*tls.Config, error) {
	switch {
	case l.Config.UsesACME():
		return l.acmeTLSConfig(), nil
	case l.Config.CertData != "" && l.Config.KeyData != "":
		cert, err := tls.X509KeyPair([]byte(l.Config.CertData), []byte(l.Config.KeyData))
		if err != nil {
			return nil, fmt.Errorf("invalid TLS certificate or key: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
	case l.Config.CertPath != "" && l.Config.KeyPath != "":
		cert, err := tls.LoadX509KeyPair(l.Config.CertPath, l.Config.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificate from path: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
	default:
		Logger.WarnLB(l.Config.Name, fmt.Sprintf("%s: no certificate configured, using a self-signed one", l.Config.Name))
		return generateSelfSignedCert()
	}
}

// ACME settings; variables so tests can use a temporary directory, a fake CA
// and short delays.
var (
	certStoragePath = "./certs/certmagic" // the persistent volume mapped in docker-compose
	acmeCA          = certmagic.LetsEncryptProductionCA
	acmeStartWait   = 5 * time.Second // how long Start waits for a stored certificate to load
	acmeRetryMin    = 2 * time.Minute // first retry; doubles up to acmeRetryMax (stays under Let's Encrypt's 5 failures/hour)
	acmeRetryMax    = time.Hour
)

// acmeTLSConfig manages certificates with a certmagic config owned by this
// instance, so balancers with different ACME settings don't overwrite each other.
func (l *LoadBalancerInstance) acmeTLSConfig() *tls.Config {
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return cfg, nil
		},
	})
	var cached sync.Map // domain -> true once its certificate is being served
	storage := newCertStorage()
	cfg = certmagic.New(cache, certmagic.Config{
		Storage: storage,
		OnEvent: func(_ context.Context, event string, data map[string]any) error {
			switch event {
			case "cached_managed_cert":
				if sans, ok := data["sans"].([]string); ok {
					for _, name := range sans {
						cached.Store(strings.ToLower(name), true)
					}
				}
			case "cert_failed", "cert_obtained":
				// Renewals run in the background long after manageCertificates returned
				if renewal, _ := data["renewal"].(bool); renewal {
					domain, _ := data["identifier"].(string)
					remaining, _ := data["remaining"].(time.Duration)
					err, _ := data["error"].(error)
					l.reportRenewal(domain, event == "cert_obtained", remaining, err)
				}
			}
			return nil
		},
	})
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:     acmeCA,
		Email:  l.Config.ACMEEmail, // optional; Let's Encrypt rejects made-up addresses
		Agreed: true,
	})
	cfg.Issuers = []certmagic.Issuer{issuer}
	l.acmeIssuer.Store(issuer) // lets the HTTP->HTTPS redirect answer HTTP-01 challenges

	go func() {
		<-l.ctx.Done()
		cache.Stop()
	}()

	domains := l.acmeDomains()
	if len(domains) > 0 {
		l.acmeStatus.Store("issuing")
		firstAttempt := make(chan struct{})
		missing := func() []string {
			var names []string
			for _, d := range domains {
				if _, ok := cached.Load(strings.ToLower(d)); !ok {
					names = append(names, d)
				}
			}
			return names
		}
		go l.manageCertificates(cfg, storage, issuer.IssuerKey(), domains, missing, firstAttempt)
		// A certificate already in storage loads in milliseconds: wait for it so
		// clients don't hit "no certificate available" right after a restart.
		// Obtaining a new one can take longer; that continues in the background.
		select {
		case <-firstAttempt:
		case <-time.After(acmeStartWait):
		case <-l.ctx.Done():
		}
	}
	return cfg.TLSConfig()
}

// acmeDomains returns the Let's Encrypt domains, or nil when ACME is off.
func (l *LoadBalancerInstance) acmeDomains() []string {
	if !l.Config.UsesACME() {
		return nil
	}
	var domains []string
	for _, d := range strings.Split(l.Config.ACMEDomains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}

// manageCertificates loads or obtains the certificates and keeps retrying
// with backoff until it succeeds: certmagic itself gives up on a certificate
// it cannot load. Before every attempt it repairs damaged files in storage.
// Failures and the recovery are reported to Telegram once each.
func (l *LoadBalancerInstance) manageCertificates(cfg *certmagic.Config, storage atomicFileStorage, issuerKey string, domains []string, missing func() []string, firstAttempt chan<- struct{}) {
	name := l.Config.Name
	delay := acmeRetryMin
	alerted := false
	for attempt := 0; ; attempt++ {
		for _, note := range storage.repair(l.ctx, issuerKey, domains) {
			Logger.WarnLB(name, fmt.Sprintf("%s: %s", name, note))
			l.alert(fmt.Sprintf("🛠 <b>%s</b>: %s", html.EscapeString(name), html.EscapeString(note)))
		}

		err := cfg.ManageSync(l.ctx, domains)
		if attempt == 0 {
			close(firstAttempt)
		}
		if l.ctx.Err() != nil {
			return
		}
		if err == nil {
			l.acmeStatus.Store("ok")
			l.acmeError.Store("")
			Logger.InfoLB(name, fmt.Sprintf("ACME certificate ready for domains: %v", domains))
			if alerted {
				l.alert(fmt.Sprintf("✅ <b>%s</b>: сертификат для %s снова в порядке.", html.EscapeString(name), html.EscapeString(strings.Join(domains, ", "))))
			}
			return
		}

		l.acmeStatus.Store("error")
		l.acmeError.Store(err.Error())
		noCert := missing()
		Logger.ErrorLB(name, fmt.Sprintf("Failed to manage ACME domains for %s: %v (no certificate for %v); retrying in %s", name, err, noCert, delay))
		if !alerted {
			alerted = true
			impact := "Текущий сертификат пока действует, клиенты подключаются."
			if len(noCert) > 0 {
				impact = fmt.Sprintf("Сертификата для %s нет — клиенты получают ошибку TLS.", strings.Join(noCert, ", "))
			}
			l.alert(fmt.Sprintf("🔒 <b>%s</b>: не удалось загрузить или выпустить сертификат. %s Повторю через %s.\n<code>%s</code>",
				html.EscapeString(name), html.EscapeString(impact), russianDelay(delay), html.EscapeString(err.Error())))
		}
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, acmeRetryMax)
	}
}

// reportRenewal alerts on the first failed renewal of a domain and on the
// renewal that finally succeeds after it.
func (l *LoadBalancerInstance) reportRenewal(domain string, ok bool, remaining time.Duration, err error) {
	name := html.EscapeString(l.Config.Name)
	if ok {
		if _, alerted := l.renewAlerted.LoadAndDelete(domain); alerted {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("Certificate for %s renewed", domain))
			l.alert(fmt.Sprintf("✅ <b>%s</b>: сертификат для %s продлён.", name, html.EscapeString(domain)))
		}
		return
	}
	reason := "unknown error"
	if err != nil {
		reason = err.Error()
	}
	Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to renew the certificate for %s (%s left): %s", domain, remaining.Round(time.Minute), reason))
	if _, already := l.renewAlerted.LoadOrStore(domain, true); already {
		return
	}
	left := "Сертификат уже истёк — клиенты получают ошибку TLS."
	if remaining > 0 {
		left = fmt.Sprintf("Текущий действует ещё %s, повторю позже.", russianRemaining(remaining))
	}
	l.alert(fmt.Sprintf("🔒 <b>%s</b>: не удалось продлить сертификат для %s. %s\n<code>%s</code>", name, html.EscapeString(domain), left, html.EscapeString(reason)))
}

// russianRemaining formats how long a certificate stays valid: "12 дн.", "5 ч".
func russianRemaining(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d дн.", d/(24*time.Hour))
	}
	return fmt.Sprintf("%d ч", max(d/time.Hour, 1))
}

// russianDelay formats a retry delay for alerts: "2 мин", "1 ч", "30 с".
func russianDelay(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d ч", d/time.Hour)
	case d >= time.Minute:
		return fmt.Sprintf("%d мин", d/time.Minute)
	default:
		return fmt.Sprintf("%d с", max(d/time.Second, 1))
	}
}

func generateSelfSignedCert() (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"Balancer Local Dev"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(time.Hour * 24 * 365),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

// newBackendTLSClient wraps a backend connection in TLS. Backend
// certificates are not verified (backends commonly use self-signed certs).
func newBackendTLSClient(conn net.Conn, serverName string, http2 bool) *tls.Conn {
	cfg := &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         serverName,
	}
	if http2 {
		cfg.NextProtos = []string{"h2", "http/1.1"}
	}
	return tls.Client(conn, cfg)
}

// maxServerNameTransports bounds how many distinct TLS server names get
// their own connection pool (the Host header is client-controlled).
const maxServerNameTransports = 256

type backendTransport struct {
	lbID uint

	proxyProtocolEnabled bool
	proxyProtocolVersion int
	http2Enabled         bool
	connectTimeout       time.Duration
	responseTimeout      time.Duration // 0: wait as long as the client does

	// One persistent *http.Transport per TLS server name: a pooled TLS
	// connection is bound to the SNI it was opened with, so requests for
	// different names must not share it. Plain HTTP uses the "" entry.
	mu         sync.Mutex
	transports map[string]*http.Transport
}

func (t *backendTransport) transportFor(serverName string) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.transports == nil {
		t.transports = make(map[string]*http.Transport)
	}
	if tr, ok := t.transports[serverName]; ok {
		return tr
	}
	if len(t.transports) >= maxServerNameTransports {
		serverName = ""
		if tr, ok := t.transports[serverName]; ok {
			return tr
		}
	}
	tr := t.newTransport(serverName)
	t.transports[serverName] = tr
	return tr
}

func (t *backendTransport) newTransport(serverName string) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   t.connectTimeout,
		KeepAlive: 30 * time.Second,
	}
	tr := &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: t.responseTimeout,
		// HTTP/2 needs TLS here (no h2c); it is negotiated via ALPN in DialTLSContext.
		ForceAttemptHTTP2: t.http2Enabled,
		// A PROXY header describes a single client, so such connections can't be reused.
		DisableKeepAlives: t.proxyProtocolEnabled,
	}

	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if t.proxyProtocolEnabled {
			clientAddr, _ := ctx.Value(ctxKeyClientAddr).(string)
			localAddr, _ := ctx.Value(http.LocalAddrContextKey).(net.Addr)
			header := buildProxyHeader(t.proxyProtocolVersion, tcpAddrFromString(clientAddr), localAddr)
			if _, err := header.WriteTo(conn); err != nil {
				conn.Close()
				return nil, err
			}
		}
		return conn, nil
	}

	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		baseConn, err := tr.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		tlsConn := newBackendTLSClient(baseConn, serverName, t.http2Enabled)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			baseConn.Close()
			return nil, err
		}
		return tlsConn, nil
	}
	return tr
}

func tcpAddrFromString(s string) net.Addr {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	p, _ := net.LookupPort("tcp", port)
	return &net.TCPAddr{IP: ip, Port: p}
}

func (t *backendTransport) closeIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tr := range t.transports {
		tr.CloseIdleConnections()
	}
}

func (t *backendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	target, _ := req.Context().Value(ctxKeyBackend).(*models.BackendServer)

	// TLS server name: the node's SNI override, otherwise the requested host.
	serverName := ""
	if target != nil && target.TLSEnabled {
		serverName = target.SNI
		if serverName == "" {
			serverName = req.Host
			if host, _, err := net.SplitHostPort(serverName); err == nil {
				serverName = host
			}
		}
	}

	resp, err := t.transportFor(serverName).RoundTrip(req)
	latency := time.Since(start)

	isError := err != nil || (resp != nil && resp.StatusCode >= 500)
	Metrics.RecordRequest(t.lbID, latency, isError)
	if target != nil {
		Metrics.RecordBackendRequest(target.ID, latency, isError)
	}

	return resp, err
}
