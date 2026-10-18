package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const handshakePrefix = "TLS handshake error from "

// handshakeLog receives http.Server's error log. Failed TLS handshakes get one
// line per connection: scanners and old clients produce a steady trickle, and
// when no certificate is available every client fails at once, flooding the
// log with hundreds of lines a second. Those lines are counted and summarised
// once per interval; anything else is logged as is.
type handshakeLog struct {
	lbName string
	// critical are the reasons that mean real clients are turned away: no
	// certificate for one of the balancer's own domains. Scanners asking for
	// an IP address or a random name only produce a warning.
	critical map[string]bool
	mu       sync.Mutex
	count    int
	reasons  map[string]int
}

func newHandshakeLog(lbName string, domains []string) *handshakeLog {
	critical := make(map[string]bool, len(domains))
	for _, d := range domains {
		critical[fmt.Sprintf("no certificate available for '%s'", strings.ToLower(d))] = true
	}
	return &handshakeLog{lbName: lbName, critical: critical, reasons: make(map[string]int)}
}

func (h *handshakeLog) Write(p []byte) (int, error) {
	line := strings.TrimSpace(string(p))
	i := strings.Index(line, handshakePrefix)
	if i < 0 {
		Logger.WarnLB(h.lbName, line)
		return len(p), nil
	}
	reason := handshakeReason(line[i+len(handshakePrefix):])
	h.mu.Lock()
	h.count++
	if _, seen := h.reasons[reason]; seen || len(h.reasons) < 50 {
		h.reasons[reason]++
	}
	h.mu.Unlock()
	return len(p), nil
}

// handshakeReason drops the client address so that equal errors group together:
// "1.2.3.4:5: read tcp 10.0.0.1:443->1.2.3.4:5: read: connection reset by peer"
// becomes "connection reset by peer".
func handshakeReason(rest string) string {
	if j := strings.Index(rest, ": "); j >= 0 {
		rest = rest[j+2:]
	}
	if strings.HasPrefix(rest, "read tcp ") || strings.HasPrefix(rest, "write tcp ") {
		if j := strings.LastIndex(rest, ": "); j >= 0 {
			rest = rest[j+2:]
		}
	}
	return rest
}

// flush logs the summary since the last flush, if there were failures.
func (h *handshakeLog) flush(interval time.Duration) {
	h.mu.Lock()
	count, reasons := h.count, h.reasons
	h.count, h.reasons = 0, make(map[string]int)
	h.mu.Unlock()
	if count == 0 {
		return
	}

	type entry struct {
		reason string
		n      int
	}
	var top []entry
	for r, n := range reasons {
		top = append(top, entry{r, n})
	}
	sort.Slice(top, func(a, b int) bool {
		return top[a].n > top[b].n || top[a].n == top[b].n && top[a].reason < top[b].reason
	})
	if len(top) > 3 {
		top = top[:3]
	}
	parts := make([]string, len(top))
	critical := false
	for i, e := range top {
		parts[i] = fmt.Sprintf("%s ×%d", e.reason, e.n)
		critical = critical || h.critical[e.reason]
	}
	msg := fmt.Sprintf("%s: %d failed TLS handshakes in the last %s; most common: %s", h.lbName, count, interval, strings.Join(parts, "; "))
	if critical {
		Logger.ErrorLB(h.lbName, msg)
	} else {
		Logger.WarnLB(h.lbName, msg)
	}
}

// run flushes every interval until ctx is cancelled, then flushes once more.
func (h *handshakeLog) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.flush(interval)
			return
		case <-ticker.C:
			h.flush(interval)
		}
	}
}
