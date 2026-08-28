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
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"time"



	"github.com/balancer/backend/internal/models"
	"github.com/caddyserver/certmagic"
	"github.com/pires/go-proxyproto"
	"github.com/quic-go/quic-go/http3"
	"gorm.io/gorm"
)

type LoadBalancerInstance struct {
	Config        models.LoadBalancer
	db            *gorm.DB
	healthChecker *HealthChecker
	
	httpServer  *http.Server
	httpsServer *http.Server
	http3Server *http3.Server
	tcpListener net.Listener
	transport   *backendTransport // reference to close idle conns on failover
	
	backends atomic.Value // Store slice of *models.BackendServer
	strategy Strategy
	
	cancel   context.CancelFunc
	
	acmeStatus atomic.Value // Store string
	acmeError  atomic.Value // Store string

	udpSessions sync.Map // Map clientAddr(string) -> *udpSession
}

type udpSession struct {
	conn     net.Conn
	lastSeen time.Time
}

func NewLoadBalancerInstance(config models.LoadBalancer, db *gorm.DB, hc *HealthChecker) *LoadBalancerInstance {
	inst := &LoadBalancerInstance{
		Config:        config,
		db:            db,
		healthChecker: hc,
	}
	inst.acmeStatus.Store("")
	inst.acmeError.Store("")

	switch config.Algorithm {
	case "round_robin":
		inst.strategy = &RoundRobin{}
	case "least_conn":
		inst.strategy = &LeastConnections{}
	case "weighted_round_robin":
		inst.strategy = &WeightedRoundRobin{}
	case "ip_hash":
		inst.strategy = &IPHash{}
	case "failover":
		inst.strategy = &Failover{}
	default:
		inst.strategy = &RoundRobin{} // Default
	}

	return inst
}

func (l *LoadBalancerInstance) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel

	l.updateBackends()

	if l.Config.Protocol == "tcp" {
		return l.startTCP(ctx)
	} else if l.Config.Protocol == "udp" {
		return l.startUDP(ctx)
	} else if l.Config.Protocol == "https" && !l.Config.SSLEnabled {
		// TLS Passthrough mode: user selected HTTPS but disabled SSL termination on the LB
		Logger.InfoLB(l.Config.Name, fmt.Sprintf("Running %s in TLS Passthrough (TCP) mode", l.Config.Name))
		return l.startTCP(ctx)
	}
	return l.startHTTP(ctx)
}

func (l *LoadBalancerInstance) updateBackends() {
	var primaryBackends []*models.BackendServer
	var backupBackends []*models.BackendServer

	for i := range l.Config.BackendGroup.Backends {
		b := &l.Config.BackendGroup.Backends[i]
		if !b.Enabled {
			continue
		}

		// Check health status
		if l.healthChecker != nil && !l.healthChecker.IsHealthy(b.ID) {
			continue // skip unhealthy backends
		}

		if b.Backup {
			backupBackends = append(backupBackends, b)
		} else {
			primaryBackends = append(primaryBackends, b)
		}
	}

	// Failover logic: use primaries if any are healthy, otherwise use backups
	var activeBackends []*models.BackendServer
	if len(primaryBackends) > 0 {
		activeBackends = primaryBackends
	} else if len(backupBackends) > 0 {
		Logger.WarnLB(l.Config.Name, fmt.Sprintf("[FAILOVER] %s: all primary backends DOWN, switching to backup nodes", l.Config.Name))
		activeBackends = backupBackends
	}

	// Build names for logging
	names := make([]string, len(activeBackends))
	for i, b := range activeBackends {
		names[i] = fmt.Sprintf("%s(%s:%d)", b.Name, b.Address, b.Port)
	}

	if len(activeBackends) == 0 {
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("[CRITICAL] %s: ALL backends (primary + backup) are DOWN", l.Config.Name))
		l.backends.Store([]*models.BackendServer{})
	} else {
		Logger.InfoLB(l.Config.Name, fmt.Sprintf("[BACKENDS] %s: active backends updated to: %v", l.Config.Name, names))
		l.backends.Store(activeBackends)
	}

	// Close idle HTTP connections so requests are forced to reconnect
	// to the new set of active backends (critical for failover/failback)
	if l.transport != nil && l.transport.inner != nil {
		l.transport.inner.CloseIdleConnections()
	}
}

