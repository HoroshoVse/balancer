package geo

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// registryFile renders a delegation file for the given registry.
func registryFile(registry string, records ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "2|%s|1792792799|%d|19700101|20261023|+0000\n", registry, len(records))
	fmt.Fprintf(&b, "%s|*|ipv4|*|%d|summary\n", registry, len(records))
	b.WriteString("# a comment\n")
	for _, r := range records {
		b.WriteString(registry + "|" + r + "\n")
	}
	return b.String()
}

func TestParseDelegated(t *testing.T) {
	data := registryFile("ripencc",
		"RU|ipv4|5.0.0.0|65536|20200101|allocated|abc",
		"DE|ipv4|5.1.0.0|768|20200101|assigned|abc", // not a power of two
		"FR|ipv4|5.2.0.0|256|20200101|available",    // not in use: skipped
		"ZZ|ipv4|5.3.0.0|256|20200101|allocated",    // unknown country: skipped
		"DE|ipv6|2001:db8::|32|20200101|allocated|abc",
		"|ipv4|5.4.0.0|256||reserved",
	)
	p, err := parseDelegated(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if p.registry != "ripencc" || p.serial != "20261023" {
		t.Fatalf("header: %q %q", p.registry, p.serial)
	}
	db := build([]*parsed{p})
	cases := map[string]string{
		"5.0.0.0": "RU", "5.0.255.255": "RU", // both ends of a /16
		"5.1.0.0": "DE", "5.1.2.255": "DE", // 768 addresses: the last is .2.255
		"5.1.3.0":       "", // one past it
		"5.2.0.1":       "", // available
		"5.3.0.1":       "", // ZZ
		"4.255.255.255": "",
		"2001:db8::1":   "DE", "2001:db8:ffff:ffff::": "DE", "2001:db9::": "",
		"::ffff:5.0.0.1": "RU", // IPv4 in IPv6 notation
	}
	for ip, want := range cases {
		if got := db.Lookup(addr(ip)); got != want {
			t.Errorf("%s = %q, want %q", ip, got, want)
		}
	}
	if got := strings.Join(db.Countries(), ","); got != "DE,RU" {
		t.Errorf("countries %q", got)
	}
}

func TestParseRejectsNonRegistryData(t *testing.T) {
	for name, data := range map[string]string{
		"html":      "<html><body>Not found</body></html>\n",
		"no header": "ripencc|RU|ipv4|5.0.0.0|256|20200101|allocated\n",
		"bad addr":  "2|ripencc|1|1|1|1|+0\nripencc|RU|ipv4|nope|256|20200101|allocated\n",
		"overflow":  "2|ripencc|1|1|1|1|+0\nripencc|RU|ipv4|255.255.255.0|512|20200101|allocated\n",
	} {
		if _, err := parseDelegated(strings.NewReader(data)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestBuildMergesAdjacentAndTrimsOverlaps(t *testing.T) {
	a := &parsed{registry: "a", v4: []v4Range{
		{start: 100, end: 199, cc: mustCC("RU")},
		{start: 200, end: 299, cc: mustCC("RU")}, // adjacent, same country: merged
		{start: 300, end: 399, cc: mustCC("DE")},
	}}
	b := &parsed{registry: "b", v4: []v4Range{
		{start: 350, end: 450, cc: mustCC("FR")}, // overlaps DE: only the part after 399 is FR
		{start: 380, end: 390, cc: mustCC("US")}, // fully inside: dropped
	}}
	db := build([]*parsed{a, b})
	for v, want := range map[uint32]string{99: "", 100: "RU", 299: "RU", 300: "DE", 399: "DE", 400: "FR", 450: "FR", 451: ""} {
		got := db.Lookup(netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}))
		if got != want {
			t.Errorf("%d = %q, want %q", v, got, want)
		}
	}
	if n, _ := db.Ranges(); n != 3 { // RU 100-299, DE 300-399, FR 400-450
		t.Errorf("%d ranges after merging, want 3", n)
	}
}

func mustCC(s string) uint16 {
	c, ok := ccCode(s)
	if !ok {
		panic(s)
	}
	return c
}

// ---- manager ----

type fakeRegistries struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	bodies map[string]string // registry -> file
	badSum map[string]bool
	hits   map[string]int
	hold   chan struct{} // when set, file downloads wait for it
}

func newFakeRegistries(t *testing.T, sources []Source) (*fakeRegistries, []Source) {
	f := &fakeRegistries{t: t, bodies: map[string]string{}, badSum: map[string]bool{}, hits: map[string]int{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.URL.Path]++
		hold := f.hold
		f.mu.Unlock()
		name, isSum := strings.TrimPrefix(r.URL.Path, "/delegated-"), false
		if strings.HasSuffix(name, ".md5") {
			name, isSum = strings.TrimSuffix(name, ".md5"), true
		}
		f.mu.Lock()
		body, ok := f.bodies[name]
		bad := f.badSum[name]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if isSum {
			sum := md5.Sum([]byte(body))
			h := hex.EncodeToString(sum[:])
			if bad {
				h = strings.Repeat("0", 32)
			}
			fmt.Fprintf(w, "MD5 (delegated-%s) = %s\n", name, h)
			return
		}
		if hold != nil {
			<-hold
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(f.srv.Close)
	out := make([]Source, len(sources))
	for i, s := range sources {
		out[i] = Source{Name: s.Name, URL: f.srv.URL + "/delegated-" + s.Name}
	}
	return f, out
}

func (f *fakeRegistries) set(name, body string) {
	f.mu.Lock()
	f.bodies[name] = body
	f.mu.Unlock()
}

func (f *fakeRegistries) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

var twoSources = []Source{{Name: "ripencc"}, {Name: "arin"}}

func goodFiles(f *fakeRegistries) {
	f.set("ripencc", registryFile("ripencc", "RU|ipv4|5.0.0.0|65536|20200101|allocated", "DE|ipv4|5.1.0.0|65536|20200101|allocated", "RU|ipv6|2a00::|12|20200101|allocated"))
	f.set("arin", registryFile("arin", "US|ipv4|8.0.0.0|16777216|20200101|allocated", "CA|ipv4|24.0.0.0|65536|20200101|assigned"))
}

func newTestManager(t *testing.T, f *fakeRegistries, sources []Source) *Manager {
	m := NewManager(t.TempDir(), sources, nil)
	m.client = f.srv.Client()
	m.minV4 = 1
	return m
}

func TestManagerDownloadsVerifiesAndPersists(t *testing.T) {
	f, srcs := newFakeRegistries(t, twoSources)
	goodFiles(f)
	m := newTestManager(t, f, srcs)

	if code, loaded := m.Country(addr("5.0.0.1")); loaded || code != "" {
		t.Fatal("answered before any data was loaded")
	}
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]string{"5.0.1.1": "RU", "8.8.8.8": "US", "24.0.0.1": "CA", "5.1.0.1": "DE", "2a00::1": "RU", "9.9.9.9": ""} {
		if code, loaded := m.Country(addr(ip)); !loaded || code != want {
			t.Errorf("%s = %q (loaded %v), want %q", ip, code, loaded, want)
		}
	}
	st := m.Status()
	if !st.Loaded || st.Countries != 4 || len(st.Sources) != 2 || st.Sources[0].DataDate != "20261023" || st.UpdatedAt.IsZero() || st.LastError != "" {
		t.Fatalf("status %+v", st)
	}

	// A new process finds the data on disk without the network
	m2 := NewManager(m.dir, srcs, nil)
	m2.minV4 = 1
	if err := m2.Load(); err != nil {
		t.Fatal(err)
	}
	if code, loaded := m2.Country(addr("8.8.8.8")); !loaded || code != "US" {
		t.Fatalf("after reload: %q %v", code, loaded)
	}
	if !m2.Status().Loaded || m2.Status().UpdatedAt.IsZero() {
		t.Fatalf("status after reload %+v", m2.Status())
	}
}

func TestManagerSkipsUnchangedFiles(t *testing.T) {
	f, srcs := newFakeRegistries(t, twoSources)
	goodFiles(f)
	m := newTestManager(t, f, srcs)
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.count("/delegated-ripencc"); n != 1 {
		t.Fatalf("downloaded the unchanged file %d times, want once", n)
	}
	if n := f.count("/delegated-ripencc.md5"); n != 2 {
		t.Fatalf("checked the checksum %d times, want on every update", n)
	}
	// ...but fetches it as soon as the registry publishes a new one
	f.set("ripencc", registryFile("ripencc", "RU|ipv4|5.0.0.0|65536|20200101|allocated", "FR|ipv4|5.1.0.0|65536|20200101|allocated"))
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if code, _ := m.Country(addr("5.1.0.1")); code != "FR" {
		t.Fatalf("new data not picked up: %q", code)
	}
}

func TestManagerRejectsWhatItCannotTrust(t *testing.T) {
	cases := map[string]func(f *fakeRegistries){
		"checksum mismatch": func(f *fakeRegistries) { f.badSum["ripencc"] = true },
		"another registry": func(f *fakeRegistries) {
			f.set("ripencc", registryFile("arin", "US|ipv4|8.0.0.0|256|20200101|allocated"))
		},
		"not a registry file": func(f *fakeRegistries) { f.set("ripencc", "<html>captive portal</html>") },
		"missing file":        func(f *fakeRegistries) { delete(f.bodies, "ripencc") },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			f, srcs := newFakeRegistries(t, twoSources)
			goodFiles(f)
			spoil(f)
			m := newTestManager(t, f, srcs)
			if err := m.Update(context.Background()); err == nil {
				t.Fatal("update reported success")
			}
			st := m.Status()
			if st.Loaded || st.Sources[0].Present || st.Sources[0].Error == "" || st.LastError == "" || st.NextAttempt.IsZero() {
				t.Fatalf("status %+v", st)
			}
			if !st.Sources[1].Present {
				t.Fatal("the healthy registry was not kept")
			}
			if _, err := os.Stat(filepath.Join(m.dir, "ripencc.txt")); err == nil {
				t.Fatal("bad file was saved")
			}
			// Incomplete data must not answer: a missing registry would read as "country unknown"
			if _, loaded := m.Country(addr("8.8.8.8")); loaded {
				t.Fatal("answered from incomplete data")
			}
		})
	}
}

