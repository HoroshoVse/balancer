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
	mu      sync.Mutex
	count   int
	by      map[string]int
	queries map[string]*int // "domain\x00TYPE" of refused DNS queries -> how many
}

func newBlockLog() *blockLog { return &blockLog{by: map[string]int{}, queries: map[string]*int{}} }

// noteQuery counts a refused DNS query by what it asked for. An asked-for name
// that is already known costs no allocation.
func (b *blockLog) noteQuery(label []byte) {
	b.mu.Lock()
	if n, ok := b.queries[string(label)]; ok {
		*n++
	} else if len(b.queries) < 300 {
		one := 1
		b.queries[string(label)] = &one
	}
	b.mu.Unlock()
}

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
	count, by, queries := b.count, b.by, b.queries
	b.count, b.by, b.queries = 0, map[string]int{}, map[string]*int{}
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
	msg := fmt.Sprintf("%s: %d connections refused by the access rules in the last %s; most from: %s", lb, count, interval, strings.Join(parts, ", "))
	if asked := topQueries(queries, 3); asked != "" {
		msg += "; domains: " + asked
	}
	Logger.WarnLB(lb, msg)
}

// topQueries lists the most asked-for names as "example.com TXT ×4100, ...".
func topQueries(queries map[string]*int, n int) string {
	type kv struct {
		k string
		n int
	}
	var top []kv
	for k, c := range queries {
		top = append(top, kv{k, *c})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n || top[i].n == top[j].n && top[i].k < top[j].k })
	if len(top) > n {
		top = top[:n]
	}
	parts := make([]string, len(top))
	for i, e := range top {
		domain, qtype := splitLabel(e.k)
		parts[i] = fmt.Sprintf("%s %s ×%d", domain, qtype, e.n)
	}
	return strings.Join(parts, ", ")
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

// admit reports whether a client at addr may be served. Every client is counted
// for the "who is hitting this balancer" statistic, rules or no rules; the
// refusals are counted again as such.
func (l *LoadBalancerInstance) admit(addr net.Addr) bool { return l.admitPacket(addr, nil) }

// admitPacket is admit for a datagram: when it looks like a DNS query, the name
// it asked for is counted too, with the address that asked and whether it was
// served, so "who asks for what" can be told apart from the attack.
func (l *LoadBalancerInstance) admitPacket(addr net.Addr, payload []byte) bool {
	a, ok := addrOf(addr)
	if !ok {
		return true
	}
	p := l.access
	d := AccessDecision{Allowed: true}
	if p != nil {
		d = p.decide(a)
	}
	l.clients.record(a, !d.Allowed)
	var label [maxQueryLabel]byte
	n, isQuery := 0, false
	if payload != nil {
		if n, isQuery = queryLabel(payload, &label); isQuery {
			l.domains.recordFrom(label[:n], a, !d.Allowed)
		}
	}
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
	if isQuery {
		p.blocks.noteQuery(label[:n])
	}
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