func (l *LoadBalancerInstance) startTCP(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", l.Config.ListenIP, l.Config.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l.tcpListener = listener

	Logger.InfoLB(l.Config.Name, fmt.Sprintf("TCP LoadBalancer %s listening on %s", l.Config.Name, addr))

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // Graceful shutdown
			default:
				Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Accept error on %s: %v", l.Config.Name, err))
				continue
			}
		}

		go l.handleTCPConnection(conn)
	}
}

func (l *LoadBalancerInstance) handleTCPConnection(clientConn net.Conn) {
	backends := l.backends.Load().([]*models.BackendServer)
	clientIP, _, _ := net.SplitHostPort(clientConn.RemoteAddr().String())
	target := l.strategy.Next(backends, clientIP)
	if target == nil {
		clientConn.Close()
		return
	}

	targetAddr := fmt.Sprintf("%s:%d", target.Address, target.Port)
	start := time.Now()
	backendConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		Metrics.RecordBackendRequest(target.ID, time.Since(start), true)
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
		clientConn.Close()
		return
	}

	if l.Config.ProxyProtocolEnabled {
		clientIP, clientPort, _ := net.SplitHostPort(clientConn.RemoteAddr().String())
		destIP, destPort, _ := net.SplitHostPort(clientConn.LocalAddr().String())
		
		header := &proxyproto.Header{
			Version:           byte(l.Config.ProxyProtocolVersion),
			Command:           proxyproto.PROXY,
			TransportProtocol: proxyproto.TCPv4,
			SourceAddr: &net.TCPAddr{
				IP:   net.ParseIP(clientIP),
				Port: parsePort(clientPort),
			},
			DestinationAddr: &net.TCPAddr{
				IP:   net.ParseIP(destIP),
				Port: parsePort(destPort),
			},
		}
		
		if header.SourceAddr.(*net.TCPAddr).IP.To4() == nil {
			header.TransportProtocol = proxyproto.TCPv6
		}
		
		_, err = header.WriteTo(backendConn)
		if err != nil {
			Logger.Error(fmt.Sprintf("Failed to write PROXY protocol header: %v", err))
			backendConn.Close()
			clientConn.Close()
			return
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(backendConn, clientConn)
		backendConn.Close()
	}()
	go func() {
		defer wg.Done()
		io.Copy(clientConn, backendConn)
		clientConn.Close()
	}()

	wg.Wait()
	Metrics.RecordBackendRequest(target.ID, time.Since(start), false)
}

func (l *LoadBalancerInstance) startUDP(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", l.Config.ListenIP, l.Config.ListenPort)
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}

	Logger.InfoLB(l.Config.Name, fmt.Sprintf("UDP LoadBalancer %s listening on %s", l.Config.Name, addr))

	go func() {
		cleanupTicker := time.NewTicker(10 * time.Second)
		defer cleanupTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				l.udpSessions.Range(func(key, value interface{}) bool {
					sess := value.(*udpSession)
					sess.conn.Close()
					return true
				})
				conn.Close()
				return
			case <-cleanupTicker.C:
				now := time.Now()
				l.udpSessions.Range(func(key, value interface{}) bool {
					sess := value.(*udpSession)
					if now.Sub(sess.lastSeen) > 30*time.Second {
						sess.conn.Close()
						l.udpSessions.Delete(key)
					}
					return true
				})
			}
		}
	}()

	buf := make([]byte, 65507)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // Graceful shutdown
			default:
				Logger.ErrorLB(l.Config.Name, fmt.Sprintf("ReadFromUDP error on %s: %v", l.Config.Name, err))
				continue
			}
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		
		go l.handleUDPPacket(data, clientAddr, conn)
	}
}