func TestManagerRejectsHTTPSources(t *testing.T) {
	m := NewManager(t.TempDir(), []Source{{Name: "ripencc", URL: "http://ftp.ripe.net/x"}}, nil)
	if err := m.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerKeepsOldDataWhenANewFileIsTruncated(t *testing.T) {
	f, srcs := newFakeRegistries(t, twoSources)
	var recs []string
	for i := 0; i < 20; i++ {
		recs = append(recs, fmt.Sprintf("RU|ipv4|5.%d.0.0|256|20200101|allocated", i))
	}
	f.set("ripencc", registryFile("ripencc", recs...))
	f.set("arin", registryFile("arin", "US|ipv4|8.0.0.0|256|20200101|allocated"))
	m := newTestManager(t, f, srcs)
	if err := m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}

	f.set("ripencc", registryFile("ripencc", recs[:5]...)) // a quarter of it: a cut-off download that happens to parse
	if err := m.Update(context.Background()); err == nil || !strings.Contains(err.Error(), "half") {
		t.Fatalf("err = %v", err)
	}
	if code, loaded := m.Country(addr("5.15.0.1")); !loaded || code != "RU" {
		t.Fatalf("old data lost: %q %v", code, loaded)
	}
	st := m.Status()
	if !st.Loaded || st.Sources[0].Error == "" {
		t.Fatalf("status %+v", st)
	}
}

