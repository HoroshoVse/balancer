package models

import (
	"net/netip"
	"strings"
)

// NormalizeBackendAddress accepts an IPv6 literal with optional URL-style
// brackets. The port lives in a separate field; storing brackets would cause
// net.JoinHostPort to produce the invalid address [[IPv6]]:port.
func NormalizeBackendAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		host := address[1 : len(address)-1]
		if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
			return host
		}
	}
	return address
}