func (l *LoadBalancerInstance) handleUDPPacket(data []byte, clientAddr *net.UDPAddr, clientConn *net.UDPConn) {
	clientKey := clientAddr.String()

	// Check for existing session
	if val, ok := l.udpSessions.Load(clientKey); ok {
		sess := val.(*udpSession)
		sess.lastSeen = time.Now()
		_, err := sess.conn.Write(data)
		if err != nil {
			sess.conn.Close()
			l.udpSessions.Delete(clientKey)
		} else {
			return
		}
	}

	// Create new session
	backends := l.backends.Load().([]*models.BackendServer)
	target := l.strategy.Next(backends, clientAddr.IP.String())
	if target == nil {
		return
	}

	targetAddr := fmt.Sprintf("%s:%d", target.Address, target.Port)
	start := time.Now()
	backendConn, err := net.Dial("udp", targetAddr)
	if err != nil {
		Metrics.RecordBackendRequest(target.ID, time.Since(start), true)
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
		return
	}

	sess := &udpSession{
		conn:     backendConn,
		lastSeen: time.Now(),
	}
	l.udpSessions.Store(clientKey, sess)

	// Write the initial packet
	_, err = backendConn.Write(data)
	if err != nil {
		Metrics.RecordBackendRequest(target.ID, time.Since(start), true)
		backendConn.Close()
		l.udpSessions.Delete(clientKey)
		return
	}
	
	Metrics.RecordBackendRequest(target.ID, time.Since(start), false)

	// Goroutine to read from backend and write back to client
	go func() {
		buf := make([]byte, 65507)
		for {
			n, err := backendConn.Read(buf)
			if err != nil {
				backendConn.Close()
				l.udpSessions.Delete(clientKey)
				return
			}
			sess.lastSeen = time.Now()
			_, err = clientConn.WriteToUDP(buf[:n], clientAddr)
			if err != nil {
				backendConn.Close()
				l.udpSessions.Delete(clientKey)
				return
			}
		}
	}()
}

func parsePort(s string) int {
	var p int
	fmt.Sscanf(s, "%d", &p)
	return p
}

