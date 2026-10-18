package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/balancer/backend/internal/models"
	"github.com/caddyserver/certmagic"
	"github.com/quic-go/quic-go/http3"
)

const (
	statusStarting = "starting"
	statusRunning  = "running"
	statusError    = "error"
	statusStopped  = "stopped"

	stickyCookieName      = "BALANCER_SESSION"
	udpSessionIdleTimeout = 60 * time.Second
	stopTimeout           = 5 * time.Second

	defaultConnectTimeout = 5 * time.Second
	defaultHTTPIdle       = 120 * time.Second
)

// httpRedirectPort is where the HTTP->HTTPS redirect listens (var for tests).
var httpRedirectPort = models.HTTPRedirectPort

// msOr converts a millisecond setting, using def when it is unset.
func msOr(ms int, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}

// context key type to avoid collisions
type ctxKey string

const (
	ctxKeyBackend    ctxKey = "backend"
	ctxKeyClientAddr ctxKey = "client_addr"
)

type instanceState struct {
	status string
	err    string
}

type LoadBalancerInstance struct {
	Config        models.LoadBalancer
	healthChecker *HealthChecker

	// ctx is cancelled by Stop; done is closed once Start has returned
	// and every listening socket is closed.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	state  atomic.Value // instanceState

	http3Server *http3.Server
	transport   *backendTransport

	backends     atomic.Value // []*models.BackendServer
	backendNames atomic.Value // string, last logged set of active backends
	strategy     Strategy
	conns        connCounter

	acmeStatus    atomic.Value // Store string
	acmeError     atomic.Value // Store string
	failoverState int32        // 0: init, 1: primary, 2: backup, 3: all down

	udpSessions sync.Map                // Map clientAddr(string) -> *udpSession
	dns         atomic.Pointer[dnsProxy] // set while serving in DNS mode
	acmeIssuer  atomic.Pointer[certmagic.ACMEIssuer]
}

type udpSession struct {
	conn      net.Conn
	backend   *models.BackendServer
	lastSeen  int64 // atomic unix timestamp
	closeOnce sync.Once
}

func NewLoadBalancerInstance(config models.LoadBalancer, hc *HealthChecker) *LoadBalancerInstance {
	ctx, cancel := context.WithCancel(context.Background())
	inst := &LoadBalancerInstance{
		Config:        config,
		healthChecker: hc,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}
	inst.state.Store(instanceState{status: statusStarting})
	inst.acmeStatus.Store("")
	inst.acmeError.Store("")
	inst.backends.Store([]*models.BackendServer{})
	inst.backendNames.Store("")
	inst.strategy = newStrategy(config.Algorithm, &inst.conns)
	inst.transport = &backendTransport{
		lbID:                 config.ID,
		proxyProtocolEnabled: config.ProxyProtocolEnabled,
		proxyProtocolVersion: config.ProxyProtocolVersion,
		http2Enabled:         config.BackendHTTP2Enabled,
		connectTimeout:       msOr(config.ConnectTimeout, defaultConnectTimeout),
		responseTimeout:      msOr(config.ResponseTimeout, 0),
	}
	return inst
}

// Start serves until Stop is called. It returns an error if the listener
// could not be started or failed; the instance is then in the error state.
func (l *LoadBalancerInstance) Start() error {
	defer close(l.done)
	if l.ctx.Err() != nil {
		l.setState(statusStopped, "")
		return nil
	}

	l.updateBackends()

	var err error
	switch {
	case l.Config.Protocol == "tcp":
		err = l.startTCP()
	case l.Config.Protocol == "udp" && l.Config.DNSMode:
		err = l.startDNS()
	case l.Config.Protocol == "udp":
		err = l.startUDP()
	case l.Config.Protocol == "https" && !l.Config.SSLEnabled:
		// TLS Passthrough mode: user selected HTTPS but disabled SSL termination on the LB
		Logger.InfoLB(l.Config.Name, fmt.Sprintf("Running %s in TLS Passthrough (TCP) mode", l.Config.Name))
		err = l.startTCP()
	default:
		err = l.startHTTP()
	}

	if err != nil && l.ctx.Err() == nil {
		l.cancel() // tear down whatever part did start
		l.setState(statusError, err.Error())
		return err
	}
	l.setState(statusStopped, "")
	return nil
}