func TestManagerUpdatesOneAtATime(t *testing.T) {
	f, srcs := newFakeRegistries(t, twoSources)
	goodFiles(f)
	f.hold = make(chan struct{})
	m := newTestManager(t, f, srcs)

	done := make(chan error, 1)
	go func() { done <- m.Update(context.Background()) }()
	deadline := time.Now().Add(3 * time.Second)
	for !m.Status().Updating {
		if time.Now().After(deadline) {
			t.Fatal("update never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := m.Update(context.Background()); err != ErrBusy {
		t.Fatalf("second update = %v, want ErrBusy", err)
	}
	close(f.hold)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.Status().Updating {
		t.Fatal("still marked as updating")
	}
}

func TestScheduling(t *testing.T) {
	f, srcs := newFakeRegistries(t, twoSources)
	goodFiles(f)
	m := newTestManager(t, f, srcs)
	clock := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return clock }
	ctx := context.Background()
	auto := func(on bool) func() (bool, time.Duration) {
		return func() (bool, time.Duration) { return on, 24 * time.Hour }
	}
	used := func(on bool) func() bool { return func() bool { return on } }
	hits := func() int { return f.count("/delegated-ripencc.md5") }

	m.maybeUpdate(ctx, auto(true), used(false))
	if hits() != 0 {
		t.Fatal("downloaded although no balancer uses country rules")
	}
	m.maybeUpdate(ctx, auto(false), used(true))
	if hits() != 1 || !m.Status().Loaded {
		t.Fatal("did not fetch the missing data for a balancer that needs it, even with auto-update off")
	}

	clock = clock.Add(23 * time.Hour)
	m.maybeUpdate(ctx, auto(true), used(true))
	if hits() != 1 {
		t.Fatal("updated before the interval passed")
	}
	clock = clock.Add(2 * time.Hour)
	m.maybeUpdate(ctx, auto(false), used(true))
	if hits() != 1 {
		t.Fatal("updated with auto-update off")
	}
	m.maybeUpdate(ctx, auto(true), used(true))
	if hits() != 2 {
		t.Fatal("did not update an outdated database")
	}

	// After a failure it waits an hour
	f.badSum["ripencc"] = true
	f.set("ripencc", registryFile("ripencc", "RU|ipv4|5.0.0.0|65536|20200101|allocated", "FR|ipv4|5.1.0.0|65536|20200101|allocated"))
	clock = clock.Add(48 * time.Hour)
	m.maybeUpdate(ctx, auto(true), used(true))
	failed := hits()
	m.maybeUpdate(ctx, auto(true), used(true))
	clock = clock.Add(59 * time.Minute)
	m.maybeUpdate(ctx, auto(true), used(true))
	if hits() != failed {
		t.Fatal("retried within the hour")
	}
	clock = clock.Add(2 * time.Minute)
	m.maybeUpdate(ctx, auto(true), used(true))
	if hits() != failed+1 {
		t.Fatal("did not retry after the hour")
	}
}
