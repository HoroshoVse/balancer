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
	"sync/atomic"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/caddyserver/certmagic"
	"github.com/quic-go/quic-go/http3"
	"github.com/pires/go-proxyproto"
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
	
	backends atomic.Value // Store slice of *models.BackendServer
	strategy Strategy
	
	cancel   context.CancelFunc
	
	acmeStatus atomic.Value // Store string
	acmeError  atomic.Value // Store string
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
		Logger.Warn(fmt.Sprintf("[FAILOVER] %s: all primary backends DOWN, switching to backup nodes", l.Config.Name))
		activeBackends = backupBackends
	}
	if len(activeBackends) == 0 {
		Logger.Error(fmt.Sprintf("[CRITICAL] %s: ALL backends (primary + backup) are DOWN", l.Config.Name))
		l.backends.Store([]*models.BackendServer{})
	} else {
		l.backends.Store(activeBackends)
	}
}

func (l *LoadBalancerInstance) startTCP(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", l.Config.ListenIP, l.Config.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l.tcpListener = listener

	Logger.Info(fmt.Sprintf("TCP LoadBalancer %s listening on %s", l.Config.Name, addr))

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
				Logger.Error(fmt.Sprintf("Accept error on %s: %v", l.Config.Name, err))
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
	backendConn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		Logger.Error(fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
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

	go func() {
		io.Copy(backendConn, clientConn)
		backendConn.Close()
	}()
	go func() {
		io.Copy(clientConn, backendConn)
		clientConn.Close()
	}()
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

	Logger.Info(fmt.Sprintf("UDP LoadBalancer %s listening on %s", l.Config.Name, addr))

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 65507)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // Graceful shutdown
			default:
				Logger.Error(fmt.Sprintf("ReadFromUDP error on %s: %v", l.Config.Name, err))
				continue
			}
		}

		data := make([]byte, n)
		copy(data, buf[:n])
		
		go l.handleUDPPacket(data, clientAddr)
	}
}

func (l *LoadBalancerInstance) handleUDPPacket(data []byte, clientAddr *net.UDPAddr) {
	backends := l.backends.Load().([]*models.BackendServer)
	target := l.strategy.Next(backends, clientAddr.IP.String())
	if target == nil {
		return
	}

	targetAddr := fmt.Sprintf("%s:%d", target.Address, target.Port)
	backendConn, err := net.Dial("udp", targetAddr)
	if err != nil {
		Logger.Error(fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
		return
	}
	defer backendConn.Close()

	backendConn.Write(data)
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
			if target == nil {
				return
			}
			
			// Store selected backend in request context for the Transport
			ctx := context.WithValue(req.Context(), "selected_backend", target)
			*req = *req.WithContext(ctx)
			
			scheme := "http"
			if target.TLSEnabled { 
				scheme = "https"
			}
			
			req.URL.Scheme = scheme
			req.URL.Host = fmt.Sprintf("%s:%d", target.Address, target.Port)
			req.URL.Path = req.URL.Path
			// Preserve the original req.Host so the backend knows what domain was requested

			if err == nil {
				req.Header.Set("X-Real-IP", clientIP)
				if req.Header.Get("X-Forwarded-For") == "" {
					req.Header.Set("X-Forwarded-For", clientIP)
				}
			}
		},
		Transport: &backendTransport{
			lbID:     l.Config.ID,
			strategy: l.strategy,
			proxyProtocolEnabled: l.Config.ProxyProtocolEnabled,
			proxyProtocolVersion: l.Config.ProxyProtocolVersion,
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
							Logger.Error(fmt.Sprintf("Failed to manage ACME domains for %s: %v", l.Config.Name, err))
						} else {
							l.acmeStatus.Store("ok")
							l.acmeError.Store("")
							Logger.Info(fmt.Sprintf("ACME certificate successfully issued for domains: %v", cleanDomains))
						}
					}()
				}
			}
			tlsConfig = cfg.TLSConfig()
		} else if l.Config.CertPath != "" && l.Config.KeyPath != "" {
			cert, err := tls.LoadX509KeyPair(l.Config.CertPath, l.Config.KeyPath)
			if err == nil {
				tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
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
			Logger.Info(fmt.Sprintf("HTTPS LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- l.httpsServer.ListenAndServeTLS("", "")
		}()
		
		if l.Config.HTTP3Enabled {
			l.http3Server = &http3.Server{
				Addr:      addr,
				Handler:   handler,
				TLSConfig: tlsConfig,
			}
			go func() {
				Logger.Info(fmt.Sprintf("HTTP/3 (QUIC) LoadBalancer %s listening on %s", l.Config.Name, addr))
				errChan <- l.http3Server.ListenAndServe()
			}()
		}
	} else {
		l.httpServer = &http.Server{
			Addr:    addr,
			Handler: handler,
		}
		go func() {
			Logger.Info(fmt.Sprintf("HTTP LoadBalancer %s listening on %s", l.Config.Name, addr))
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
}

func (t *backendTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	
	// Create a custom transport for this request if proxy protocol is enabled
	// Normally we would cache this transport, but for simplicity we do it here
	tr := http.DefaultTransport.(*http.Transport).Clone()
	
	hostWithoutPort, _, _ := net.SplitHostPort(req.Host)
	if hostWithoutPort == "" {
		hostWithoutPort = req.Host
	}
	
	tr.TLSClientConfig = &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         hostWithoutPort, // pass original SNI to the backend
	}
	if t.proxyProtocolEnabled {
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext(ctx, network, addr)
			
			if err != nil {
				return nil, err
			}
			
			// Try to extract client IP/Port from the request
			clientIP, clientPortStr, _ := net.SplitHostPort(req.RemoteAddr)
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
			
			return conn, nil
		}
	}
	
	resp, err := tr.RoundTrip(req)
	latency := time.Since(start)
	
	isError := err != nil || (resp != nil && resp.StatusCode >= 500)
	Metrics.RecordRequest(t.lbID, latency, isError)
	
	if target, ok := req.Context().Value("selected_backend").(*models.BackendServer); ok {
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