// Stop shuts the instance down and waits until its ports are released.
func (l *LoadBalancerInstance) Stop() {
	l.cancel()
	select {
	case <-l.done:
	case <-time.After(stopTimeout):
		Logger.WarnLB(l.Config.Name, fmt.Sprintf("Timed out waiting for %s to stop", l.Config.Name))
	}
}

func (l *LoadBalancerInstance) setState(status, errStr string) {
	l.state.Store(instanceState{status: status, err: errStr})
}

// Status returns the runtime state and the last start error, if any.
func (l *LoadBalancerInstance) Status() (string, string) {
	s := l.state.Load().(instanceState)
	return s.status, s.err
}

func (l *LoadBalancerInstance) listenAddr() string {
	return backendAddr(l.Config.ListenIP, l.Config.ListenPort)
}

func (l *LoadBalancerInstance) stickyIP() bool {
	return l.Config.StickySessionsEnabled && l.Config.StickySessionType == "ip"
}

func (l *LoadBalancerInstance) stickyCookie() bool {
	return l.Config.StickySessionsEnabled && l.Config.StickySessionType == "cookie"
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
	newState := int32(0)
	if len(primaryBackends) > 0 {
		activeBackends = primaryBackends
		newState = 1 // primary
	} else if len(backupBackends) > 0 {
		activeBackends = backupBackends
		newState = 2 // backup
	} else {
		newState = 3 // all down
	}

	// Log and alert only on transitions
	oldState := atomic.SwapInt32(&l.failoverState, newState)
	if oldState != newState {
		name := html.EscapeString(l.Config.Name)
		switch newState {
		case 1:
			if oldState != 0 {
				Logger.InfoLB(l.Config.Name, fmt.Sprintf("[FAILOVER] %s: primary backends recovered", l.Config.Name))
				l.alert(fmt.Sprintf("✅ <b>%s</b>: основные узлы вернулись, резерв снова ждёт.", name))
			}
		case 2:
			Logger.WarnLB(l.Config.Name, fmt.Sprintf("[FAILOVER] %s: all primary backends DOWN, switching to backup nodes", l.Config.Name))
			if oldState == 1 {
				l.alert(fmt.Sprintf("⚠️ <b>%s</b>: основные узлы не отвечают — трафик переключён на резерв.", name))
			}
		case 3:
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("[CRITICAL] %s: ALL backends (primary + backup) are DOWN", l.Config.Name))
			if oldState != 0 {
				l.alert(fmt.Sprintf("❌ <b>%s</b>: недоступны все узлы, включая резерв!", name))
			}
		}
	}

	names := make([]string, len(activeBackends))
	active := make(map[uint]bool, len(activeBackends))
	for i, b := range activeBackends {
		names[i] = fmt.Sprintf("%s(%s)", b.Name, backendAddr(b.Address, b.Port))
		active[b.ID] = true
	}
	if joined := strings.Join(names, ", "); joined != l.backendNames.Swap(joined) && len(activeBackends) > 0 {
		Logger.InfoLB(l.Config.Name, fmt.Sprintf("[BACKENDS] %s: active backends updated to: [%s]", l.Config.Name, joined))
	}

	if activeBackends == nil {
		activeBackends = []*models.BackendServer{}
	}
	l.backends.Store(activeBackends)

	// Close idle HTTP connections so requests are forced to reconnect
	// to the new set of active backends (critical for failover/failback)
	l.transport.closeIdleConnections()

	// Move UDP flows off backends that are no longer active
	l.udpSessions.Range(func(key, value interface{}) bool {
		if sess := value.(*udpSession); !active[sess.backend.ID] {
			l.closeUDPSession(key.(string), sess)
		}
		return true
	})
}

func (l *LoadBalancerInstance) alert(msg string) {
	if l.healthChecker != nil {
		l.healthChecker.SendTelegramAlert(msg)
	}
}

