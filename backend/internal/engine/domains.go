package engine

import (
	"encoding/binary"
	"hash/maphash"
	"net/netip"
	"sort"
	"strconv"
	"time"
)

// What did the refused queries ask for? A flood against a DNS balancer is
// usually one name made unique with random subdomains (xk3j9.example.com, so no
// cache can answer it), and seeing "example.com TXT" is what tells the owner
// what the attack is. The first question of a refused datagram is read, and the
// name is cut down to its registrable-looking tail so a random-subdomain flood
// collapses into one row.

// maxQueryLabel fits "domain\x00TYPE": a name is at most 253 characters, a
// type name at most "TYPE65535".
const maxQueryLabel = 253 + 1 + 9

// queryLabel reads the first question of a DNS query in p and writes
// "domain\x00TYPE" to out: the domain in lower case, cut to its registrable-looking
// tail, and the query type by name. ok is false for anything that is not a
// well-formed query, which is most of what a random flood of garbage looks like.
// It does not allocate.
func queryLabel(p []byte, out *[maxQueryLabel]byte) (n int, ok bool) {
	const header = 12
	// The header, a name of at least the root byte, and the type and class
	if len(p) < header+1+4 || p[2]&0x80 != 0 || binary.BigEndian.Uint16(p[4:]) == 0 {
		return 0, false
	}
	i, w, wire := header, 0, 1 // read position, written length, length of the name on the wire (its zero byte included)
	var dots [3]int            // where the last three dots are: dots[2] is the last one
	nd := 0
	for {
		if i >= len(p) {
			return 0, false
		}
		l := int(p[i])
		i++
		if l == 0 {
			break
		}
		// A length above 63 is a compression pointer (not allowed in the first
		// question) or a reserved form; a label must also fit in the packet
		if l > 63 || i+l > len(p) {
			return 0, false
		}
		if wire += 1 + l; wire > 255 {
			return 0, false
		}
		if w > 0 {
			dots[0], dots[1], dots[2] = dots[1], dots[2], w
			nd++
			out[w] = '.'
			w++
		}
		for _, c := range p[i : i+l] {
			// Printable ASCII only, and no dot inside a label: whatever else is not a
			// host name, and it must not reach the log or the page
			if c <= ' ' || c > '~' || c == '.' {
				return 0, false
			}
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			out[w] = c
			w++
		}
		i += l
	}
	if i+4 > len(p) {
		return 0, false
	}
	qtype := binary.BigEndian.Uint16(p[i:])
	switch binary.BigEndian.Uint16(p[i+2:]) { // IN, CH, HS, NONE, ANY: anything else is not DNS
	case 1, 3, 4, 254, 255:
	default:
		return 0, false
	}

	start := 0
	switch {
	case w == 0:
		out[0] = '.' // the root
		w = 1
	case nd >= 2: // three labels or more
		start = dots[1] + 1 // the last two
		tld, second := out[dots[2]+1:w], out[dots[1]+1:dots[2]]
		if len(tld) == 2 && genericSecondLevel(second) {
			start = 0 // foo.co.uk: three labels
			if nd >= 3 {
				start = dots[0] + 1
			}
		}
	}
	n = copy(out[:], out[start:w])
	out[n] = 0
	n++
	// appended in place: the array has room for the longest type name
	return n + len(appendQType(out[n:n], qtype)), true
}

// genericSecondLevel names the second-level labels under a country code that are
// not owned by one organisation: in foo.co.uk the name is foo.co.uk, not co.uk.
func genericSecondLevel(b []byte) bool {
	switch string(b) {
	case "co", "com", "net", "org", "gov", "edu", "ac", "or":
		return true
	}
	return false
}

