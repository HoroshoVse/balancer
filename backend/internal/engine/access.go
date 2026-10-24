package engine

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/balancer/backend/internal/models"
)

// CountryLookup finds the country of an address. loaded is false while the
// country database is incomplete: the answer is then unknown, not "no country".
type CountryLookup interface {
	Country(netip.Addr) (code string, loaded bool)
}

// GeoIP is set at startup; nil means countries cannot be told apart.
var GeoIP CountryLookup

// AccessDecision is the verdict on one address and why.
type AccessDecision struct {
	Allowed bool   `json:"allowed"`
	Country string `json:"country"`        // "" when unknown
	Reason  string `json:"reason"`         // machine-readable: see the constants below
	Rule    string `json:"rule,omitempty"` // the list entry that matched
}

// Reasons of a decision.
const (
	ReasonNoRules       = "no_rules"
	ReasonInternal      = "internal"
	ReasonAllowList     = "allow_list"
	ReasonDenyList      = "deny_list"
	ReasonCountryOK     = "country_allowed"
	ReasonCountryDenied = "country_denied"
	ReasonUnknown       = "unknown_country"
	ReasonNoDatabase    = "no_database"
)

// accessPolicy decides who may connect to one balancer. Order:
//  1. the always-allow list;
//  2. the always-deny list;
//  3. internal addresses (private, loopback, link-local, CGNAT): always allowed,
//     so health checks, the admin and the server's own tools are never locked out;
//  4. the country rule. An address that no registry lists has no country:
//     "only these countries" refuses it, "all except these" lets it in.
//
// While the country database is not loaded, the country rule lets everyone in
// rather than locking out every user because of a failed download.
type accessPolicy struct {
	mode      string
	countries map[string]bool
	allow     prefixSet
	deny      prefixSet
	blocks    *blockLog
	lastNoDB  atomic.Int64 // unix seconds of the last "database missing" warning
	name      string
}

func newAccessPolicy(lb models.LoadBalancer) *accessPolicy {
	if lb.AccessMode == models.AccessEveryone && lb.AccessAllow == "" && lb.AccessDeny == "" {
		return nil
	}
	p := &accessPolicy{mode: lb.AccessMode, countries: map[string]bool{}, name: lb.Name, blocks: newBlockLog()}
	for _, c := range models.ParseCountries(lb.AccessCountries) {
		p.countries[c] = true
	}
	allow, _ := models.ParsePrefixes(lb.AccessAllow) // validated when saved
	deny, _ := models.ParsePrefixes(lb.AccessDeny)
	p.allow, p.deny = newPrefixSet(allow), newPrefixSet(deny)
	return p
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isInternal(a netip.Addr) bool {
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() || cgnat.Contains(a)
}

func (p *accessPolicy) decide(a netip.Addr) AccessDecision {
	a = a.Unmap()
	if rule, ok := p.allow.match(a); ok {
		return AccessDecision{Allowed: true, Reason: ReasonAllowList, Rule: rule}
	}
	if rule, ok := p.deny.match(a); ok {
		return AccessDecision{Allowed: false, Reason: ReasonDenyList, Rule: rule}
	}
	if isInternal(a) {
		return AccessDecision{Allowed: true, Reason: ReasonInternal}
	}
	if p.mode == models.AccessEveryone {
		return AccessDecision{Allowed: true, Reason: ReasonNoRules}
	}
	var cc string
	loaded := false
	if GeoIP != nil {
		cc, loaded = GeoIP.Country(a)
	}
	if !loaded {
		if now := time.Now().Unix(); now-p.lastNoDB.Load() > 600 && p.lastNoDB.Swap(now) != now {
			Logger.WarnLB(p.name, fmt.Sprintf("%s: the country database is not loaded, so the country filter is letting everyone in. Update it in Settings.", p.name))
		}
		return AccessDecision{Allowed: true, Reason: ReasonNoDatabase}
	}
	listed := cc != "" && p.countries[cc]
	switch {
	case p.mode == models.AccessOnly && listed:
		return AccessDecision{Allowed: true, Country: cc, Reason: ReasonCountryOK}
	case p.mode == models.AccessOnly && cc == "":
		return AccessDecision{Allowed: false, Reason: ReasonUnknown}
	case p.mode == models.AccessOnly:
		return AccessDecision{Allowed: false, Country: cc, Reason: ReasonCountryDenied}
	case listed: // AccessExcept
		return AccessDecision{Allowed: false, Country: cc, Reason: ReasonCountryDenied}
	default:
		return AccessDecision{Allowed: true, Country: cc, Reason: ReasonCountryOK}
	}
}

// prefixSet answers "is this address in any of these subnets" in a few map
// lookups, however many subnets there are: a list of thousands is checked on
// every packet of a flood.
type prefixSet struct {
	lens4, lens6 []int
	m            map[netip.Prefix]bool
}

func newPrefixSet(prefixes []netip.Prefix) prefixSet {
	s := prefixSet{m: map[netip.Prefix]bool{}}
	seen4, seen6 := map[int]bool{}, map[int]bool{}
	for _, p := range prefixes {
		p = p.Masked()
		s.m[p] = true
		if p.Addr().Is4() && !seen4[p.Bits()] {
			seen4[p.Bits()] = true
			s.lens4 = append(s.lens4, p.Bits())
		} else if p.Addr().Is6() && !seen6[p.Bits()] {
			seen6[p.Bits()] = true
			s.lens6 = append(s.lens6, p.Bits())
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(s.lens4)))
	sort.Sort(sort.Reverse(sort.IntSlice(s.lens6)))
	return s
}

// match reports the most specific subnet containing the address.
func (s prefixSet) match(a netip.Addr) (string, bool) {
	if len(s.m) == 0 {
		return "", false
	}
	lens := s.lens6
	if a.Is4() {
		lens = s.lens4
	}
	for _, bits := range lens {
		if p := netip.PrefixFrom(a, bits).Masked(); s.m[p] {
			if p.IsSingleIP() {
				return p.Addr().String(), true
			}
			return p.String(), true
		}
	}
	return "", false
}

// blockLog counts refused connections and reports them once a minute: during
// an attack that is thousands of refusals a second, and one log line each
// would drown everything else.
type blockLog struct {
	mu    sync.Mutex
	count int
	by    map[string]int
}

func newBlockLog() *blockLog { return &blockLog{by: map[string]int{}} }

func (b *blockLog) note(key string) {
	b.mu.Lock()
	b.count++
	if _, ok := b.by[key]; ok || len(b.by) < 300 {
		b.by[key]++
	}
	b.mu.Unlock()
}

// flush writes the summary since the last flush, if there was anything.
func (b *blockLog) flush(lb string, interval time.Duration) {
	b.mu.Lock()
	count, by := b.count, b.by
	b.count, b.by = 0, map[string]int{}
	b.mu.Unlock()
	if count == 0 {
		return
	}
	type kv struct {
		k string
		n int
	}
	var top []kv
	for k, n := range by {
		top = append(top, kv{k, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n || top[i].n == top[j].n && top[i].k < top[j].k })
	if len(top) > 5 {
		top = top[:5]
	}
	parts := make([]string, len(top))
	for i, e := range top {
		parts[i] = fmt.Sprintf("%s ×%d", e.k, e.n)
	}
	Logger.WarnLB(lb, fmt.Sprintf("%s: %d connections refused by the access rules in the last %s; most from: %s", lb, count, interval, strings.Join(parts, ", ")))
}

func (b *blockLog) run(done <-chan struct{}, lb string) {
	const interval = time.Minute
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			b.flush(lb, interval)
			return
		case <-t.C:
			b.flush(lb, interval)
		}
	}
}