// pickBackend chooses a backend for a new connection and reserves a
// connection slot on it. Callers must call releaseBackend when done.
// preferredID (e.g. from a sticky cookie) wins if that backend is available.
func (l *LoadBalancerInstance) pickBackend(clientIP string, preferredID uint) *models.BackendServer {
	backends := l.backends.Load().([]*models.BackendServer)
	candidates := make([]*models.BackendServer, 0, len(backends))
	for _, b := range backends {
		if b.MaxConns <= 0 || l.conns.Active(b.ID) < int64(b.MaxConns) {
			candidates = append(candidates, b)
		}
	}

	for len(candidates) > 0 {
		var target *models.BackendServer
		if preferredID != 0 {
			for _, b := range candidates {
				if b.ID == preferredID {
					target = b
					break
				}
			}
			preferredID = 0
		}
		if target == nil {
			if l.stickyIP() {
				target = candidates[hashIP(clientIP)%uint32(len(candidates))]
			} else {
				target = l.strategy.Next(candidates, clientIP)
			}
		}
		if target == nil {
			return nil
		}
		if l.conns.tryAcquire(target.ID, target.MaxConns) {
			return target
		}
		// Hit max_conns concurrently; try the remaining nodes
		remaining := candidates[:0]
		for _, b := range candidates {
			if b.ID != target.ID {
				remaining = append(remaining, b)
			}
		}
		candidates = remaining
	}
	return nil
}

func (l *LoadBalancerInstance) releaseBackend(b *models.BackendServer) {
	l.conns.release(b.ID)
}

// nextBackoff mirrors net/http's accept retry delay.
func nextBackoff(d time.Duration) time.Duration {
	if d == 0 {
		return 5 * time.Millisecond
	}
	if d *= 2; d > time.Second {
		return time.Second
	}
	return d
}

func (l *LoadBalancerInstance) startTCP() error {
	addr := l.listenAddr()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	l.setState(statusRunning, "")
	Logger.InfoLB(l.Config.Name, fmt.Sprintf("TCP LoadBalancer %s listening on %s", l.Config.Name, addr))
	return l.serveTCP(listener)
}

// serveTCP accepts connections until the instance stops.
func (l *LoadBalancerInstance) serveTCP(listener net.Listener) error {
	go func() {
		<-l.ctx.Done()
		listener.Close()
	}()

	var delay time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			if l.ctx.Err() != nil {
				return nil // Graceful shutdown
			}
			delay = nextBackoff(delay)
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Accept error on %s: %v; retrying in %v", l.Config.Name, err, delay))
			select {
			case <-time.After(delay):
			case <-l.ctx.Done():
				return nil
			}
			continue
		}
		delay = 0
		go l.handleTCPConnection(conn)
	}
}

func (l *LoadBalancerInstance) handleTCPConnection(clientConn net.Conn) {
	defer clientConn.Close()

	clientIP, _, _ := net.SplitHostPort(clientConn.RemoteAddr().String())
	target := l.pickBackend(clientIP, 0)
	if target == nil {
		Metrics.RecordRequest(l.Config.ID, 0, true)
		return
	}
	defer l.releaseBackend(target)

	targetAddr := backendAddr(target.Address, target.Port)
	start := time.Now()
	backendConn, err := net.DialTimeout("tcp", targetAddr, msOr(l.Config.ConnectTimeout, defaultConnectTimeout))
	dialLatency := time.Since(start)
	Metrics.RecordBackendRequest(target.ID, dialLatency, err != nil)
	Metrics.RecordRequest(l.Config.ID, dialLatency, err != nil)
	if err != nil {
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
		return
	}
	defer backendConn.Close()

	// The PROXY header goes first on the wire, before any TLS handshake
	if l.Config.ProxyProtocolEnabled {
		header := buildProxyHeader(l.Config.ProxyProtocolVersion, clientConn.RemoteAddr(), clientConn.LocalAddr())
		if _, err := header.WriteTo(backendConn); err != nil {
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to write PROXY protocol header: %v", err))
			return
		}
	}

	// If TLSEnabled is true for this backend, wrap the connection in TLS
	if target.TLSEnabled {
		backendConn = newBackendTLSClient(backendConn, target.SNI, false)
	}

	proxyStreams(clientConn, backendConn, msOr(l.Config.IdleTimeout, 0))
}

