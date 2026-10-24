package geo

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Source is one registry's delegation file. Name must be the registry name
// the file declares in its header.
type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// DefaultSources are the five Regional Internet Registries, which allocate
// every public address and publish where each block went. Files are fetched
// over HTTPS and checked against the checksum the same registry publishes.
var DefaultSources = []Source{
	{"ripencc", "https://ftp.ripe.net/pub/stats/ripencc/delegated-ripencc-extended-latest"},
	{"arin", "https://ftp.arin.net/pub/stats/arin/delegated-arin-extended-latest"},
	{"apnic", "https://ftp.apnic.net/stats/apnic/delegated-apnic-extended-latest"},
	{"lacnic", "https://ftp.lacnic.net/pub/stats/lacnic/delegated-lacnic-extended-latest"},
	{"afrinic", "https://ftp.afrinic.net/pub/stats/afrinic/delegated-afrinic-extended-latest"},
}

// ErrBusy is returned when an update is already running.
var ErrBusy = errors.New("an update is already running")

var md5Re = regexp.MustCompile(`(?i)\b[0-9a-f]{32}\b`)

// SourceStatus describes one registry's data.
type SourceStatus struct {
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Present     bool      `json:"present"`
	Downloading bool      `json:"downloading,omitempty"` // being fetched right now
	Ranges      int       `json:"ranges"`
	DataDate    string    `json:"data_date,omitempty"`  // the registry's own date, e.g. 20261023
	UpdatedAt   time.Time `json:"updated_at,omitempty"` // last time the data was confirmed current
	Error       string    `json:"error,omitempty"`      // why the last attempt failed
}

