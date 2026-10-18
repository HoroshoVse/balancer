package engine

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

type failoverRig struct {
	t      *testing.T
	hc     *HealthChecker
	inst   *LoadBalancerInstance
	alerts []string
}

// newFailoverRig builds an instance whose nodes are health-checked (by hand)
// and whose alerts are collected instead of sent.
func newFailoverRig(t *testing.T, mode string, nodes ...models.BackendServer) *failoverRig {
	cfg := models.LoadBalancer{Name: "TCP", Protocol: "tcp", BackupMode: mode, BackendGroup: models.BackendGroup{Backends: nodes}}
	r := &failoverRig{t: t, hc: NewHealthChecker(nil)}
	r.inst = NewLoadBalancerInstance(cfg, r.hc)
	r.inst.notify = func(msg string) { r.alerts = append(r.alerts, msg) }
	r.inst.updateBackends()
	return r
}

func node(id uint, name string, backup bool) models.BackendServer {
	return models.BackendServer{ID: id, Name: name, Address: "127.0.0.1", Port: 9, Enabled: true, Backup: backup, HCEnabled: true}
}

// set marks nodes up or down and lets the instance react, as a health change does.
func (r *failoverRig) set(healthy bool, ids ...uint) {
	r.hc.mu.Lock()
	for _, id := range ids {
		r.hc.states[id] = &backendHealth{known: true, healthy: healthy}
	}
	r.hc.mu.Unlock()
	r.inst.updateBackends()
}

func (r *failoverRig) expect(active string, alert string) {
	r.t.Helper()
	var names []string
	for _, b := range r.inst.backends.Load().([]*models.BackendServer) {
		names = append(names, b.Name)
	}
	if got := strings.Join(names, ","); got != active {
		r.t.Fatalf("active nodes %q, want %q", got, active)
	}
	switch {
	case alert == "" && len(r.alerts) > 0:
		r.t.Fatalf("unexpected alerts %q", r.alerts)
	case alert != "" && (len(r.alerts) != 1 || !strings.Contains(r.alerts[0], alert)):
		r.t.Fatalf("alerts %q, want one containing %q", r.alerts, alert)
	}
	r.alerts = nil
}

func TestOrderedBackups(t *testing.T) {
	r := newFailoverRig(t, models.BackupOrdered, node(1, "node1", false), node(2, "node2", true), node(3, "node3", true))
	r.expect("node1", "")

	r.set(false, 1)
	r.expect("node2", "основные узлы не отвечают — трафик переключён на резерв <b>node2</b>.")
	r.set(false, 2)
	r.expect("node3", "резерв <b>node2</b> не отвечает — трафик переключён на следующий, <b>node3</b>.")
	r.set(true, 2)
	r.expect("node2", "резерв <b>node2</b> снова отвечает — трафик вернулся на него, <b>node3</b> снова ждёт.")
	r.set(false, 2, 3)
	r.expect("", "недоступны все узлы, включая резерв!")
	r.set(true, 3)
	r.expect("node3", "резерв снова отвечает — трафик идёт на <b>node3</b>. Основные узлы пока не отвечают.")
	r.set(true, 1)
	r.expect("node1", "основные узлы вернулись, резерв снова ждёт.")
}

func TestAllBackupsTogether(t *testing.T) {
	r := newFailoverRig(t, models.BackupAll, node(1, "node1", false), node(2, "node2", true), node(3, "node3", true))
	r.set(false, 1)
	r.expect("node2,node3", "трафик переключён на резерв <b>node2</b>, <b>node3</b>.")
	// One of several serving backups failing is reported per node, not here
	r.set(false, 2)
	r.expect("node3", "")
}

func TestNoBackupWording(t *testing.T) {
	r := newFailoverRig(t, models.BackupAll, node(1, "web<1>", false))
	r.set(false, 1)
	r.expect("", "не отвечает ни один узел — трафику некуда идти!")
	r.set(true, 1)
	r.expect("web<1>", "узлы снова отвечают, трафик идёт.")
}

func TestStartupStaysQuiet(t *testing.T) {
	hc := NewHealthChecker(nil)
	hc.states[1] = &backendHealth{known: true, healthy: false}
	cfg := models.LoadBalancer{Name: "TCP", BackupMode: models.BackupOrdered, BackendGroup: models.BackendGroup{Backends: []models.BackendServer{node(1, "node1", false), node(2, "node2", true)}}}
	inst := NewLoadBalancerInstance(cfg, hc)
	var alerts []string
	inst.notify = func(msg string) { alerts = append(alerts, msg) }
	inst.updateBackends()
	if len(alerts) != 0 {
		t.Fatalf("a restart with a node already down must not alert: %q", alerts)
	}
}

func TestRenewalAlerts(t *testing.T) {
	r := newFailoverRig(t, models.BackupAll, node(1, "node1", false))
	r.inst.reportRenewal("example.com", false, 72*time.Hour, errors.New("acme: <boom>"))
	r.inst.reportRenewal("example.com", false, 71*time.Hour, errors.New("again"))
	if len(r.alerts) != 1 || !strings.Contains(r.alerts[0], "не удалось продлить сертификат для example.com. Текущий действует ещё 3 дн.") || !strings.Contains(r.alerts[0], "acme: &lt;boom&gt;") {
		t.Fatalf("failure alerts: %q", r.alerts)
	}
	r.alerts = nil
	r.inst.reportRenewal("example.com", true, 0, nil)
	r.inst.reportRenewal("example.com", true, 0, nil)
	if len(r.alerts) != 1 || !strings.Contains(r.alerts[0], "сертификат для example.com продлён") {
		t.Fatalf("recovery alerts: %q", r.alerts)
	}
}

func TestSendTelegram(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		if strings.Contains(r.URL.Path, "badchat") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	old := telegramAPI
	telegramAPI = srv.URL
	t.Cleanup(func() { telegramAPI = old })

	if res := SendTelegram("123:abc", "42", "hi"); !res.OK || got != "/bot123:abc/sendMessage" {
		t.Fatalf("success: %+v path %q", res, got)
	}
	if res := SendTelegram("123:badchat", "42", "hi"); res.OK || res.Status != 400 || res.Description != "Bad Request: chat not found" {
		t.Fatalf("rejected: %+v", res)
	}
	srv.Close()
	res := SendTelegram("123:secret-token", "42", "hi")
	if res.OK || !res.Network || strings.Contains(res.Description, "secret-token") {
		t.Fatalf("network error: %+v", res)
	}
}