func (l *LoadBalancerInstance) startHTTP(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", l.Config.ListenIP, l.Config.ListenPort)
	
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			backends := l.backends.Load().([]*models.BackendServer)
			clientIP, _, err := net.SplitHostPort(req.RemoteAddr)
			if err != nil {
				clientIP = req.RemoteAddr
			}
			target := l.strategy.Next(backends, clientIP)

			// Sticky Sessions Support
			if l.Config.StickySessionsEnabled {
				cookieName := "BALANCER_SESSION"
				if l.Config.StickySessionType != "" {
					cookieName = l.Config.StickySessionType
				}
				if cookie, err := req.Cookie(cookieName); err == nil && cookie.Value != "" {
					// Check if this backend is still active and healthy
					for _, b := range backends {
						if fmt.Sprintf("%d", b.ID) == cookie.Value {
							target = b // Override the strategy
							break
						}
					}
				}
			}

			if target == nil {
				return
			}
			
			// Store selected backend in request context for the Transport
			ctx := context.WithValue(req.Context(), "selected_backend", target)
			ctx = context.WithValue(ctx, ctxKeyRequestHost, req.Host) // Stash SNI
			*req = *req.WithContext(ctx)
			
			scheme := "http"
			if l.Config.Protocol == "https" || target.TLSEnabled { 
				scheme = "https"
			}
			
			req.URL.Scheme = scheme
			// req.URL.Host determines the http.Transport connection pool key.
			// It MUST be the backend's address so connections are pooled per-backend.
			req.URL.Host = fmt.Sprintf("%s:%d", target.Address, target.Port)
			// req.Host determines the Host header sent to the backend.
			// We leave req.Host untouched to preserve the original client requested domain.
			req.URL.Path = req.URL.Path

			if err == nil {
				req.Header.Set("X-Real-IP", clientIP)
				if req.Header.Get("X-Forwarded-For") == "" {
					req.Header.Set("X-Forwarded-For", clientIP)
				}
			}
		},
		Transport: func() http.RoundTripper {
			l.transport = &backendTransport{
				lbID:     l.Config.ID,
				strategy: l.strategy,
				proxyProtocolEnabled: l.Config.ProxyProtocolEnabled,
				proxyProtocolVersion: l.Config.ProxyProtocolVersion,
				http2Enabled:         l.Config.Protocol == "https" || l.Config.BackendHTTP2Enabled,
			}
			return l.transport
		}(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Proxy error to %s: %v", r.URL.String(), err))
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("502 Bad Gateway - Backend node is unreachable or dropped the connection"))
		},
		ModifyResponse: func(resp *http.Response) error {
			if l.Config.StickySessionsEnabled {
				if target, ok := resp.Request.Context().Value("selected_backend").(*models.BackendServer); ok {
					cookieName := "BALANCER_SESSION"
					if l.Config.StickySessionType != "" {
						cookieName = l.Config.StickySessionType
					}
					cookieVal := fmt.Sprintf("%d", target.ID)
					reqCookie, err := resp.Request.Cookie(cookieName)
					// Set cookie if it wasn't present or if we routed to a different node
					if err != nil || reqCookie.Value != cookieVal {
						c := &http.Cookie{
							Name:     cookieName,
							Value:    cookieVal,
							Path:     "/",
							HttpOnly: true,
							MaxAge:   86400, // 24 hours
						}
						if v := c.String(); v != "" {
							resp.Header.Add("Set-Cookie", v)
						}
					}
				}
			}
			return nil
		},
	}

	handler := http.Handler(proxy)

	// TLS / ACME Setup
	var tlsConfig *tls.Config
	if l.Config.SSLEnabled {
		if l.Config.ACMEEnabled || l.Config.AutoSSL {
			certmagic.DefaultACME.Agreed = true
			certmagic.DefaultACME.Email = l.Config.ACMEEmail
			if certmagic.DefaultACME.Email == "" {
				certmagic.DefaultACME.Email = "admin@balancer.local"
			}
			
			// Store certificates in the persistent volume mapped in docker-compose
			certmagic.Default.Storage = &certmagic.FileStorage{Path: "./certs/certmagic"}
			
			cfg := certmagic.NewDefault()
			if l.Config.ACMEDomains != "" {
				rawDomains := strings.Split(l.Config.ACMEDomains, ",")
				var cleanDomains []string
				for _, d := range rawDomains {
					d = strings.TrimSpace(d)
					if d != "" {
						cleanDomains = append(cleanDomains, d)
					}
				}
				if len(cleanDomains) > 0 {
					l.acmeStatus.Store("issuing")
					go func() {
						err := cfg.ManageSync(context.Background(), cleanDomains)
						if err != nil {
							l.acmeStatus.Store("error")
							l.acmeError.Store(err.Error())
							Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to manage ACME domains for %s: %v", l.Config.Name, err))
						} else {
							l.acmeStatus.Store("ok")
							l.acmeError.Store("")
							Logger.Info(fmt.Sprintf("ACME certificate successfully issued for domains: %v", cleanDomains))
						}
					}()
				}
			}
			tlsConfig = cfg.TLSConfig()
		} else if l.Config.CertData != "" && l.Config.KeyData != "" {
			cert, err := tls.X509KeyPair([]byte(l.Config.CertData), []byte(l.Config.KeyData))
			if err == nil {
				tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			} else {
				Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to load TLS cert from data for %s: %v", l.Config.Name, err))
			}
		} else if l.Config.CertPath != "" && l.Config.KeyPath != "" {
			cert, err := tls.LoadX509KeyPair(l.Config.CertPath, l.Config.KeyPath)
			if err == nil {
				tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			} else {
				Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to load TLS cert from path for %s: %v", l.Config.Name, err))
			}
		} else {
			// Local dev fallback
			tlsConfig, _ = generateSelfSignedCert()
		}
	}

	errChan := make(chan error, 1)

	if l.Config.SSLEnabled && tlsConfig != nil {
		l.httpsServer = &http.Server{
			Addr:      addr,
			Handler:   handler,
			TLSConfig: tlsConfig,
		}

		go func() {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTPS LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- l.httpsServer.ListenAndServeTLS("", "")
		}()
		
		if l.Config.HTTP3Enabled {
			l.http3Server = &http3.Server{
				Addr:      addr,
				Handler:   handler,
				TLSConfig: tlsConfig,
			}
			go func() {
				Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTP/3 (QUIC) LoadBalancer %s listening on %s", l.Config.Name, addr))
				errChan <- l.http3Server.ListenAndServe()
			}()
		}
	} else {
		l.httpServer = &http.Server{
			Addr:    addr,
			Handler: handler,
		}
		go func() {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTP LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- l.httpServer.ListenAndServe()
		}()
	}

	go func() {
		<-ctx.Done()
		if l.httpServer != nil {
			l.httpServer.Shutdown(context.Background())
		}
		if l.httpsServer != nil {
			l.httpsServer.Shutdown(context.Background())
		}
		if l.http3Server != nil {
			l.http3Server.Close()
		}
	}()

	return <-errChan
}

