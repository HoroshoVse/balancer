// Package geo maps IP addresses to countries using the delegation statistics
// that the five Regional Internet Registries publish themselves.
package geo

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

type v4Range struct {
	start, end uint32
	cc         uint16
}

type v6Range struct {
	startHi, startLo, endHi, endLo uint64
	cc                             uint16
}

// ccCode packs a two-letter code into 0..675; ccString reverses it.
func ccCode(s string) (uint16, bool) {
	if len(s) != 2 {
		return 0, false
	}
	a, b := s[0], s[1]
	if a >= 'a' && a <= 'z' {
		a -= 'a' - 'A'
	}
	if b >= 'a' && b <= 'z' {
		b -= 'a' - 'A'
	}
	if a < 'A' || a > 'Z' || b < 'A' || b > 'Z' {
		return 0, false
	}
	return uint16(a-'A')*26 + uint16(b-'A'), true
}

func ccString(c uint16) string {
	return string([]byte{byte('A' + c/26), byte('A' + c%26)})
}

// parsed is the content of one registry file.
type parsed struct {
	registry string
	serial   string
	v4       []v4Range
	v6       []v6Range
}

// parseDelegated reads the "delegated-*-extended" format:
//
//	2|ripencc|1792792799|261085|19700101|20261023|+0200     (header: version|registry|serial|records|start|end date|offset)
//	ripencc|*|ipv4|*|90210|summary                            (summary)
//	ripencc|RU|ipv4|2.56.8.0|1024|20200101|allocated|id       (record)
//	ripencc|DE|ipv6|2001:db8::|32|20200101|allocated|id       (record)
//
// For IPv4 the value is a number of addresses (not always a power of two); for
// IPv6 it is the prefix length. Only allocated and assigned space counts.
func parseDelegated(r io.Reader) (*parsed, error) {
	p := &parsed{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.SplitN(line, "|", 9)
		if len(f) >= 7 && p.registry == "" && isVersion(f[0]) {
			p.registry, p.serial = f[1], f[5] // the sixth field is the date of the data; the third is a serial number or a date, depending on the registry
			continue
		}
		if len(f) < 7 || f[1] == "*" || (f[2] != "ipv4" && f[2] != "ipv6") {
			continue
		}
		if f[6] != "allocated" && f[6] != "assigned" {
			continue
		}
		cc, ok := ccCode(f[1])
		if !ok || f[1] == "ZZ" {
			continue
		}
		start, err := netip.ParseAddr(f[3])
		if err != nil {
			return nil, fmt.Errorf("bad address %q", f[3])
		}
		n, err := strconv.ParseUint(f[4], 10, 64)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("bad size %q for %s", f[4], f[3])
		}
		if f[2] == "ipv4" {
			if !start.Is4() {
				return nil, fmt.Errorf("%s is not IPv4", f[3])
			}
			s := binary.BigEndian.Uint32(start.AsSlice())
			e := uint64(s) + n - 1
			if e > 0xFFFFFFFF {
				return nil, fmt.Errorf("range %s+%d leaves the IPv4 space", f[3], n)
			}
			p.v4 = append(p.v4, v4Range{start: s, end: uint32(e), cc: cc})
			continue
		}
		if !start.Is6() || n > 128 {
			return nil, fmt.Errorf("bad IPv6 record %s/%d", f[3], n)
		}
		b := start.As16()
		hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
		ehi, elo := hi, lo
		switch {
		case n <= 64:
			ehi = hi | (^uint64(0) >> n)
			elo = ^uint64(0)
		default:
			elo = lo | (^uint64(0) >> (n - 64))
		}
		p.v6 = append(p.v6, v6Range{hi, lo, ehi, elo, cc})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if p.registry == "" {
		return nil, fmt.Errorf("no header line: this is not a delegation file")
	}
	return p, nil
}

func isVersion(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// DB looks up the country of an address.
type DB struct {
	v4        []v4Range
	v6        []v6Range
	countries []string
}

// build merges the registries into one sorted index. Registries do not
// overlap; if data ever does, the lower range wins and the other is trimmed.
func build(files []*parsed) *DB {
	var v4 []v4Range
	var v6 []v6Range
	for _, f := range files {
		v4 = append(v4, f.v4...)
		v6 = append(v6, f.v6...)
	}
	sort.Slice(v4, func(i, j int) bool { return v4[i].start < v4[j].start })
	sort.Slice(v6, func(i, j int) bool { return less6(v6[i].startHi, v6[i].startLo, v6[j].startHi, v6[j].startLo) })

	merged4 := v4[:0:0]
	for _, r := range v4 {
		if n := len(merged4); n > 0 {
			last := &merged4[n-1]
			if r.start <= last.end {
				if r.end <= last.end {
					continue
				}
				r.start = last.end + 1
			}
			if r.cc == last.cc && r.start == last.end+1 && last.end != 0xFFFFFFFF {
				last.end = r.end
				continue
			}
		}
		merged4 = append(merged4, r)
	}
	merged6 := v6[:0:0]
	for _, r := range v6 {
		if n := len(merged6); n > 0 {
			last := &merged6[n-1]
			if !less6(last.endHi, last.endLo, r.startHi, r.startLo) { // overlaps
				if !less6(last.endHi, last.endLo, r.endHi, r.endLo) {
					continue
				}
				r.startHi, r.startLo = inc6(last.endHi, last.endLo)
			}
			if r.cc == last.cc {
				if hi, lo := inc6(last.endHi, last.endLo); hi == r.startHi && lo == r.startLo {
					last.endHi, last.endLo = r.endHi, r.endLo
					continue
				}
			}
		}
		merged6 = append(merged6, r)
	}

	seen := map[uint16]bool{}
	for _, r := range merged4 {
		seen[r.cc] = true
	}
	for _, r := range merged6 {
		seen[r.cc] = true
	}
	d := &DB{v4: merged4, v6: merged6}
	for c := range seen {
		d.countries = append(d.countries, ccString(c))
	}
	sort.Strings(d.countries)
	return d
}

func less6(aHi, aLo, bHi, bLo uint64) bool {
	return aHi < bHi || (aHi == bHi && aLo < bLo)
}

func inc6(hi, lo uint64) (uint64, uint64) {
	lo++
	if lo == 0 {
		hi++
	}
	return hi, lo
}

// Lookup returns the country of the address, or "" when no registry has it.
func (d *DB) Lookup(a netip.Addr) string {
	a = a.Unmap()
	if a.Is4() {
		v := binary.BigEndian.Uint32(a.AsSlice())
		lo, hi := 0, len(d.v4)
		for lo < hi { // first range that starts after v
			mid := int(uint(lo+hi) >> 1)
			if d.v4[mid].start > v {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		if lo > 0 && v <= d.v4[lo-1].end {
			return ccString(d.v4[lo-1].cc)
		}
		return ""
	}
	if !a.Is6() {
		return ""
	}
	b := a.As16()
	vh, vl := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	lo, hi := 0, len(d.v6)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if less6(vh, vl, d.v6[mid].startHi, d.v6[mid].startLo) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo > 0 {
		r := d.v6[lo-1]
		if !less6(r.endHi, r.endLo, vh, vl) {
			return ccString(r.cc)
		}
	}
	return ""
}

// Countries lists the codes that have at least one range, sorted.
func (d *DB) Countries() []string { return d.countries }

// Ranges is the number of IPv4 and IPv6 ranges in the index.
func (d *DB) Ranges() (v4, v6 int) { return len(d.v4), len(d.v6) }