func addrOf(a net.Addr) (netip.Addr, bool) {
	switch v := a.(type) {
	case *net.TCPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	case *net.UDPAddr:
		ip, ok := netip.AddrFromSlice(v.IP)
		return ip.Unmap(), ok
	case nil:
		return netip.Addr{}, false
	}
	ap, err := netip.ParseAddrPort(a.String())
	return ap.Addr().Unmap(), err == nil
}

// admit reports whether a client at addr may be served, and counts the refusals.
func (l *LoadBalancerInstance) admit(addr net.Addr) bool {
	p := l.access
	if p == nil {
		return true
	}
	a, ok := addrOf(addr)
	if !ok {
		return true
	}
	d := p.decide(a)
	if d.Allowed {
		return true
	}
	key := d.Country
	switch {
	case d.Reason == ReasonDenyList:
		key = "deny list"
	case key == "":
		key = "unknown country"
	}
	// Refusals by the deny list have no country: they are counted under "list"
	where := d.Country
	if d.Reason == ReasonDenyList {
		where = "list"
	}
	Metrics.RecordBlocked(l.Config.ID, where, d.Reason)
	p.blocks.note(key)
	return false
}

// CheckAccess says what the balancer would do with a connection from addr.
// ok is false when no such balancer is running.
func (e *Engine) CheckAccess(lbID uint, addr netip.Addr) (AccessDecision, bool) {
	e.mu.RLock()
	inst := e.activeBalancers[lbID]
	e.mu.RUnlock()
	if inst == nil {
		return AccessDecision{}, false
	}
	if inst.access == nil {
		return AccessDecision{Allowed: true, Reason: ReasonNoRules}, true
	}
	return inst.access.decide(addr), true
}

// aclListener drops connections from refused addresses right at accept, before
// TLS or HTTP parsing spend any effort on them.
type aclListener struct {
	net.Listener
	l *LoadBalancerInstance
}

func (a *aclListener) Accept() (net.Conn, error) {
	for {
		c, err := a.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if a.l.admit(c.RemoteAddr()) {
			return c, nil
		}
		c.Close()
	}
}

// aclPacketConn drops datagrams (QUIC) from refused addresses.
type aclPacketConn struct {
	net.PacketConn
	l *LoadBalancerInstance
}

func (a *aclPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := a.PacketConn.ReadFrom(p)
		if err != nil || a.l.admit(addr) {
			return n, addr, err
		}
	}
}
