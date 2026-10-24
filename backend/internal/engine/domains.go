package engine

import (
	"encoding/binary"
	"hash/maphash"
	"strconv"
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

// domainTracker counts the refused queries by domain and type over the same
// window as the client table. Its keys are hashes of the "domain\x00TYPE" text
// (with a seed of this process, so nobody can aim at a collision); the text
// itself is kept for the entries in the table.
type domainTracker struct {
	tracker
	seed maphash.Seed
}

func newDomainTracker() *domainTracker {
	return &domainTracker{tracker{clock: coarseUnix}, maphash.MakeSeed()}
}

// record counts one refused query; label is what queryLabel produced.
func (t *domainTracker) record(label []byte) {
	if t == nil {
		return
	}
	var k ipKey
	binary.LittleEndian.PutUint64(k[:8], maphash.Bytes(t.seed, label))
	t.add(k, true, label)
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

// top lists the n most asked-for names of the window.
func (t *domainTracker) top(n int) []BlockedDomain {
	if t == nil {
		return nil
	}
	ranked := t.ranked(n)
	out := make([]BlockedDomain, len(ranked))
	for i, c := range ranked {
		out[i].Domain, out[i].Type = splitLabel(c.label)
		out[i].Count = c.hits()
	}
	return out
}