func (l *LoadBalancerInstance) Stop() {
	if l.cancel != nil {
		l.cancel()
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

type backendTransport struct {
	lbID     uint
	strategy Strategy

	proxyProtocolEnabled bool
	proxyProtocolVersion int
	http2Enabled         bool

	// Persistent transport — created once, reused for all requests.
	// This is critical for HTTP/2: Go's h2 layer needs a stable transport
	// to maintain connection pools and multiplexed streams.
	inner *http.Transport
	once  sync.Once
}

// getTransport returns the cached *http.Transport, creating it on first call.
func (t *backendTransport) getTransport() *http.Transport {
	t.once.Do(func() {
		tr := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}

		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
		}

		tr.ForceAttemptHTTP2 = false

		baseDial := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Override addr with the actual backend IP, ignoring req.URL.Host (which is used for SNI)
			target, ok := ctx.Value("selected_backend").(*models.BackendServer)
			if ok && target != nil {
				addr = fmt.Sprintf("%s:%d", target.Address, target.Port)
			}
			conn, err := baseDial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			
			if t.proxyProtocolEnabled {
				if err != nil {
					return nil, err
				}

				// Extract client info from the request stashed in context
				remoteAddr, _ := ctx.Value(ctxKeyRemoteAddr).(string)
				clientIP, clientPortStr, _ := net.SplitHostPort(remoteAddr)
				destIP, destPortStr, _ := net.SplitHostPort(conn.LocalAddr().String())

				header := &proxyproto.Header{
					Version:           byte(t.proxyProtocolVersion),
					Command:           proxyproto.PROXY,
					TransportProtocol: proxyproto.TCPv4,
					SourceAddr: &net.TCPAddr{
						IP:   net.ParseIP(clientIP),
						Port: parsePort(clientPortStr),
					},
					DestinationAddr: &net.TCPAddr{
						IP:   net.ParseIP(destIP),
						Port: parsePort(destPortStr),
					},
				}

				if header.SourceAddr.(*net.TCPAddr).IP.To4() == nil {
					header.TransportProtocol = proxyproto.TCPv6
				}

				_, err = header.WriteTo(conn)
				if err != nil {
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
			
			sni := ""
			if reqHost, ok := ctx.Value(ctxKeyRequestHost).(string); ok {
				sni = reqHost
			}
			
			tlsConfig := tr.TLSClientConfig.Clone()
			if sni != "" {
				// Trim port if present for SNI
				host, _, err := net.SplitHostPort(sni)
				if err != nil {
					host = sni
				}
				tlsConfig.ServerName = host
			}
			
			tlsConn := tls.Client(baseConn, tlsConfig)
			err = tlsConn.HandshakeContext(ctx)
			if err != nil {
				baseConn.Close()
				return nil, err
			}
			return tlsConn, nil
		}

		t.inner = tr
	})
	return t.inner
}

// context key type to avoid collisions
type ctxKey string

const ctxKeyRemoteAddr ctxKey = "remote_addr"
const ctxKeyRequestHost ctxKey = "request_host"

func (t *backendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	tr := t.getTransport()

	// Apply SNI override from backend config
	if target, ok := req.Context().Value("selected_backend").(*models.BackendServer); ok && target.SNI != "" {
		req.Host = target.SNI
		// We need per-request SNI. Clone TLSClientConfig only when SNI differs.
		// For HTTP/2 the ServerName in the persistent config doesn't matter
		// because Go's http2 transport uses req.Host for the :authority pseudo-header,
		// and the TLS layer picks up ServerName from the first connection.
		// But to be safe with varying backends, we override via the request Host.
	}

	// Stash RemoteAddr in context so the proxy-protocol DialContext can read it
	if t.proxyProtocolEnabled {
		ctx := context.WithValue(req.Context(), ctxKeyRemoteAddr, req.RemoteAddr)
		req = req.WithContext(ctx)
	}

	resp, err := tr.RoundTrip(req)
	latency := time.Since(start)

	isError := err != nil || (resp != nil && resp.StatusCode >= 500)
	Metrics.RecordRequest(t.lbID, latency, isError)

	if target, ok := req.Context().Value("selected_backend").(*models.BackendServer); ok {
		Metrics.RecordBackendRequest(target.ID, latency, isError)
		if lc, ok := t.strategy.(*LeastConnections); ok {
			lc.CompleteConnection(target)
		}
	}

	return resp, err
}

func (l *LoadBalancerInstance) GetACMEStatus() string {
	if val := l.acmeStatus.Load(); val != nil {
		return val.(string)
	}
	return ""
}

func (l *LoadBalancerInstance) GetACMEError() string {
	if val := l.acmeError.Load(); val != nil {
		return val.(string)
	}
	return ""
}