// Status is the state of the whole database.
type Status struct {
	Loaded      bool           `json:"loaded"`   // every registry is present, lookups are meaningful
	Updating    bool           `json:"updating"` // a download is running
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
	Countries   int            `json:"countries"`
	Ranges      int            `json:"ranges"`
	Sources     []SourceStatus `json:"sources"`
	LastAttempt time.Time      `json:"last_attempt,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	NextAttempt time.Time      `json:"next_attempt,omitempty"` // earliest automatic retry after a failure
}

type meta struct {
	MD5       string    `json:"md5"`
	Size      int64     `json:"size"`
	Serial    string    `json:"serial"`
	FetchedAt time.Time `json:"fetched_at"`
	CheckedAt time.Time `json:"checked_at"`
}

type snapshot struct {
	db       *DB
	complete bool
}

// Manager keeps the registry files on disk, the lookup index in memory, and
// refreshes both on request or on a schedule.
type Manager struct {
	dir     string
	sources []Source
	client  *http.Client
	log     func(level, msg string)
	maxSize int64 // per file
	minV4   int   // fewest IPv4 ranges a believable file has
	now     func() time.Time

	mu          sync.Mutex
	files       map[string]*parsed
	metas       map[string]meta
	errs        map[string]string
	lastAttempt time.Time
	lastErr     string
	nextAttempt time.Time
	updating    bool
	busy        map[string]bool // registries being fetched right now

	cur atomic.Pointer[snapshot]
}

// NewManager stores its files in dir. log may be nil.
func NewManager(dir string, sources []Source, log func(level, msg string)) *Manager {
	m := &Manager{
		dir: dir, sources: sources, log: log,
		maxSize: 64 << 20, minV4: 500, now: time.Now,
		files: map[string]*parsed{}, metas: map[string]meta{}, errs: map[string]string{}, busy: map[string]bool{},
	}
	m.client = &http.Client{
		// No overall timeout: the registries serve ~50 MB slowly and a stalled
		// transfer is caught by stallTimeout instead
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing redirect to %s", req.URL.Scheme)
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return m
}

func (m *Manager) logf(level, format string, args ...any) {
	if m.log != nil {
		m.log(level, fmt.Sprintf(format, args...))
	}
}

// Country returns the country of an address. loaded is false while the data
// is incomplete (nothing downloaded yet, or a registry is missing); code is
// "" when the data is complete but no registry lists the address.
func (m *Manager) Country(a netip.Addr) (code string, loaded bool) {
	s := m.cur.Load()
	if s == nil || !s.complete {
		return "", false
	}
	return s.db.Lookup(a), true
}

// Countries lists the country codes present in the data.
func (m *Manager) Countries() []string {
	if s := m.cur.Load(); s != nil {
		return s.db.Countries()
	}
	return nil
}

// Database returns the current lookup index, or nil when nothing is loaded.
func (m *Manager) Database() *DB {
	if s := m.cur.Load(); s != nil {
		return s.db
	}
	return nil
}

// Load reads the files already on disk; it does not touch the network.
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, src := range m.sources {
		f, err := os.Open(filepath.Join(m.dir, src.Name+".txt"))
		if err != nil {
			if !os.IsNotExist(err) {
				m.errs[src.Name] = err.Error()
			}
			continue
		}
		p, err := parseDelegated(f)
		f.Close()
		if err == nil && p.registry != src.Name {
			err = fmt.Errorf("the file is for %q, expected %q", p.registry, src.Name)
		}
		if err != nil {
			m.errs[src.Name] = "saved file is unusable: " + err.Error()
			continue
		}
		m.files[src.Name] = p
		if data, err := os.ReadFile(filepath.Join(m.dir, src.Name+".json")); err == nil {
			var mt meta
			if json.Unmarshal(data, &mt) == nil {
				m.metas[src.Name] = mt
			}
		}
		if mt := m.metas[src.Name]; mt.CheckedAt.IsZero() {
			if st, err := os.Stat(filepath.Join(m.dir, src.Name+".txt")); err == nil {
				mt.FetchedAt, mt.CheckedAt, mt.Serial = st.ModTime(), st.ModTime(), p.serial
				m.metas[src.Name] = mt
			}
		}
	}
	m.rebuildLocked()
	return nil
}

func (m *Manager) rebuildLocked() {
	var parts []*parsed
	for _, src := range m.sources {
		if p := m.files[src.Name]; p != nil {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		m.cur.Store(nil)
		return
	}
	m.cur.Store(&snapshot{db: build(parts), complete: len(parts) == len(m.sources)})
}

// Update downloads every registry whose data changed and rebuilds the index.
// A registry that fails keeps its previous data; the others are still updated.
func (m *Manager) Update(ctx context.Context) error {
	m.mu.Lock()
	if m.updating {
		m.mu.Unlock()
		return ErrBusy
	}
	m.updating = true
	m.lastAttempt = m.now()
	m.mu.Unlock()

	// The registries are slow and independent: fetch them side by side
	errs := make([]error, len(m.sources))
	var wg sync.WaitGroup
	for i, src := range m.sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.mu.Lock()
			m.busy[src.Name] = true
			m.mu.Unlock()
			changed, err := m.fetch(ctx, src)
			m.mu.Lock()
			defer m.mu.Unlock()
			delete(m.busy, src.Name)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", src.Name, err)
				m.errs[src.Name] = err.Error()
				m.logf("WARN", "Geo database: %s was not updated: %v", src.Name, err)
				return
			}
			delete(m.errs, src.Name)
			if changed {
				m.logf("INFO", "Geo database: %s updated (%d IPv4 and %d IPv6 ranges)", src.Name, len(m.files[src.Name].v4), len(m.files[src.Name].v6))
			}
		}()
	}
	wg.Wait()
	var failed []string
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.updating = false
	m.rebuildLocked()
	if len(failed) > 0 {
		m.lastErr = strings.Join(failed, "; ")
		m.nextAttempt = m.now().Add(time.Hour)
		return errors.New(m.lastErr)
	}
	m.lastErr, m.nextAttempt = "", time.Time{}
	return nil
}

// fetch brings one registry up to date. The small checksum file tells whether
// the big one changed, so an unchanged registry costs a few hundred bytes.
func (m *Manager) fetch(ctx context.Context, src Source) (changed bool, err error) {
	if !strings.HasPrefix(src.URL, "https://") {
		return false, errors.New("only HTTPS sources are allowed")
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return false, err
	}
	want, err := m.fetchChecksum(ctx, src.URL+".md5")
	if err != nil {
		return false, fmt.Errorf("checksum: %w", err)
	}
	m.mu.Lock()
	mt, haveMeta := m.metas[src.Name]
	old := m.files[src.Name]
	m.mu.Unlock()
	if old != nil && haveMeta && mt.MD5 == want {
		mt.CheckedAt = m.now()
		m.saveMeta(src.Name, mt)
		return false, nil
	}

	tmp, err := os.CreateTemp(m.dir, ".download-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	sum := md5.New()
	size, err := m.download(ctx, src.URL, io.MultiWriter(tmp, sum))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return false, fmt.Errorf("checksum mismatch: downloaded %s, the registry publishes %s", got, want)
	}

	f, err := os.Open(tmp.Name())
	if err != nil {
		return false, err
	}
	p, err := parseDelegated(f)
	f.Close()
	if err != nil {
		return false, fmt.Errorf("unreadable file: %w", err)
	}
	if p.registry != src.Name {
		return false, fmt.Errorf("the file is for %q, expected %q", p.registry, src.Name)
	}
	if len(p.v4) < m.minV4 {
		return false, fmt.Errorf("only %d IPv4 ranges, the file looks incomplete", len(p.v4))
	}
	if old != nil && len(p.v4) < len(old.v4)/2 {
		return false, fmt.Errorf("%d IPv4 ranges against %d before: refusing a file less than half the size", len(p.v4), len(old.v4))
	}

	final := filepath.Join(m.dir, src.Name+".txt")
	if err := os.Rename(tmp.Name(), final); err != nil {
		return false, err
	}
	now := m.now()
	mt = meta{MD5: want, Size: size, Serial: p.serial, FetchedAt: now, CheckedAt: now}
	m.saveMeta(src.Name, mt)
	m.mu.Lock()
	m.files[src.Name] = p
	m.mu.Unlock()
	return true, nil
}

func (m *Manager) saveMeta(name string, mt meta) {
	m.mu.Lock()
	m.metas[name] = mt
	m.mu.Unlock()
	data, _ := json.Marshal(mt)
	tmp := filepath.Join(m.dir, "."+name+".json.tmp")
	if os.WriteFile(tmp, data, 0o644) == nil {
		os.Rename(tmp, filepath.Join(m.dir, name+".json"))
	}
}

func (m *Manager) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "balancer-geo/1")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	return resp, nil
}

func (m *Manager) fetchChecksum(ctx context.Context, url string) (string, error) {
	resp, err := m.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	sum := md5Re.FindString(string(body))
	if sum == "" {
		return "", errors.New("no checksum in the answer")
	}
	return strings.ToLower(sum), nil
}

// stallTimeout is how long a transfer may deliver nothing before it is abandoned.
const stallTimeout = 90 * time.Second

func (m *Manager) download(ctx context.Context, url string, w io.Writer) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stalled := time.AfterFunc(stallTimeout, cancel)
	defer stalled.Stop()

	resp, err := m.get(ctx, url)
	if err != nil {
		return 0, stallErr(ctx, err)
	}
	defer resp.Body.Close()
	body := io.LimitReader(resp.Body, m.maxSize+1)
	n, err := io.Copy(w, readFunc(func(p []byte) (int, error) {
		k, err := body.Read(p)
		if k > 0 {
			stalled.Reset(stallTimeout)
		}
		return k, err
	}))
	if err != nil {
		return n, stallErr(ctx, err)
	}
	if n > m.maxSize {
		return n, fmt.Errorf("file is larger than %d MB", m.maxSize>>20)
	}
	return n, nil
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func stallErr(ctx context.Context, err error) error {
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return fmt.Errorf("no data for %v, the transfer was abandoned", stallTimeout)
	}
	return err
}

// Status reports the state of every registry.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Updating: m.updating, LastAttempt: m.lastAttempt, LastError: m.lastErr, NextAttempt: m.nextAttempt}
	complete := true
	for _, src := range m.sources {
		ss := SourceStatus{Name: src.Name, URL: src.URL, Error: m.errs[src.Name], Downloading: m.busy[src.Name]}
		if p := m.files[src.Name]; p != nil {
			ss.Present, ss.Ranges, ss.DataDate = true, len(p.v4)+len(p.v6), p.serial
			ss.UpdatedAt = m.metas[src.Name].CheckedAt
			st.Ranges += ss.Ranges
			if st.UpdatedAt.IsZero() || ss.UpdatedAt.Before(st.UpdatedAt) {
				st.UpdatedAt = ss.UpdatedAt
			}
		} else {
			complete = false
		}
		st.Sources = append(st.Sources, ss)
	}
	st.Loaded = complete && len(m.sources) > 0
	if !st.Loaded {
		st.UpdatedAt = time.Time{}
	}
	if s := m.cur.Load(); s != nil {
		st.Countries = len(s.db.Countries())
	}
	return st
}

// Run keeps the data fresh until ctx ends. Each minute it checks whether an
// update is due; inUse says whether any balancer needs the data at all, so
// installations that don't filter by country never download it.
//
// Updates run when the data is missing (even with auto-update off, since a
// rule that cannot be applied is worse), or when it is older than every.
// After a failure it waits an hour before trying again.
func (m *Manager) Run(ctx context.Context, schedule func() (auto bool, every time.Duration), inUse func() bool) {
	m.maybeUpdate(ctx, schedule, inUse)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.maybeUpdate(ctx, schedule, inUse)
		}
	}
}

func (m *Manager) maybeUpdate(ctx context.Context, schedule func() (bool, time.Duration), inUse func() bool) {
	m.mu.Lock()
	busy, wait := m.updating, m.now().Before(m.nextAttempt)
	m.mu.Unlock()
	if busy || wait || !inUse() {
		return
	}
	st := m.Status()
	auto, every := schedule()
	if !st.Loaded || (auto && m.now().Sub(st.UpdatedAt) >= every) {
		if err := m.Update(ctx); err != nil && !errors.Is(err, ErrBusy) {
			m.logf("WARN", "Geo database update failed, next try in an hour: %v", err)
		}
	}
}
