package engine

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/models"
)

// dialTestLB starts a TCP balancer over the given nodes (all on 127.0.0.1).
func dialTestLB(t *testing.T, id uint, mutate func(*models.LoadBalancer), nodes ...models.BackendServer) (*LoadBalancerInstance, string) {
	t.Helper()
	for i := range nodes {
		nodes[i].Address, nodes[i].Enabled = "127.0.0.1", true
	}
	cfg := models.LoadBalancer{
		ID: id, Name: fmt.Sprintf("dial-%d", id), Protocol: "tcp", Algorithm: "round_robin",
		ListenIP: "127.0.0.1", ListenPort: freePort(t, "tcp"),
		BackendGroup: models.BackendGroup{Backends: nodes},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	inst := startInstance(t, cfg)
	return inst, backendAddr("127.0.0.1", cfg.ListenPort)
}

// echoOnce sends a message through the balancer and reports whether it came back.
func echoOnce(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		return err
	}
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil {
		return err
	}
	if string(buf) != "ping" {
		return fmt.Errorf("got %q", buf)
	}
	return nil
}

func countLogs(lbName, substr string) int {
	n := 0
	for _, e := range Logger.GetLogs() {
		if e.LBName == lbName && strings.Contains(e.Message, substr) {
			n++
		}
	}
	return n
}

// The incident: one of the nodes refuses connections (Xray restarting).
// Clients must not notice; before, each connection that landed on it was dropped.
func TestTCPRetriesAnotherNodeWhenOneRefuses(t *testing.T) {
	dead := freePort(t, "tcp")
	inst, addr := dialTestLB(t, 1001, nil,
		models.BackendServer{ID: 1002, Port: dead},
		models.BackendServer{ID: 1003, Port: echoServer(t)},
	)
	for i := 0; i < 40; i++ {
		if err := echoOnce(addr); err != nil {
			t.Fatalf("connection %d failed although a healthy node exists: %v", i, err)
		}
	}
	if !inst.isEjected(1002) {
		t.Fatal("the refusing node was not taken out of rotation")
	}
	if inst.isEjected(1003) {
		t.Fatal("the healthy node was taken out of rotation")
	}
}

func TestTCPFallsBackToBackupsWhenPrimariesRefuse(t *testing.T) {
	dead := freePort(t, "tcp")
	for _, mode := range []string{models.BackupAll, models.BackupOrdered} {
		t.Run(mode, func(t *testing.T) {
			id := uint(1010)
			if mode == models.BackupOrdered {
				id = 1020
			}
			_, addr := dialTestLB(t, id, func(lb *models.LoadBalancer) { lb.BackupMode = mode },
				models.BackendServer{ID: id + 1, Port: dead},
				models.BackendServer{ID: id + 2, Port: dead, Backup: true}, // first backup is down as well
				models.BackendServer{ID: id + 3, Port: echoServer(t), Backup: true},
			)
			for i := 0; i < 10; i++ {
				if err := echoOnce(addr); err != nil {
					t.Fatalf("connection %d: %v", i, err)
				}
			}
		})
	}
}

func TestRefusingNodeIsSkippedThenRetriedWhenAlone(t *testing.T) {
	dead := freePort(t, "tcp")
	live := echoServer(t)
	inst, _ := dialTestLB(t, 1030, nil,
		models.BackendServer{ID: 1031, Port: dead},
		models.BackendServer{ID: 1032, Port: live},
	)
	backends := inst.backends.Load().([]*models.BackendServer)
	inst.noteDialFailure(backends[0], fmt.Errorf("connection refused"))

	for i := 0; i < 50; i++ {
		got := inst.pickFrom(backends, "203.0.113.1", 0, nil)
		if got.ID != 1032 {
			t.Fatalf("pick %d chose the node that just refused (id %d)", i, got.ID)
		}
		inst.releaseBackend(got)
	}
	// Only the failed node left: still tried, so a node that came back is used at once
	got := inst.pickFrom(backends[:1], "203.0.113.1", 0, nil)
	if got == nil || got.ID != 1031 {
		t.Fatalf("a lone ejected node must still be tried, got %v", got)
	}
	inst.releaseBackend(got)

	// ...and it is back in rotation after one successful connection
	inst.noteDialSuccess(backends[0])
	if inst.isEjected(1031) {
		t.Fatal("node still ejected after a successful connection")
	}
}

func TestTCPAllNodesDownClosesClientAndLogsInBulk(t *testing.T) {
	_, addr := dialTestLB(t, 1040, nil,
		models.BackendServer{ID: 1041, Port: freePort(t, "tcp")},
		models.BackendServer{ID: 1042, Port: freePort(t, "tcp")},
	)
	start := time.Now()
	for i := 0; i < 60; i++ {
		if err := echoOnce(addr); err == nil {
			t.Fatal("connection succeeded with every node down")
		}
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("60 refused connections took %v", took)
	}
	// 60 connections x 2 nodes = 120 failures; the log must not carry one line each
	if n := countLogs("dial-1040", "Failed to connect to backend"); n < 1 || n > 4 {
		t.Fatalf("%d 'Failed to connect' lines for 120 failures, want a handful", n)
	}
}

func TestFailTrackerThrottle(t *testing.T) {
	var ft failTracker
	t0 := time.Now()
	if n := ft.note(t0); n != 1 {
		t.Fatalf("first failure reports %d, want 1", n)
	}
	for i := 0; i < 5; i++ {
		if n := ft.note(t0.Add(time.Second)); n != 0 {
			t.Fatalf("failure within the interval reports %d, want it suppressed", n)
		}
	}
	if n := ft.note(t0.Add(failLogInterval + time.Second)); n != 6 {
		t.Fatalf("after the interval reports %d, want the 6 failures since the last message", n)
	}
}
