package geo

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
)

func prefixStrings(p []netip.Prefix) string {
	s := make([]string, len(p))
	for i, x := range p {
		s[i] = x.String()
	}
	return strings.Join(s, " ")
}

func TestCIDRSplitIsExact(t *testing.T) {
	cases := []struct {
		start, end uint32
		want       string
	}{
		{0x01000000, 0x01FFFFFF, "1.0.0.0/8"},
		{0x05000100, 0x050003FF, "5.0.1.0/24 5.0.2.0/23"},             // 768 addresses starting off a block boundary
		{0x05010000, 0x0501012F, "5.1.0.0/24 5.1.1.0/27 5.1.1.32/28"}, // 304 addresses: /24 + /27 + /28
		{0x7F000001, 0x7F000001, "127.0.0.1/32"},
		{0, 0xFFFFFFFF, "0.0.0.0/0"},
		{0xFFFFFF00, 0xFFFFFFFF, "255.255.255.0/24"}, // up to the top of the space
	}
	for _, c := range cases {
		if got := prefixStrings(cidrs4(c.start, c.end)); got != c.want {
			t.Errorf("%d-%d = %s, want %s", c.start, c.end, got, c.want)
		}
	}
	// Exhaustive check on a messy range: every address covered once, none outside
	start, end := uint32(0x0A000007), uint32(0x0A0013F1)
	covered := uint64(0)
	for _, p := range cidrs4(start, end) {
		lo := uint64(netipTo32(p.Addr()))
		size := uint64(1) << (32 - p.Bits())
		if lo < uint64(start) || lo+size-1 > uint64(end) {
			t.Fatalf("%s leaves the range", p)
		}
		covered += size
	}
	if covered != uint64(end)-uint64(start)+1 {
		t.Fatalf("covered %d of %d", covered, uint64(end)-uint64(start)+1)
	}
}

func netipTo32(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func TestCIDRSplitIPv6(t *testing.T) {
	// 2001:db8::/32 as a range, and a merged pair of adjacent /33s, and an unaligned stretch
	whole := v6Range{startHi: 0x20010db800000000, endHi: 0x20010db8ffffffff, endLo: ^uint64(0)}
	if got := prefixStrings(cidrs6(whole)); got != "2001:db8::/32" {
		t.Errorf("got %s", got)
	}
	odd := v6Range{startHi: 0x20010db800000000, startLo: 0x1, endHi: 0x20010db800000000, endLo: 0x4}
	if got := prefixStrings(cidrs6(odd)); got != "2001:db8::1/128 2001:db8::2/127 2001:db8::4/128" {
		t.Errorf("got %s", got)
	}
}

func TestExportFormats(t *testing.T) {
	p := &parsed{registry: "x", v4: []v4Range{
		{start: 0x05000000, end: 0x0500FFFF, cc: mustCC("RU")},
		{start: 0x08000000, end: 0x08FFFFFF, cc: mustCC("US")},
	}, v6: []v6Range{{startHi: 0x2a00000000000000, endHi: 0x2a00ffffffffffff, endLo: ^uint64(0), cc: mustCC("RU")}}}
	db := build([]*parsed{p})

	var b bytes.Buffer
	if err := db.WriteExport(&b, []string{"ru"}, FormatPlain, ""); err != nil {
		t.Fatal(err)
	}
	if b.String() != "5.0.0.0/16\n2a00::/16\n" {
		t.Errorf("plain:\n%s", b.String())
	}

	b.Reset()
	if err := db.WriteExport(&b, []string{"RU"}, FormatIPSet, "geo_ru"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"create geo_ru hash:net family inet", "flush geo_ru_tmp\n", "add geo_ru_tmp 5.0.0.0/16\n", "swap geo_ru_tmp geo_ru\n", "destroy geo_ru_tmp\n",
		"create geo_ru_v6 hash:net family inet6", "add geo_ru_v6_tmp 2a00::/16\n",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("ipset output lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "8.0.0.0") {
		t.Error("exported a country that was not asked for")
	}

	b.Reset()
	if err := db.WriteExport(&b, []string{"RU", "US"}, FormatNft, "geo"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"delete table inet geo", "set geo4 {", "type ipv4_addr", "flags interval", "\t\t\t5.0.0.0/16,\n\t\t\t8.0.0.0/8\n", "set geo6 {", "2a00::/16"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("nft output lacks %q:\n%s", want, b.String())
		}
	}

	if err := db.WriteExport(&b, []string{"RU"}, "xml", ""); err == nil {
		t.Error("accepted an unknown format")
	}
	if err := db.WriteExport(&b, []string{"RU"}, FormatIPSet, "bad name; rm -rf"); err == nil {
		t.Error("accepted an unsafe set name")
	}
}