// proxyStreams copies both ways until either side closes or, when idle > 0,
// no data has moved in either direction for that long.
func proxyStreams(a, b net.Conn, idle time.Duration) {
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	copyDir := func(dst, src net.Conn) {
		defer dst.Close() // unblocks the opposite direction
		if idle <= 0 {
			io.Copy(dst, src) // keeps the kernel splice fast path
			return
		}
		buf := make([]byte, 32*1024)
		for {
			src.SetReadDeadline(time.Now().Add(idle))
			n, err := src.Read(buf)
			if n > 0 {
				lastActivity.Store(time.Now().UnixNano())
				dst.SetWriteDeadline(time.Now().Add(idle))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, lastActivity.Load())) < idle {
					continue // quiet here, but the other direction is active
				}
				return
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyDir(b, a) }()
	go func() { defer wg.Done(); copyDir(a, b) }()
	wg.Wait()
}

func (l *LoadBalancerInstance) startUDP() error {
	addr := l.listenAddr()
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	conn.SetReadBuffer(4 << 20) // absorb bursts instead of dropping datagrams
	l.setState(statusRunning, "")
	Logger.InfoLB(l.Config.Name, fmt.Sprintf("UDP LoadBalancer %s listening on %s", l.Config.Name, addr))

	idle := msOr(l.Config.IdleTimeout, udpSessionIdleTimeout)
	sweep := min(max(idle/2, time.Second), 15*time.Second)
	go func() {
		cleanupTicker := time.NewTicker(sweep)
		defer cleanupTicker.Stop()
		for {
			select {
			case <-l.ctx.Done():
				conn.Close()
				l.udpSessions.Range(func(key, value interface{}) bool {
					l.closeUDPSession(key.(string), value.(*udpSession))
					return true
				})
				return
			case <-cleanupTicker.C:
				cutoff := time.Now().Add(-idle).Unix()
				l.udpSessions.Range(func(key, value interface{}) bool {
					sess := value.(*udpSession)
					if atomic.LoadInt64(&sess.lastSeen) < cutoff {
						l.closeUDPSession(key.(string), sess)
					}
					return true
				})
			}
		}
	}()

	// Datagrams are handled inline: it keeps per-client ordering and
	// avoids racing session creation for the same client.
	buf := make([]byte, 65535)
	var delay time.Duration
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			if l.ctx.Err() != nil {
				return nil // Graceful shutdown
			}
			delay = nextBackoff(delay)
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("ReadFromUDP error on %s: %v", l.Config.Name, err))
			time.Sleep(delay)
			continue
		}
		delay = 0
		l.handleUDPPacket(buf[:n], clientAddr, conn)
	}
}

func (l *LoadBalancerInstance) handleUDPPacket(data []byte, clientAddr *net.UDPAddr, clientConn *net.UDPConn) {
	clientKey := clientAddr.String()

	payload := data
	if l.Config.ProxyProtocolEnabled {
		header := buildProxyHeader(l.Config.ProxyProtocolVersion, clientAddr, clientConn.LocalAddr())
		headerBytes, err := header.Format()
		if err == nil {
			payload = append(headerBytes, data...)
		} else {
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to format PROXY protocol header: %v", err))
		}
	}

	// Existing session
	if val, ok := l.udpSessions.Load(clientKey); ok {
		sess := val.(*udpSession)
		atomic.StoreInt64(&sess.lastSeen, time.Now().Unix())
		if _, err := sess.conn.Write(payload); err != nil {
			l.closeUDPSession(clientKey, sess)
		}
		return
	}

	// Create new session
	target := l.pickBackend(clientAddr.IP.String(), 0)
	if target == nil {
		Metrics.RecordRequest(l.Config.ID, 0, true)
		return
	}

	targetAddr := backendAddr(target.Address, target.Port)
	start := time.Now()
	backendConn, err := net.Dial("udp", targetAddr)
	if err != nil {
		l.releaseBackend(target)
		Metrics.RecordBackendRequest(target.ID, time.Since(start), true)
		Metrics.RecordRequest(l.Config.ID, time.Since(start), true)
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to connect to backend %s: %v", targetAddr, err))
		return
	}

	sess := &udpSession{
		conn:     backendConn,
		backend:  target,
		lastSeen: time.Now().Unix(),
	}
	l.udpSessions.Store(clientKey, sess)

	_, err = backendConn.Write(payload)
	Metrics.RecordBackendRequest(target.ID, time.Since(start), err != nil)
	Metrics.RecordRequest(l.Config.ID, time.Since(start), err != nil)
	if err != nil {
		l.closeUDPSession(clientKey, sess)
		return
	}

	go l.relayUDPReplies(clientKey, sess, clientConn, clientAddr)
}