func appendQType(dst []byte, t uint16) []byte {
	switch t {
	case 1:
		return append(dst, "A"...)
	case 2:
		return append(dst, "NS"...)
	case 5:
		return append(dst, "CNAME"...)
	case 6:
		return append(dst, "SOA"...)
	case 12:
		return append(dst, "PTR"...)
	case 15:
		return append(dst, "MX"...)
	case 16:
		return append(dst, "TXT"...)
	case 28:
		return append(dst, "AAAA"...)
	case 33:
		return append(dst, "SRV"...)
	case 255:
		return append(dst, "ANY"...)
	}
	return strconv.AppendUint(append(dst, "TYPE"...), uint64(t), 10)
}

// domainTracker counts the DNS queries by domain and type over the same window
// as the client table, and keeps who asked for each name. Its keys are hashes
// of the "domain\x00TYPE" text (with a seed of this process, so nobody can aim at
// a collision); the text itself is kept for the entries in the table.
//
// names is one row per name: how many queries were served and how many refused.
// pairs is one row per (name, client address), so a name can be listed with the
// addresses that asked for it. Both tables are the fixed-size space-saving kind,
// so the pairs of a name hit by a flood from countless addresses are a lower
// bound: names says what is left over.
type domainTracker struct {
	tracker // names
	pairs   tracker
	seed    maphash.Seed
}

func newDomainTracker() *domainTracker {
	return &domainTracker{tracker: tracker{clock: coarseUnix}, pairs: tracker{clock: coarseUnix}, seed: maphash.MakeSeed()}
}

// setClock moves both tables to the same clock (tests).
func (t *domainTracker) setClock(clock func() int64) { t.clock, t.pairs.clock = clock, clock }

func (t *domainTracker) nameKey(label []byte) ipKey {
	var k ipKey
	binary.LittleEndian.PutUint64(k[:8], maphash.Bytes(t.seed, label))
	return k
}

// record counts one query for the name label (what queryLabel produced);
// refused says whether the balancer turned it away.
func (t *domainTracker) record(label []byte, refused bool) {
	if t == nil {
		return
	}
	t.add(t.nameKey(label), refused, label)
}

// recordFrom is record that also remembers which address asked.
func (t *domainTracker) recordFrom(label []byte, from netip.Addr, refused bool) {
	if t == nil {
		return
	}
	t.record(label, refused)
	ip := ipKey(from.As16())
	k := t.nameKey(label)
	binary.LittleEndian.PutUint64(k[8:], maphash.Bytes(t.seed, ip[:]))
	// The key is a hash of both; what it stands for is kept as the address then the name
	var both [len(ip) + maxQueryLabel]byte
	copy(both[:], ip[:])
	n := len(ip) + copy(both[len(ip):], label)
	t.pairs.add(k, refused, both[:n])
}

// BlockedDomain is one row of "what the refused queries asked for".
type BlockedDomain struct {
	Domain string `json:"domain"`
	Type   string `json:"type"`
	Count  uint64 `json:"count"`
}

// splitLabel is the other way round of queryLabel's output.
func splitLabel(label string) (domain, qtype string) {
	for i := len(label) - 1; i >= 0; i-- {
		if label[i] == 0 {
			return label[:i], label[i+1:]
		}
	}
	return label, ""
}

// top lists the n names refused most in the window.
func (t *domainTracker) top(n int) []BlockedDomain {
	if t == nil {
		return nil
	}
	ranked := t.rankedBy(n, true)
	out := make([]BlockedDomain, len(ranked))
	for i, c := range ranked {
		out[i].Domain, out[i].Type = splitLabel(c.label)
		out[i].Count = c.refused
	}
	return out
}

const (
	// queryDomainsShown names are reported for each of "served most" and "refused
	// most": a flood must not push the served names out of the list, nor the
	// other way round.
	queryDomainsShown = 20
	// queryClientsShown addresses are listed under a name.
	queryClientsShown = 8
	allKeys           = clientShards * clientShardCap * clientSlices
)

