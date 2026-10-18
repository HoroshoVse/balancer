package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/balancer/backend/internal/models"
	"gorm.io/gorm"
)

// Defaults for backends whose health check fields are left at zero.
const (
	defaultHCInterval  = 5 * time.Second
	defaultHCTimeout   = 2 * time.Second
	defaultHCThreshold = 1
)

// HealthTarget is a backend to check and the balancer it belongs to.
type HealthTarget struct {
	Backend models.BackendServer
	LBName  string
}

type backendHealth struct {
	known     bool // false until the first check completes
	healthy   bool
	fails     int // consecutive failures while healthy
	successes int // consecutive successes while unhealthy
	nextCheck time.Time
	checking  bool
}

type HealthChecker struct {
	db       *gorm.DB
	cancel   context.CancelFunc
	client   *http.Client
	onChange func() // callback when health state changes

	mu      sync.Mutex
	targets map[uint]HealthTarget
	states  map[uint]*backendHealth

	lastTelegram atomic.Pointer[TelegramDelivery]
}

func NewHealthChecker(db *gorm.DB) *HealthChecker {
	return &HealthChecker{
		db: db,
		client: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				DisableKeepAlives: true,
			},
			// A redirect means the backend answered; don't follow it.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		targets: make(map[uint]HealthTarget),
		states:  make(map[uint]*backendHealth),
	}
}

// SetOnChange must be called before Start.
func (hc *HealthChecker) SetOnChange(fn func()) {
	hc.onChange = fn
}

// SetTargets replaces the set of backends to check. Known health states are
// kept for backends that remain, and they are re-checked right away.
func (hc *HealthChecker) SetTargets(targets []HealthTarget) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	hc.targets = make(map[uint]HealthTarget, len(targets))
	for _, t := range targets {
		hc.targets[t.Backend.ID] = t
	}
	for id, st := range hc.states {
		t, ok := hc.targets[id]
		if !ok || !t.Backend.Enabled || !t.Backend.HCEnabled {
			delete(hc.states, id) // unchecked backends are treated as healthy
			continue
		}
		st.nextCheck = time.Time{}
	}
}

func (hc *HealthChecker) IsHealthy(backendID uint) bool {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	st, exists := hc.states[backendID]
	if !exists || !st.known {
		return true // assume healthy if not yet checked
	}
	return st.healthy
}

func (hc *HealthChecker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	hc.cancel = cancel

	go hc.loop(ctx)
}

func (hc *HealthChecker) Stop() {
	if hc.cancel != nil {
		hc.cancel()
	}
}

func (hc *HealthChecker) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			hc.runDue(now)
		}
	}
}

// runDue starts checks for backends whose interval has elapsed.
func (hc *HealthChecker) runDue(now time.Time) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	for id, t := range hc.targets {
		if !t.Backend.Enabled || !t.Backend.HCEnabled {
			continue
		}
		st := hc.states[id]
		if st == nil {
			st = &backendHealth{healthy: true}
			hc.states[id] = st
		}
		if st.checking || now.Before(st.nextCheck) {
			continue
		}
		st.checking = true
		go func(t HealthTarget) {
			hc.record(t, hc.probe(t.Backend), time.Now())
		}(t)
	}
}

func secondsOr(v int, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return time.Duration(v) * time.Second
}

func thresholdOr(v int) int {
	if v <= 0 {
		return defaultHCThreshold
	}
	return v
}

// record applies a probe result. A backend starts out assumed healthy and
// is marked DOWN only after FailureThreshold consecutive failures (so one slow
// first answer can't take every node out), and UP again after
// RecoveryThreshold consecutive successes.
func (hc *HealthChecker) record(t HealthTarget, isUp bool, now time.Time) {
	b := t.Backend
	hc.mu.Lock()
	st := hc.states[b.ID]
	if st == nil {
		hc.mu.Unlock()
		return // backend was removed while being checked
	}
	st.checking = false
	st.nextCheck = now.Add(secondsOr(b.HCInterval, defaultHCInterval))

	changed := false
	if isUp {
		st.fails = 0
		if !st.known {
			st.known, st.healthy = true, true
		} else if !st.healthy {
			st.successes++
			if st.successes >= thresholdOr(b.HCRecoveryThreshold) {
				st.healthy, st.successes, changed = true, 0, true
			}
		}
	} else {
		st.successes = 0
		if !st.known || st.healthy {
			st.fails++
			if st.fails >= thresholdOr(b.HCFailureThreshold) {
				st.known, st.healthy, st.fails, changed = true, false, 0, true
			}
		}
	}
	hc.mu.Unlock()

	if !changed {
		return
	}

	target := backendAddr(b.Address, hcPort(b))
	statusStr := "снова в строю 🟢"
	if !isUp {
		statusStr = "не отвечает 🔴"
		Logger.WarnLB(t.LBName, fmt.Sprintf("HealthCheck FAILED for %s (%s) in LB %s", b.Name, target, t.LBName))
	} else {
		Logger.InfoLB(t.LBName, fmt.Sprintf("HealthCheck OK for %s (%s) in LB %s", b.Name, target, t.LBName))
	}

	msg := fmt.Sprintf("<b>%s</b>: узел <b>%s</b> (%s) %s",
		html.EscapeString(t.LBName), html.EscapeString(b.Name), html.EscapeString(target), statusStr)
	hc.SendTelegramAlert(msg)
	if hc.onChange != nil {
		go hc.onChange()
	}
}

