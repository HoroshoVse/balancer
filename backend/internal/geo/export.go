package geo

import (
	"fmt"
	"io"
	"math/big"
	"math/bits"
	"net/netip"
	"sort"
	"strings"
)

// Prefixes returns the subnets of the given countries as CIDR blocks, for
// handing to a firewall. Ranges are not aligned to subnet boundaries, so each
// is split into the fewest blocks that cover it exactly.
func (d *DB) Prefixes(countries []string) (v4, v6 []netip.Prefix) {
	want := map[uint16]bool{}
	for _, c := range countries {
		if code, ok := ccCode(c); ok {
			want[code] = true
		}
	}
	for _, r := range d.v4 {
		if want[r.cc] {
			v4 = append(v4, cidrs4(r.start, r.end)...)
		}
	}
	for _, r := range d.v6 {
		if want[r.cc] {
			v6 = append(v6, cidrs6(r)...)
		}
	}
	return v4, v6
}

func cidrs4(start, end uint32) []netip.Prefix {
	var out []netip.Prefix
	for {
		size := uint64(1) << 32
		if start != 0 {
			size = uint64(1) << bits.TrailingZeros32(start)
		}
		for size > uint64(end)-uint64(start)+1 {
			size >>= 1
		}
		a := netip.AddrFrom4([4]byte{byte(start >> 24), byte(start >> 16), byte(start >> 8), byte(start)})
		out = append(out, netip.PrefixFrom(a, 32-bits.Len64(size)+1))
		next := uint64(start) + size
		if next > uint64(end) {
			return out
		}
		start = uint32(next)
	}
}

func addr6(n *big.Int) netip.Addr {
	var b [16]byte
	n.FillBytes(b[:])
	return netip.AddrFrom16(b)
}

func cidrs6(r v6Range) []netip.Prefix {
	join := func(hi, lo uint64) *big.Int {
		n := new(big.Int).SetUint64(hi)
		return n.Lsh(n, 64).Or(n, new(big.Int).SetUint64(lo))
	}
	start, end := join(r.startHi, r.startLo), join(r.endHi, r.endLo)
	one := big.NewInt(1)
	var out []netip.Prefix
	for start.Cmp(end) <= 0 {
		remaining := new(big.Int).Sub(end, start)
		remaining.Add(remaining, one)
		// Largest block that starts at `start` (its lowest set bit) and fits
		size := new(big.Int).Lsh(one, 128)
		if start.Sign() != 0 {
			size = new(big.Int).Lsh(one, start.TrailingZeroBits())
		}
		for size.Cmp(remaining) > 0 {
			size.Rsh(size, 1)
		}
		out = append(out, netip.PrefixFrom(addr6(start), 128-(size.BitLen()-1)))
		start = new(big.Int).Add(start, size)
	}
	return out
}

// Export formats.
const (
	FormatPlain = "plain" // one CIDR per line
	FormatIPSet = "ipset" // for `ipset restore`: replaces the sets atomically
	FormatNft   = "nft"   // for `nft -f`: replaces the table atomically
)

// WriteExport writes the subnets of the countries in a firewall format.
// name is the base name of the sets (letters, digits, underscore).
func (d *DB) WriteExport(w io.Writer, countries []string, format, name string) error {
	if name == "" {
		name = "geo"
	}
	for _, r := range name {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return fmt.Errorf("set name %q: only letters, digits and _ are allowed", name)
		}
	}
	v4, v6 := d.Prefixes(countries)
	sortPrefixes(v4)
	sortPrefixes(v6)
	switch format {
	case FormatPlain, "":
		for _, p := range append(append([]netip.Prefix{}, v4...), v6...) {
			fmt.Fprintln(w, p)
		}
	case FormatIPSet:
		// No comment lines: not every ipset version accepts them in restore input
		for _, s := range []struct {
			set, family string
			list        []netip.Prefix
		}{{name, "inet", v4}, {name + "_v6", "inet6", v6}} {
			fmt.Fprintf(w, "create %s hash:net family %s hashsize 4096 maxelem 262144 -exist\n", s.set, s.family)
			fmt.Fprintf(w, "create %s_tmp hash:net family %s hashsize 4096 maxelem 262144 -exist\n", s.set, s.family)
			fmt.Fprintf(w, "flush %s_tmp\n", s.set)
			for _, p := range s.list {
				fmt.Fprintf(w, "add %s_tmp %s\n", s.set, p)
			}
			fmt.Fprintf(w, "swap %s_tmp %s\ndestroy %s_tmp\n", s.set, s.set, s.set)
		}
	case FormatNft:
		fmt.Fprintf(w, "# Countries: %s. Sets @%s4 and @%s6 in table inet %s.\n", strings.Join(countries, ","), name, name, name)
		fmt.Fprintf(w, "table inet %s\ndelete table inet %s\ntable inet %s {\n", name, name, name)
		for _, s := range []struct {
			set, typ string
			list     []netip.Prefix
		}{{name + "4", "ipv4_addr", v4}, {name + "6", "ipv6_addr", v6}} {
			fmt.Fprintf(w, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n", s.set, s.typ)
			if len(s.list) > 0 {
				fmt.Fprint(w, "\t\telements = {\n")
				for i, p := range s.list {
					sep := ","
					if i == len(s.list)-1 {
						sep = ""
					}
					fmt.Fprintf(w, "\t\t\t%s%s\n", p, sep)
				}
				fmt.Fprint(w, "\t\t}\n")
			}
			fmt.Fprint(w, "\t}\n")
		}
		fmt.Fprint(w, "}\n")
		// Sets are visible only inside their own table, so a rule using them goes there too
		fmt.Fprintf(w, "# To refuse UDP/53 from everywhere but these countries and the internal network, add to the table above:\n"+
			"#   chain input { type filter hook input priority -10; policy accept;\n"+
			"#     iifname != \"lo\" udp dport 53 ip saddr != @%s4 ip saddr != 10.0.0.0/8 drop }\n", name)
	default:
		return fmt.Errorf("unknown format %q (expected plain, ipset or nft)", format)
	}
	return nil
}

func sortPrefixes(p []netip.Prefix) {
	sort.Slice(p, func(i, j int) bool {
		if c := p[i].Addr().Compare(p[j].Addr()); c != 0 {
			return c < 0
		}
		return p[i].Bits() < p[j].Bits()
	})
}
