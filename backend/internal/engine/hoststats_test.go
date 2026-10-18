package engine

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

type fakeProc struct {
	t   *testing.T
	dir string
}

func newFakeProc(t *testing.T) *fakeProc {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &fakeProc{t: t, dir: dir}
	p.write("meminfo", "MemTotal:        8000000 kB\nMemFree:          500000 kB\nMemAvailable:    6000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1500000 kB\n")
	p.write("loadavg", "0.52 0.58 0.61 1/389 12345\n")
	p.write("uptime", "350735.47 234388.90\n")
	p.write("net/route", "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"+
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"+
		"eth1\t00000000\t0102A8C0\t0003\t0\t0\t200\t00000000\t0\t0\t0\n"+
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n")
	return p
}

func (p *fakeProc) write(name, content string) {
	if err := os.WriteFile(filepath.Join(p.dir, name), []byte(content), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// cpu sets the aggregate counters (user nice system idle iowait irq softirq steal) for 4 cores.
func (p *fakeProc) cpu(user, system, idle, iowait uint64) {
	line := func(name string) string {
		return name + " " + itoa(user) + " 0 " + itoa(system) + " " + itoa(idle) + " " + itoa(iowait) + " 0 0 0 0 0\n"
	}
	p.write("stat", line("cpu")+line("cpu0")+line("cpu1")+line("cpu2")+line("cpu3")+"intr 1 2 3\nctxt 42\n")
}

func (p *fakeProc) net(eth0Rx, eth0Tx uint64) {
	p.write("net/dev", "Inter-|   Receive                                                |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo: 999999 10 0 0 0 0 0 0 999999 10 0 0 0 0 0 0\n"+
		"  eth0: "+itoa(eth0Rx)+" 10 0 0 0 0 0 0 "+itoa(eth0Tx)+" 10 0 0 0 0 0 0\n"+
		"docker0: 5000000 10 0 0 0 0 0 0 5000000 10 0 0 0 0 0 0\n")
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func TestHostSampler(t *testing.T) {
	p := newFakeProc(t)
	h := NewHostSampler(p.dir)
	t0 := time.Unix(1_700_000_000, 0)

	p.cpu(1000, 500, 8000, 500)
	p.net(1_000_000, 2_000_000)
	h.Sample(t0)
	if s := h.Stats(); !s.Available || len(s.History) != 0 {
		t.Fatalf("baseline: available=%v history=%d", s.Available, len(s.History))
	}

	// +300 busy, +700 idle/iowait => 30 %; eth0 +5 MB in, +1 MB out over 5 s
	p.cpu(1200, 600, 8600, 600)
	p.net(6_000_000, 3_000_000)
	h.Sample(t0.Add(5 * time.Second))

	s := h.Stats()
	if len(s.History) != 1 {
		t.Fatalf("history: %d", len(s.History))
	}
	snap := s.History[0]
	if math.Abs(snap.CPUPercent-30) > 0.01 {
		t.Errorf("cpu: %v, want 30", snap.CPUPercent)
	}
	if snap.MemUsed != 2_000_000*1024 || s.MemTotal != 8_000_000*1024 {
		t.Errorf("memory: used %d total %d", snap.MemUsed, s.MemTotal)
	}
	if s.SwapUsed != 500_000*1024 || s.SwapTotal != 2_000_000*1024 {
		t.Errorf("swap: used %d total %d", s.SwapUsed, s.SwapTotal)
	}
	if s.Interface != "eth0" || snap.NetRxBps != 1_000_000 || snap.NetTxBps != 200_000 {
		t.Errorf("network: %s rx %v tx %v", s.Interface, snap.NetRxBps, snap.NetTxBps)
	}
	if s.CPUs != 4 || s.Load != [3]float64{0.52, 0.58, 0.61} || s.Uptime != 350735 {
		t.Errorf("cpus %d load %v uptime %d", s.CPUs, s.Load, s.Uptime)
	}

	// A counter reset (interface re-created) gives no rate instead of a huge one
	p.cpu(1200, 600, 9600, 600)
	p.net(100, 100)
	h.Sample(t0.Add(10 * time.Second))
	if last := h.Stats().History[1]; last.NetRxBps != 0 || last.NetTxBps != 0 || last.CPUPercent != 0 {
		t.Errorf("after reset: %+v", last)
	}

	// Only the last five minutes are kept
	for i := 3; i < 100; i++ {
		h.Sample(t0.Add(time.Duration(i) * 5 * time.Second))
	}
	if n := len(h.Stats().History); n != hostHistoryLen {
		t.Errorf("history length: %d", n)
	}
}

func TestHostSamplerFallbacks(t *testing.T) {
	// No default route: the busiest physical interface is used, never lo or docker0
	p := newFakeProc(t)
	p.write("net/route", "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n")
	p.cpu(1, 1, 1, 1)
	p.net(10, 10)
	h := NewHostSampler(p.dir)
	h.Sample(time.Now())
	if s := h.Stats(); s.Interface != "eth0" {
		t.Errorf("interface: %q", s.Interface)
	}

	// No /proc at all (macOS, Windows)
	h = NewHostSampler(filepath.Join(t.TempDir(), "missing"))
	h.Sample(time.Now())
	if s := h.Stats(); s.Available {
		t.Error("available without /proc")
	}
}
