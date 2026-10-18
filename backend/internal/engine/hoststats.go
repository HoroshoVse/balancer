package engine

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Load of the machine the balancer runs on, read from /proc (Linux only).
// In Docker this is still the host: /proc/stat and /proc/meminfo aren't
// namespaced, and the backend runs with network_mode: host.

const hostHistoryLen = 60 // 5 minutes at one sample per 5 seconds

type HostSnapshot struct {
	TS         int64   `json:"ts"`          // unix milliseconds
	CPUPercent float64 `json:"cpu_percent"` // all cores together, 0-100
	MemUsed    uint64  `json:"mem_used"`    // bytes: total minus available
	NetRxBps   float64 `json:"net_rx_bps"`  // bytes per second received on Interface
	NetTxBps   float64 `json:"net_tx_bps"`  // bytes per second sent on Interface
}

type HostStats struct {
	Available bool           `json:"available"` // false where there is no /proc
	CPUs      int            `json:"cpus"`
	Load      [3]float64     `json:"load"` // load average over 1, 5 and 15 minutes
	MemTotal  uint64         `json:"mem_total"`
	SwapTotal uint64         `json:"swap_total"`
	SwapUsed  uint64         `json:"swap_used"`
	Interface string         `json:"interface"` // the default-route interface
	Uptime    int64          `json:"uptime_seconds"`
	History   []HostSnapshot `json:"history"`
}

type cpuTimes struct{ busy, total uint64 }

type netCounters struct {
	iface  string
	rx, tx uint64
}

// HostSampler keeps the last five minutes of host load.
type HostSampler struct {
	proc string

	mu      sync.RWMutex
	stats   HostStats
	prevCPU cpuTimes
	prevNet netCounters
	prevAt  time.Time
}

var Host = NewHostSampler("/proc")

func NewHostSampler(procRoot string) *HostSampler {
	return &HostSampler{proc: procRoot}
}

// Start samples every 5 seconds, in step with the traffic metrics.
func (h *HostSampler) Start() {
	h.Sample(time.Now()) // baseline for the first rates
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for now := range ticker.C {
			h.Sample(now)
		}
	}()
}

// Stats returns a copy of the current readings and history.
func (h *HostSampler) Stats() HostStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	stats := h.stats
	stats.History = append(make([]HostSnapshot, 0, len(h.stats.History)), h.stats.History...)
	return stats
}

// Sample reads /proc once. Rates need a previous reading, so the first
// call only records a baseline.
func (h *HostSampler) Sample(now time.Time) {
	cpu, cpus, err := readCPU(h.path("stat"))
	if err != nil {
		h.mu.Lock()
		h.stats.Available = false
		h.mu.Unlock()
		return
	}
	mem := readMeminfo(h.path("meminfo"))
	net := readNetDev(h.path("net/dev"), defaultInterface(h.path("net/route")))
	load := readLoadavg(h.path("loadavg"))
	uptime := readUptime(h.path("uptime"))

	memUsed := mem["MemTotal"] - min(mem["MemAvailable"], mem["MemTotal"])

	h.mu.Lock()
	defer h.mu.Unlock()
	s := &h.stats
	s.Available = true
	s.CPUs = cpus
	s.Load = load
	s.MemTotal = mem["MemTotal"]
	s.SwapTotal = mem["SwapTotal"]
	s.SwapUsed = mem["SwapTotal"] - min(mem["SwapFree"], mem["SwapTotal"])
	s.Interface = net.iface
	s.Uptime = uptime

	if !h.prevAt.IsZero() {
		snap := HostSnapshot{TS: now.UnixMilli(), MemUsed: memUsed}
		if cpu.total > h.prevCPU.total && cpu.busy >= h.prevCPU.busy {
			snap.CPUPercent = min(100, float64(cpu.busy-h.prevCPU.busy)/float64(cpu.total-h.prevCPU.total)*100)
		}
		// Counters restart when the interface changes or the driver resets them
		if secs := now.Sub(h.prevAt).Seconds(); secs > 0 && net.iface == h.prevNet.iface && net.rx >= h.prevNet.rx && net.tx >= h.prevNet.tx {
			snap.NetRxBps = float64(net.rx-h.prevNet.rx) / secs
			snap.NetTxBps = float64(net.tx-h.prevNet.tx) / secs
		}
		s.History = append(s.History, snap)
		if len(s.History) > hostHistoryLen {
			s.History = s.History[len(s.History)-hostHistoryLen:]
		}
	}
	h.prevCPU, h.prevNet, h.prevAt = cpu, net, now
}

func (h *HostSampler) path(name string) string {
	return filepath.Join(h.proc, name)
}

// readCPU sums the aggregate "cpu" line of /proc/stat and counts the cores.
func readCPU(path string) (cpuTimes, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return cpuTimes{}, 0, err
	}
	defer f.Close()

	var t cpuTimes
	cpus := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cpus++
			continue
		}
		// user nice system idle iowait irq softirq steal; guest time is already in user
		for i := 1; i < len(fields) && i <= 8; i++ {
			v, _ := strconv.ParseUint(fields[i], 10, 64)
			t.total += v
			if i != 4 && i != 5 { // idle, iowait
				t.busy += v
			}
		}
	}
	return t, cpus, sc.Err()
}

// readMeminfo returns /proc/meminfo values in bytes.
func readMeminfo(path string) map[string]uint64 {
	values := map[string]uint64{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		fields := strings.Fields(rest)
		if !ok || len(fields) == 0 {
			continue
		}
		v, _ := strconv.ParseUint(fields[0], 10, 64)
		if len(fields) > 1 && fields[1] == "kB" {
			v *= 1024
		}
		values[key] = v
	}
	return values
}

// defaultInterface returns the interface of the default route with the lowest metric.
func defaultInterface(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	best, bestMetric := "", uint64(0)
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		// Iface Destination Gateway Flags RefCnt Use Metric Mask ...
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		metric, _ := strconv.ParseUint(f[6], 10, 64)
		if best == "" || metric < bestMetric {
			best, bestMetric = f[0], metric
		}
	}
	return best
}

func isVirtualInterface(name string) bool {
	for _, prefix := range []string{"lo", "docker", "veth", "br-", "virbr"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// readNetDev returns byte counters of iface, or of the busiest physical
// interface when iface is empty or missing.
func readNetDev(path, iface string) netCounters {
	data, err := os.ReadFile(path)
	if err != nil {
		return netCounters{}
	}
	var busiest netCounters
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		f := strings.Fields(rest)
		if !ok || len(f) < 9 {
			continue
		}
		c := netCounters{iface: strings.TrimSpace(name)}
		c.rx, _ = strconv.ParseUint(f[0], 10, 64)
		c.tx, _ = strconv.ParseUint(f[8], 10, 64)
		if c.iface == iface {
			return c
		}
		if !isVirtualInterface(c.iface) && c.rx+c.tx >= busiest.rx+busiest.tx {
			busiest = c
		}
	}
	return busiest
}

func readLoadavg(path string) [3]float64 {
	var load [3]float64
	data, err := os.ReadFile(path)
	if err != nil {
		return load
	}
	for i, field := range strings.Fields(string(data)) {
		if i >= 3 {
			break
		}
		load[i], _ = strconv.ParseFloat(field, 64)
	}
	return load
}

func readUptime(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	secs, _ := strconv.ParseFloat(fields[0], 64)
	return int64(secs)
}