func hcPort(b models.BackendServer) int {
	if b.HCPort > 0 {
		return b.HCPort
	}
	return b.Port
}

func (hc *HealthChecker) probe(b models.BackendServer) bool {
	target := backendAddr(b.Address, hcPort(b))
	timeout := secondsOr(b.HCTimeout, defaultHCTimeout)

	switch b.HCProtocol {
	case "http", "https":
		path := b.HCPath
		if path == "" {
			path = "/"
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.HCProtocol+"://"+target+path, nil)
		if err != nil {
			return false
		}
		if b.SNI != "" {
			req.Host = b.SNI
		}
		resp, err := hc.client.Do(req)
		if err != nil {
			return false
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 400
	case "udp":
		return probeUDP(target, timeout)
	case "dns":
		return probeDNS(target, b.HCPath, timeout)
	default: // TCP fallback
		conn, err := net.DialTimeout("tcp", target, timeout)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}
}

// probeUDP sends a datagram and treats an ICMP "port unreachable" as down.
// UDP has no handshake, so silence until the timeout counts as up.
func probeUDP(target string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("udp", target, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte{0}); err != nil {
		return false
	}
	buf := make([]byte, 512)
	if _, err := conn.Read(buf); err != nil {
		var ne net.Error
		return errors.As(err, &ne) && ne.Timeout()
	}
	return true
}

// probeDNS sends a real DNS query over UDP. Any answer with the matching ID
// counts as healthy (even SERVFAIL: the server is up, its upstream may not be);
// silence or ICMP errors count as down.
func probeDNS(target, name string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("udp", target, timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))

	id := uint16(time.Now().UnixNano())
	if _, err := conn.Write(buildDNSQuery(id, name)); err != nil {
		return false
	}
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return false
		}
		if n >= dnsHeaderLen && binary.BigEndian.Uint16(buf) == id && buf[2]&0x80 != 0 {
			return true
		}
	}
}

// buildDNSQuery encodes a recursive query: NS for the root zone when name is
// empty or ".", otherwise an A query for name.
func buildDNSQuery(id uint16, name string) []byte {
	name = strings.Trim(strings.TrimPrefix(name, "/"), ".")
	qtype := uint16(1) // A
	if name == "" {
		qtype = 2 // NS
	}
	msg := make([]byte, dnsHeaderLen, dnsHeaderLen+len(name)+6)
	binary.BigEndian.PutUint16(msg[0:], id)
	binary.BigEndian.PutUint16(msg[2:], 0x0100) // RD
	binary.BigEndian.PutUint16(msg[4:], 1)      // QDCOUNT
	if name != "" {
		for _, label := range strings.Split(name, ".") {
			msg = append(msg, byte(len(label)))
			msg = append(msg, label...)
		}
	}
	msg = append(msg, 0)
	return binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(msg, qtype), 1)
}

// telegramAPI is the Bot API base URL; tests point it at a local server.
var telegramAPI = "https://api.telegram.org"

// TelegramDelivery is the outcome of sending one message to Telegram.
type TelegramDelivery struct {
	At          time.Time `json:"at"`
	OK          bool      `json:"ok"`
	Status      int       `json:"status,omitempty"`      // HTTP status of Telegram's answer
	Description string    `json:"description,omitempty"` // Telegram's explanation, or the network error
	Network     bool      `json:"network,omitempty"`     // Telegram could not be reached at all
}

// SendTelegram delivers an HTML-formatted message and reports how it went.
func SendTelegram(token, chatID, message string) TelegramDelivery {
	result := TelegramDelivery{At: time.Now()}
	data, _ := json.Marshal(map[string]interface{}{"chat_id": chatID, "text": message, "parse_mode": "HTML"})
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(telegramAPI+"/bot"+token+"/sendMessage", "application/json", bytes.NewReader(data))
	if err != nil {
		// url.Error contains the URL, which includes the bot token
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		result.Network, result.Description = true, err.Error()
		return result
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	result.OK = resp.StatusCode == http.StatusOK
	if !result.OK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		var answer struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(body, &answer) == nil && answer.Description != "" {
			result.Description = answer.Description
		} else {
			result.Description = strings.TrimSpace(resp.Status + " " + string(body))
		}
	}
	return result
}

// LastTelegram returns the outcome of the latest alert, or nil if none was sent
// since the server started.
func (hc *HealthChecker) LastTelegram() *TelegramDelivery {
	return hc.lastTelegram.Load()
}

// SendTelegramAlert sends an HTML-formatted message if notifications are on.
// Callers must escape any user-provided text with html.EscapeString.
func (hc *HealthChecker) SendTelegramAlert(message string) {
	if hc.db == nil {
		return
	}
	var settings models.Settings
	if err := hc.db.Order("id").Limit(1).Find(&settings).Error; err != nil {
		return
	}

	if !settings.NotificationsEnabled || settings.TelegramBotToken == "" || settings.TelegramChatID == "" {
		return
	}

	go func() {
		result := SendTelegram(settings.TelegramBotToken, settings.TelegramChatID, message)
		hc.lastTelegram.Store(&result)
		switch {
		case result.Network:
			Logger.Warn("Telegram alert failed: " + result.Description)
		case !result.OK:
			Logger.Warn(fmt.Sprintf("Telegram alert rejected: %d %s", result.Status, strconv.Quote(result.Description)))
		}
	}()
}