// relayUDPReplies forwards backend responses to the client until the session closes.
func (l *LoadBalancerInstance) relayUDPReplies(clientKey string, sess *udpSession, clientConn *net.UDPConn, clientAddr *net.UDPAddr) {
	defer l.closeUDPSession(clientKey, sess)
	buf := make([]byte, 65535)
	for {
		n, err := sess.conn.Read(buf)
		if err != nil {
			return
		}
		atomic.StoreInt64(&sess.lastSeen, time.Now().Unix())
		if _, err := clientConn.WriteToUDP(buf[:n], clientAddr); err != nil {
			return
		}
	}
}

func (l *LoadBalancerInstance) closeUDPSession(clientKey string, sess *udpSession) {
	sess.closeOnce.Do(func() {
		sess.conn.Close()
		l.releaseBackend(sess.backend)
	})
	l.udpSessions.CompareAndDelete(clientKey, sess)
}

func (l *LoadBalancerInstance) startHTTP() error {
	addr := l.listenAddr()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			target := req.Context().Value(ctxKeyBackend).(*models.BackendServer)

			req.URL.Scheme = "http"
			if target.TLSEnabled {
				req.URL.Scheme = "https"
			}
			// req.URL.Host determines the http.Transport connection pool key.
			// It MUST be the backend's address so connections are pooled per-backend.
			req.URL.Host = backendAddr(target.Address, target.Port)
			// req.Host is the Host header sent to the backend: the client's
			// requested domain unless the node overrides it.
			if target.SNI != "" {
				req.Host = target.SNI
			}

			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				req.Header.Set("X-Real-IP", clientIP)
			}
			if req.TLS != nil {
				req.Header.Set("X-Forwarded-Proto", "https")
			} else {
				req.Header.Set("X-Forwarded-Proto", "http")
			}
			// X-Forwarded-For is appended by ReverseProxy itself.
		},
		Transport: l.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return // client went away
			}
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Proxy error to %s: %v", r.URL.String(), err))
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				http.Error(w, "504 Gateway Timeout - Backend node did not respond in time", http.StatusGatewayTimeout)
				return
			}
			http.Error(w, "502 Bad Gateway - Backend node is unreachable or dropped the connection", http.StatusBadGateway)
		},
		ModifyResponse: func(resp *http.Response) error {
			if !l.stickyCookie() {
				return nil
			}
			target, ok := resp.Request.Context().Value(ctxKeyBackend).(*models.BackendServer)
			if !ok {
				return nil
			}
			cookieVal := strconv.FormatUint(uint64(target.ID), 10)
			// Set cookie if it wasn't present or if we routed to a different node
			if c, err := resp.Request.Cookie(stickyCookieName); err == nil && c.Value == cookieVal {
				return nil
			}
			c := &http.Cookie{
				Name:     stickyCookieName,
				Value:    cookieVal,
				Path:     "/",
				HttpOnly: true,
				Secure:   l.Config.Protocol == "https",
				SameSite: http.SameSiteLaxMode,
				MaxAge:   86400, // 24 hours
			}
			resp.Header.Add("Set-Cookie", c.String())
			return nil
		},
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.http3Server != nil && r.ProtoMajor < 3 {
			l.http3Server.SetQuicHeaders(w.Header()) // advertise HTTP/3 via Alt-Svc
		}

		clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			clientIP = r.RemoteAddr
		}
		var preferredID uint
		if l.stickyCookie() {
			if c, err := r.Cookie(stickyCookieName); err == nil {
				if id, err := strconv.ParseUint(c.Value, 10, 64); err == nil {
					preferredID = uint(id)
				}
			}
		}

		target := l.pickBackend(clientIP, preferredID)
		if target == nil {
			Metrics.RecordRequest(l.Config.ID, 0, true)
			http.Error(w, "503 Service Unavailable - no healthy backend nodes available", http.StatusServiceUnavailable)
			return
		}
		defer l.releaseBackend(target)

		ctx := context.WithValue(r.Context(), ctxKeyBackend, target)
		ctx = context.WithValue(ctx, ctxKeyClientAddr, r.RemoteAddr)
		proxy.ServeHTTP(w, r.WithContext(ctx))
	})

	var tlsConfig *tls.Config
	if l.Config.SSLEnabled {
		var err error
		if tlsConfig, err = l.buildTLSConfig(); err != nil {
			return err
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       msOr(l.Config.IdleTimeout, defaultHTTPIdle),
	}
	if tlsConfig != nil {
		// Failed handshakes are summarised once a minute instead of one line each
		hlog := newHandshakeLog(l.Config.Name, l.acmeDomains())
		server.ErrorLog = log.New(hlog, "", 0)
		go hlog.run(l.ctx, time.Minute)
	}

	errChan := make(chan error, 3)

	var redirectLn net.Listener
	var redirectServer *http.Server
	if tlsConfig != nil && l.Config.HTTPRedirect {
		redirectAddr := backendAddr(l.Config.ListenIP, httpRedirectPort)
		if redirectLn, err = net.Listen("tcp", redirectAddr); err != nil {
			ln.Close()
			return fmt.Errorf("HTTP to HTTPS redirect: %w", err)
		}
		redirectServer = &http.Server{Handler: l.redirectHandler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("Redirecting HTTP on %s to HTTPS", redirectAddr))
			errChan <- redirectServer.Serve(redirectLn)
		}()
	}
	var udpConn net.PacketConn
	if tlsConfig != nil && l.Config.HTTP3Enabled {
		if udpConn, err = net.ListenPacket("udp", addr); err != nil {
			ln.Close()
			if redirectLn != nil {
				redirectLn.Close()
			}
			return err
		}
		l.http3Server = &http3.Server{
			Handler:   handler,
			TLSConfig: tlsConfig,
		}
		go func() {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTP/3 (QUIC) LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- l.http3Server.Serve(udpConn)
		}()
	}

	go func() {
		if tlsConfig != nil {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTPS LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- server.ServeTLS(ln, "", "")
		} else {
			Logger.InfoLB(l.Config.Name, fmt.Sprintf("HTTP LoadBalancer %s listening on %s", l.Config.Name, addr))
			errChan <- server.Serve(ln)
		}
	}()
	l.setState(statusRunning, "")

	var serveErr error
	select {
	case <-l.ctx.Done():
	case serveErr = <-errChan:
	}

	// Release the ports right away; let in-flight requests drain in the background.
	ln.Close()
	if redirectLn != nil {
		redirectLn.Close()
		redirectServer.Close()
	}
	if l.http3Server != nil {
		l.http3Server.Close()
		udpConn.Close()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		server.Close()
	}()

	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && !errors.Is(serveErr, net.ErrClosed) {
		return serveErr
	}
	return nil
}

// redirectHandler answers ACME HTTP-01 challenges and sends everything else
// to the same URL over HTTPS (308 keeps the method and body for non-GET).
func (l *LoadBalancerInstance) redirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if issuer := l.acmeIssuer.Load(); issuer != nil && issuer.HandleHTTPChallenge(w, r) {
			return
		}
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host == "" {
			http.Error(w, "Missing Host header", http.StatusBadRequest)
			return
		}
		if l.Config.ListenPort != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(l.Config.ListenPort))
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]" // IPv6 literal
		}
		code := http.StatusPermanentRedirect
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			code = http.StatusMovedPermanently
		}
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), code)
	})
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
