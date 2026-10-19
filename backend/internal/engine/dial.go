package engine

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/balancer/backend/internal/models"
)

const (
	// A client connection tries at most this many different nodes
	maxDialAttempts = 3
	// A node that refused or timed out a connection is skipped for this long,
	// without waiting for the periodic health check to notice
	passiveEjectTime = 10 * time.Second
	// Repeated connection errors to one node are logged at most this often
	failLogInterval = 10 * time.Second
)

var errNoBackend = errors.New("no backend available")

// failTracker throttles the log: when a node goes down, thousands of clients
// reconnect at once and each would otherwise add its own identical line,
// pushing everything else out of the 1000-entry log.
type failTracker struct {
	mu      sync.Mutex
	count   int
	lastLog time.Time
}

// note records a failure and reports how many failures the next log line
// should cover, or 0 when this one is to be suppressed.
func (t *failTracker) note(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	if now.Sub(t.lastLog) < failLogInterval {
		return 0
	}
	n := t.count
	t.count, t.lastLog = 0, now
	return n
}

func (l *LoadBalancerInstance) isEjected(id uint) bool {
	until, ok := l.ejected.Load(id)
	return ok && time.Now().UnixNano() < until.(int64)
}

// noteDialFailure takes the node out of rotation for a few seconds and logs
// the failure (summarised when they come in bursts).
func (l *LoadBalancerInstance) noteDialFailure(b *models.BackendServer, err error) {
	now := time.Now()
	l.ejected.Store(b.ID, now.Add(passiveEjectTime).UnixNano())
	v, _ := l.dialFails.LoadOrStore(b.ID, &failTracker{})
	n := v.(*failTracker).note(now)
	addr := backendAddr(b.Address, b.Port)
	switch {
	case n == 1:
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("Failed to connect to backend %s: %v", addr, err))
	case n > 1:
		Logger.ErrorLB(l.Config.Name, fmt.Sprintf("%d failed connections to backend %s since the last message; latest: %v", n, addr, err))
	}
}

// noteDialSuccess returns a node that failed earlier to rotation.
func (l *LoadBalancerInstance) noteDialSuccess(b *models.BackendServer) {
	if _, was := l.ejected.LoadAndDelete(b.ID); was {
		Logger.InfoLB(l.Config.Name, fmt.Sprintf("Backend %s accepts connections again", backendAddr(b.Address, b.Port)))
	}
}

// dialTCPBackend connects a new client to a node and reserves a connection
// slot on it; the caller releases the slot. A refused or timed-out connect is
// retried on another node straight away (nothing has been sent yet, so that
// is safe) instead of dropping the client until the health check catches up.
// The nodes that were active come first; if they all fail, the healthy
// backups are tried.
func (l *LoadBalancerInstance) dialTCPBackend(clientIP string) (*models.BackendServer, net.Conn, error) {
	timeout := msOr(l.Config.ConnectTimeout, defaultConnectTimeout)
	budget := time.Now().Add(2 * timeout) // slow failures must not pile up into a long wait
	lists := [][]*models.BackendServer{
		l.backends.Load().([]*models.BackendServer),
		l.fallback.Load().([]*models.BackendServer),
	}
	tried := make(map[uint]bool, maxDialAttempts)
	lastErr := errNoBackend

	for i, list := range lists {
		for len(tried) < maxDialAttempts && time.Now().Before(budget) {
			var target *models.BackendServer
			if i == 1 && l.Config.BackupMode == models.BackupOrdered {
				target = l.firstUntried(list, tried) // backups are used in list order
			} else {
				target = l.pickFrom(list, clientIP, 0, tried)
			}
			if target == nil {
				break
			}
			tried[target.ID] = true

			start := time.Now()
			conn, err := net.DialTimeout("tcp", backendAddr(target.Address, target.Port), timeout)
			Metrics.RecordBackendRequest(target.ID, time.Since(start), err != nil)
			if err == nil {
				l.noteDialSuccess(target)
				return target, conn, nil
			}
			l.releaseBackend(target)
			l.noteDialFailure(target, err)
			lastErr = err
		}
	}
	return nil, nil, lastErr
}

// firstUntried returns the first node of the list that has not been tried and
// has a free connection slot, reserving the slot.
func (l *LoadBalancerInstance) firstUntried(list []*models.BackendServer, tried map[uint]bool) *models.BackendServer {
	for _, b := range list {
		if !tried[b.ID] && l.conns.tryAcquire(b.ID, b.MaxConns) {
			return b
		}
	}
	return nil
}