// QueryClient is one address that asked for a name.
type QueryClient struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`            // "" when unknown or the database is not loaded
	Internal bool   `json:"internal,omitempty"` // a private, loopback or link-local address
	Accepted uint64 `json:"accepted"`
	Refused  uint64 `json:"refused"`
}

// QueryDomain is one name with how its queries fared and who asked.
type QueryDomain struct {
	Domain   string        `json:"domain"`
	Type     string        `json:"type"`
	Accepted uint64        `json:"accepted"`
	Refused  uint64        `json:"refused"`
	Clients  []QueryClient `json:"clients"`
	// OtherAccepted and OtherRefused are the queries of addresses that are not
	// listed: too many of them, or too quiet to stay in the table.
	OtherAccepted uint64 `json:"other_accepted,omitempty"`
	OtherRefused  uint64 `json:"other_refused,omitempty"`
}

// QueryReport is what the "who asked for what" window shows.
type QueryReport struct {
	WindowSeconds int           `json:"window_seconds"`
	Domains       []QueryDomain `json:"domains"`
}

// report lists the names asked for in the window, each with its asking addresses.
func (t *domainTracker) report() QueryReport {
	rep := QueryReport{WindowSeconds: int(ClientWindow / time.Second), Domains: []QueryDomain{}}
	if t == nil {
		return rep
	}
	names := t.ranked(allKeys)
	if len(names) == 0 {
		return rep
	}
	pick := map[string]tally{}
	for _, by := range []func(tally) uint64{func(c tally) uint64 { return c.accepted }, func(c tally) uint64 { return c.refused }} {
		sort.Slice(names, func(i, j int) bool {
			a, b := by(names[i]), by(names[j])
			return a > b || a == b && names[i].label < names[j].label
		})
		for i := 0; i < len(names) && i < queryDomainsShown && by(names[i]) > 0; i++ {
			pick[names[i].label] = names[i]
		}
	}

	byName := map[string][]tally{}
	for _, c := range t.pairs.ranked(allKeys) {
		if len(c.label) <= len(ipKey{}) {
			continue
		}
		name := c.label[len(ipKey{}):]
		if _, ok := pick[name]; ok {
			byName[name] = append(byName[name], c)
		}
	}

	for name, c := range pick {
		d := QueryDomain{Accepted: c.accepted, Refused: c.refused}
		d.Domain, d.Type = splitLabel(name)
		pairs := byName[name]
		sort.Slice(pairs, func(i, j int) bool {
			a, b := pairs[i].hits(), pairs[j].hits()
			return a > b || a == b && pairs[i].label < pairs[j].label
		})
		var listedAccepted, listedRefused uint64
		for i, pc := range pairs {
			if i == queryClientsShown {
				break
			}
			addr := ipKey([]byte(pc.label)[:len(ipKey{})]).addr()
			qc := QueryClient{IP: addr.String(), Accepted: pc.accepted, Refused: pc.refused, Internal: isInternal(addr)}
			if GeoIP != nil && !qc.Internal {
				if cc, loaded := GeoIP.Country(addr); loaded {
					qc.Country = cc
				}
			}
			d.Clients = append(d.Clients, qc)
			listedAccepted += pc.accepted
			listedRefused += pc.refused
		}
		if d.Accepted > listedAccepted {
			d.OtherAccepted = d.Accepted - listedAccepted
		}
		if d.Refused > listedRefused {
			d.OtherRefused = d.Refused - listedRefused
		}
		if d.Clients == nil {
			d.Clients = []QueryClient{}
		}
		rep.Domains = append(rep.Domains, d)
	}
	sort.Slice(rep.Domains, func(i, j int) bool {
		a, b := rep.Domains[i], rep.Domains[j]
		ha, hb := a.Accepted+a.Refused, b.Accepted+b.Refused
		if ha != hb {
			return ha > hb
		}
		return a.Domain+"\x00"+a.Type < b.Domain+"\x00"+b.Type
	})
	return rep
}
