package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/balancer/backend/internal/models"
)

// DNS mode proxies DNS over UDP the way dnsdist does: each backend gets a
// few long-lived sockets, every query is sent with a fresh DNS ID, and the
// answer is matched back to the client by that ID. Unlike the generic UDP
// session mode this needs no socket per client port, so high query rates
// don't exhaust ephemeral ports.

const (
	dnsHeaderLen        = 12
	dnsSocketsPerNode   = 4
	dnsMaxPendingPerSock = 60000
)

// dnsQueryTimeout is how long an unanswered query is tracked (var for tests).
var dnsQueryTimeout = 5 * time.Second

type dnsPending struct {
	client  *net.UDPAddr
	origID  uint16
	backend *models.BackendServer
	sent    time.Time
}

// dnsUpstream is one UDP socket to a backend shared by many queries.
type dnsUpstream struct {
	conn    *net.UDPConn
	mu      sync.Mutex
	pending map[uint16]*dnsPending
}

type dnsPool struct {
	upstreams []*dnsUpstream
	next      uint32
}

type dnsProxy struct {
	inst     *LoadBalancerInstance
	listener *net.UDPConn
	timeout  time.Duration

	mu    sync.Mutex
	pools map[uint]*dnsPool // backend ID -> sockets
}

func newDNSProxy(inst *LoadBalancerInstance, listener *net.UDPConn) *dnsProxy {
	timeout := msOr(inst.Config.ResponseTimeout, dnsQueryTimeout)
	return &dnsProxy{inst: inst, listener: listener, timeout: timeout, pools: make(map[uint]*dnsPool)}
}

// register stores a pending query under a random unused DNS ID.
func (u *dnsUpstream) register(p *dnsPending) (uint16, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) >= dnsMaxPendingPerSock {
		return 0, false
	}
	for {
		id := uint16(rand.Uint32())
		if _, used := u.pending[id]; !used {
			u.pending[id] = p
			return id, true
		}
	}
}

func (u *dnsUpstream) take(id uint16) *dnsPending {
	u.mu.Lock()
	defer u.mu.Unlock()
	p := u.pending[id]
	delete(u.pending, id)
	return p
}

// upstreamFor returns one of the backend's sockets, dialing them on first use.
func (d *dnsProxy) upstreamFor(b *models.BackendServer) (*dnsUpstream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inst.ctx.Err() != nil {
		return nil, net.ErrClosed
	}

	pool := d.pools[b.ID]
	if pool == nil {
		raddr, err := net.ResolveUDPAddr("udp", backendAddr(b.Address, b.Port))
		if err != nil {
			return nil, err
		}
		pool = &dnsPool{}
		for i := 0; i < dnsSocketsPerNode; i++ {
			conn, err := net.DialUDP("udp", nil, raddr)
			if err != nil {
				for _, u := range pool.upstreams {
					u.conn.Close()
				}
				return nil, err
			}
			conn.SetReadBuffer(1 << 20)
			u := &dnsUpstream{conn: conn, pending: make(map[uint16]*dnsPending)}
			pool.upstreams = append(pool.upstreams, u)
			go d.readReplies(u)
		}
		d.pools[b.ID] = pool
	}
	i := atomic.AddUint32(&pool.next, 1) % uint32(len(pool.upstreams))
	return pool.upstreams[i], nil
}

func (d *dnsProxy) handleQuery(data []byte, client *net.UDPAddr) {
	l := d.inst
	if len(data) < dnsHeaderLen || data[2]&0x80 != 0 {
		return // too short, or a response rather than a query
	}

	target := l.pickBackend(client.IP.String(), 0)
	if target == nil {
		Metrics.RecordRequest(l.Config.ID, 0, true)
		return
	}
	up, err := d.upstreamFor(target)
	if err != nil {
		l.releaseBackend(target)
		Metrics.RecordRequest(l.Config.ID, 0, true)
		Metrics.RecordBackendRequest(target.ID, 0, true)
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to open DNS socket to %s: %v", backendAddr(target.Address, target.Port), err))
		return
	}

	pend := &dnsPending{client: client, origID: binary.BigEndian.Uint16(data), backend: target, sent: time.Now()}
	id, ok := up.register(pend)
	if !ok {
		l.releaseBackend(target)
		Metrics.RecordRequest(l.Config.ID, 0, true)
		return
	}

	msg := make([]byte, 0, len(data)+64)
	if l.Config.ProxyProtocolEnabled {
		header := buildProxyHeader(l.Config.ProxyProtocolVersion, client, d.listener.LocalAddr())
		if hb, err := header.Format(); err == nil {
			msg = append(msg, hb...)
		}
	}
	idOffset := len(msg)
	msg = append(msg, data...)
	binary.BigEndian.PutUint16(msg[idOffset:], id)

	if _, err := up.conn.Write(msg); err != nil {
		if up.take(id) != nil {
			l.releaseBackend(target)
			Metrics.RecordRequest(l.Config.ID, 0, true)
			Metrics.RecordBackendRequest(target.ID, 0, true)
		}
	}
}

