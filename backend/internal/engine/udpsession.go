package engine

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// maxUDPSessions bounds the UDP sessions one balancer keeps (var for tests).
// Every client address and port gets a session of its own, with a socket and a
// goroutine, so thousands of clients appearing at once — a flood with random
// source ports, or just a busy resolver — would otherwise use up the memory
// and the ephemeral ports. Datagrams that would start a session beyond the
// limit are dropped; existing sessions are not affected.
var maxUDPSessions = 16384

// udpBufs holds the buffers for relaying a backend's reply. A buffer is taken
// only while a datagram is being passed on, never while waiting for one: a
// 64 KiB buffer per session, kept for its whole life, is what used up the
// memory of a 1 GB server in an attack.
var udpBufs = sync.Pool{New: func() any {
	b := make([]byte, 65535)
	return &b
}}

// reserveUDPSession claims room for one more session.
func (l *LoadBalancerInstance) reserveUDPSession() bool {
	if l.udpCount.Add(1) > int64(maxUDPSessions) {
		l.udpCount.Add(-1)
		l.udpDropped.Add(1)
		return false
	}
	return true
}

// reportUDPLimit tells once a minute if datagrams were dropped for lack of room.
func (l *LoadBalancerInstance) reportUDPLimit() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
			n := l.udpDropped.Swap(0)
			if n == 0 {
				continue
			}
			for i := int64(0); i < n; i++ {
				Metrics.RecordBlocked(l.Config.ID, "limit", "session_limit")
			}
			Logger.WarnLB(l.Config.Name, fmt.Sprintf("%s: %d new UDP sessions refused in the last minute: the limit of %d sessions is reached", l.Config.Name, n, maxUDPSessions))
		}
	}
}

// relayUDPReplies forwards backend responses to the client until the session closes.
func (l *LoadBalancerInstance) relayUDPReplies(clientKey string, sess *udpSession, clientConn *net.UDPConn, clientAddr *net.UDPAddr) {
	defer l.closeUDPSession(clientKey, sess)
	wait := readWaiter(sess.conn) // blocks without holding a buffer; nil when not supported
	for {
		if wait != nil {
			if err := wait(); err != nil {
				return
			}
		}
		bp := udpBufs.Get().(*[]byte)
		n, err := sess.conn.Read(*bp)
		if err == nil {
			atomic.StoreInt64(&sess.lastSeen, time.Now().Unix())
			_, err = clientConn.WriteToUDP((*bp)[:n], clientAddr)
		}
		udpBufs.Put(bp)
		if err != nil {
			return
		}
	}
}
