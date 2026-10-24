package models

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Values of LoadBalancer.AccessMode.
const (
	AccessEveryone = ""      // no filtering
	AccessOnly     = "allow" // only the listed countries
	AccessExcept   = "deny"  // everyone but the listed countries
)

const (
	maxAccessEntries = 5000
	maxCountries     = 300
)

// splitList splits a list written with commas, spaces or newlines.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
}

// ParsePrefixes parses a list of addresses and subnets ("1.2.3.4",
// "203.0.113.0/24", "2001:db8::/32"); a bare address is a single-host subnet.
func ParsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, tok := range splitList(list) {
		p, err := parseOnePrefix(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parseOnePrefix(tok string) (netip.Prefix, error) {
	if strings.Contains(tok, "/") {
		p, err := netip.ParsePrefix(tok)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a valid subnet", tok)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(tok)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not a valid IP address or subnet", tok)
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// ParseCountries returns the codes of a country list, upper-cased.
func ParseCountries(list string) []string {
	var out []string
	for _, c := range splitList(list) {
		out = append(out, strings.ToUpper(c))
	}
	return out
}

func (lb *LoadBalancer) normalizeAccess() {
	lb.AccessMode = strings.ToLower(strings.TrimSpace(lb.AccessMode))
	if lb.AccessMode == "off" || lb.AccessMode == "none" {
		lb.AccessMode = AccessEveryone
	}

	seen := map[string]bool{}
	var codes []string
	for _, c := range ParseCountries(lb.AccessCountries) {
		if !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	lb.AccessCountries = strings.Join(codes, ",")

	lb.AccessAllow = normalizeList(lb.AccessAllow)
	lb.AccessDeny = normalizeList(lb.AccessDeny)
}

// normalizeList writes each entry in its canonical form, one per line.
// Entries that don't parse are kept as typed so Validate can name them.
func normalizeList(list string) string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range splitList(list) {
		if p, err := parseOnePrefix(tok); err == nil {
			if p.IsSingleIP() {
				tok = p.Addr().String()
			} else {
				tok = p.String()
			}
		}
		if !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return strings.Join(out, "\n")
}

func (lb *LoadBalancer) validateAccess() error {
	switch lb.AccessMode {
	case AccessEveryone, AccessOnly, AccessExcept:
	default:
		return fmt.Errorf("unknown access mode %q (expected allow or deny)", lb.AccessMode)
	}
	codes := ParseCountries(lb.AccessCountries)
	if len(codes) > maxCountries {
		return errors.New("too many countries")
	}
	for _, c := range codes {
		if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
			return fmt.Errorf("%q is not a two-letter country code", c)
		}
	}
	for _, list := range []struct{ name, text string }{{"allow list", lb.AccessAllow}, {"deny list", lb.AccessDeny}} {
		toks := splitList(list.text)
		if len(toks) > maxAccessEntries {
			return fmt.Errorf("the %s has more than %d entries", list.name, maxAccessEntries)
		}
		for _, tok := range toks {
			if _, err := parseOnePrefix(tok); err != nil {
				return fmt.Errorf("%s: %w", list.name, err)
			}
		}
	}
	if lb.AccessMode == AccessOnly && len(codes) == 0 && lb.AccessAllow == "" {
		return errors.New("access limited to the listed countries, but no country or allowed address is listed: nobody could connect")
	}
	return nil
}