// readReplies relays answers from one backend socket back to their clients.
func (d *dnsProxy) readReplies(u *dnsUpstream) {
	l := d.inst
	buf := make([]byte, 65535)
	for {
		n, err := u.conn.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || l.ctx.Err() != nil {
				return
			}
			// e.g. ICMP port unreachable; pending queries will time out
			continue
		}
		if n < dnsHeaderLen {
			continue
		}
		pend := u.take(binary.BigEndian.Uint16(buf))
		if pend == nil {
			continue // late answer to a query that already timed out
		}
		binary.BigEndian.PutUint16(buf, pend.origID)
		_, err = d.listener.WriteToUDP(buf[:n], pend.client)

		latency := time.Since(pend.sent)
		l.releaseBackend(pend.backend)
		Metrics.RecordRequest(l.Config.ID, latency, err != nil)
		Metrics.RecordBackendRequest(pend.backend.ID, latency, err != nil)
	}
}

// expire drops queries that got no answer in time and counts them as errors.
func (d *dnsProxy) expire(now time.Time) {
	d.mu.Lock()
	pools := make([]*dnsPool, 0, len(d.pools))
	for _, p := range d.pools {
		pools = append(pools, p)
	}
	d.mu.Unlock()

	for _, pool := range pools {
		for _, u := range pool.upstreams {
			var expired []*dnsPending
			u.mu.Lock()
			for id, p := range u.pending {
				if now.Sub(p.sent) > d.timeout {
					delete(u.pending, id)
					expired = append(expired, p)
				}
			}
			u.mu.Unlock()
			for _, p := range expired {
				d.inst.releaseBackend(p.backend)
				Metrics.RecordRequest(d.inst.Config.ID, d.timeout, true)
				Metrics.RecordBackendRequest(p.backend.ID, d.timeout, true)
			}
		}
	}
}

func (d *dnsProxy) close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, pool := range d.pools {
		for _, u := range pool.upstreams {
			u.conn.Close()
			u.mu.Lock()
			for id, p := range u.pending {
				delete(u.pending, id)
				d.inst.releaseBackend(p.backend)
			}
			u.mu.Unlock()
		}
	}
	d.pools = map[uint]*dnsPool{}
}

// socketCount reports open backend sockets (for tests).
func (d *dnsProxy) socketCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, pool := range d.pools {
		n += len(pool.upstreams)
	}
	return n
}

// startDNS serves DNS over UDP (ID-rewriting proxy) and TCP on the same port;
// clients retry over TCP when a UDP answer is truncated.
func (l *LoadBalancerInstance) startDNS() error {
	addr := l.listenAddr()
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	tcpListener, err := net.Listen("tcp", addr)
	if err != nil {
		conn.Close()
		return err
	}
	conn.SetReadBuffer(4 << 20)

	proxy := newDNSProxy(l, conn)
	l.dns.Store(proxy)
	l.setState(statusRunning, "")
	Logger.InfoLB(l.Config.Name, fmt.Sprintf("DNS LoadBalancer %s listening on %s (UDP + TCP)", l.Config.Name, addr))

	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		l.serveTCP(tcpListener)
	}()

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-l.ctx.Done():
				conn.Close()
				return
			case now := <-ticker.C:
				proxy.expire(now)
			}
		}
	}()

	buf := make([]byte, 65535)
	var delay time.Duration
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			if l.ctx.Err() != nil {
				break
			}
			delay = nextBackoff(delay)
			Logger.ErrorLB(l.Config.Name, fmt.Sprintf("ReadFromUDP error on %s: %v", l.Config.Name, err))
			time.Sleep(delay)
			continue
		}
		delay = 0
		if !l.admit(client) {
			continue
		}
		proxy.handleQuery(buf[:n], client)
	}

	proxy.close()
	<-tcpDone
	return nil
}
