package models

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

var (
	validProtocols   = map[string]bool{"http": true, "https": true, "tcp": true, "udp": true}
	validAlgorithms  = map[string]bool{"round_robin": true, "least_conn": true, "weighted_round_robin": true, "ip_hash": true, "failover": true}
	validHCProtocols = map[string]bool{"http": true, "https": true, "tcp": true, "udp": true, "dns": true}
)

// TerminatesHTTP reports whether the balancer parses HTTP itself
// (as opposed to forwarding raw TCP/UDP or TLS passthrough).
func (lb *LoadBalancer) TerminatesHTTP() bool {
	return lb.Protocol == "http" || (lb.Protocol == "https" && lb.SSLEnabled)
}

// UsesACME reports whether certificates are obtained automatically.
func (lb *LoadBalancer) UsesACME() bool {
	return lb.ACMEEnabled || lb.AutoSSL
}

// HTTPRedirectPort is where the HTTP->HTTPS redirect listens.
const HTTPRedirectPort = 80

// MaxTimeoutMs caps the configurable timeouts (24h).
const MaxTimeoutMs = 24 * 60 * 60 * 1000

// Socket is one address family/port pair a balancer binds.
type Socket struct {
	Transport string // tcp or udp
	Port      int
}

// ListenSockets returns every socket the balancer binds.
func (lb *LoadBalancer) ListenSockets() []Socket {
	var sockets []Socket
	switch {
	case lb.Protocol == "udp" && lb.DNSMode:
		sockets = []Socket{{"udp", lb.ListenPort}, {"tcp", lb.ListenPort}}
	case lb.Protocol == "udp":
		sockets = []Socket{{"udp", lb.ListenPort}}
	case lb.Protocol == "https" && lb.SSLEnabled && lb.HTTP3Enabled:
		sockets = []Socket{{"tcp", lb.ListenPort}, {"udp", lb.ListenPort}}
	default:
		sockets = []Socket{{"tcp", lb.ListenPort}}
	}
	if lb.HTTPRedirect && lb.Protocol == "https" && lb.SSLEnabled {
		sockets = append(sockets, Socket{"tcp", HTTPRedirectPort})
	}
	return sockets
}

// Normalize trims input and fills in defaults before validation.
func (lb *LoadBalancer) Normalize() {
	lb.Name = strings.TrimSpace(lb.Name)
	lb.Protocol = strings.ToLower(strings.TrimSpace(lb.Protocol))
	lb.ListenIP = strings.TrimSpace(lb.ListenIP)
	// "HTTP + SSL" (allowed by older versions) is HTTPS with termination;
	// raw TCP/UDP never terminates TLS.
	if lb.Protocol == "http" && lb.SSLEnabled {
		lb.Protocol = "https"
	}
	if lb.Protocol == "tcp" || lb.Protocol == "udp" {
		lb.SSLEnabled = false
		lb.HTTP3Enabled = false
	}
	if lb.Algorithm == "" {
		lb.Algorithm = "round_robin"
	}
	if lb.BackupMode == "" {
		lb.BackupMode = BackupAll
	}
	if lb.ProxyProtocolVersion == 0 {
		lb.ProxyProtocolVersion = 2
	}
	if lb.StickySessionsEnabled && lb.StickySessionType == "" {
		lb.StickySessionType = "ip"
	}
	lb.ACMEDomains = strings.TrimSpace(lb.ACMEDomains)
	lb.ACMEEmail = strings.TrimSpace(lb.ACMEEmail)
	if strings.TrimSpace(lb.BackendGroup.Name) == "" {
		lb.BackendGroup.Name = lb.Name + " Group"
	}
	for i := range lb.BackendGroup.Backends {
		b := &lb.BackendGroup.Backends[i]
		b.Position = i
		b.Address = strings.TrimSpace(b.Address)
		b.Name = strings.TrimSpace(b.Name)
		if b.Name == "" {
			b.Name = b.Address
		}
		if b.Weight <= 0 {
			b.Weight = 1
		}
		b.SNI = strings.TrimSpace(b.SNI)
		b.HCProtocol = strings.ToLower(strings.TrimSpace(b.HCProtocol))
		if b.HCPath != "" && !strings.HasPrefix(b.HCPath, "/") && b.HCProtocol != "dns" {
			b.HCPath = "/" + b.HCPath
		}
	}
}

// Validate checks a normalized load balancer configuration.
func (lb *LoadBalancer) Validate() error {
	if lb.Name == "" {
		return errors.New("name is required")
	}
	if len(lb.Name) > 100 {
		return errors.New("name must be at most 100 characters")
	}
	if !validProtocols[lb.Protocol] {
		return fmt.Errorf("unknown protocol %q (expected http, https, tcp or udp)", lb.Protocol)
	}
	if !validAlgorithms[lb.Algorithm] {
		return fmt.Errorf("unknown algorithm %q", lb.Algorithm)
	}
	if lb.BackupMode != BackupAll && lb.BackupMode != BackupOrdered {
		return fmt.Errorf("unknown backup mode %q (expected all or ordered)", lb.BackupMode)
	}
	if lb.ListenPort < 1 || lb.ListenPort > 65535 {
		return fmt.Errorf("listen port must be between 1 and 65535")
	}
	if lb.ListenIP != "" && net.ParseIP(lb.ListenIP) == nil {
		return fmt.Errorf("listen IP %q is not a valid IP address", lb.ListenIP)
	}
	if lb.ProxyProtocolEnabled {
		if lb.ProxyProtocolVersion != 1 && lb.ProxyProtocolVersion != 2 {
			return errors.New("proxy protocol version must be 1 or 2")
		}
		if lb.Protocol == "udp" && lb.ProxyProtocolVersion != 2 {
			return errors.New("proxy protocol v1 does not support UDP, use v2")
		}
	}
	if lb.StickySessionsEnabled {
		switch lb.StickySessionType {
		case "ip":
		case "cookie":
			if !lb.TerminatesHTTP() {
				return errors.New("cookie sticky sessions require HTTP or HTTPS with SSL termination, use source IP instead")
			}
		default:
			return fmt.Errorf("unknown sticky session type %q (expected ip or cookie)", lb.StickySessionType)
		}
	}
	if lb.SSLEnabled && lb.UsesACME() && lb.ACMEDomains == "" {
		return errors.New("Auto-SSL requires at least one domain")
	}
	if lb.DNSMode && lb.Protocol != "udp" {
		return errors.New("DNS mode is only available for the UDP protocol")
	}
	if lb.HTTPRedirect {
		if !(lb.Protocol == "https" && lb.SSLEnabled) {
			return errors.New("HTTP to HTTPS redirect requires the HTTPS protocol with SSL enabled")
		}
		if lb.ListenPort == HTTPRedirectPort {
			return fmt.Errorf("HTTP to HTTPS redirect listens on port %d, so the balancer itself can't use it", HTTPRedirectPort)
		}
	}
	for name, v := range map[string]int{"connect": lb.ConnectTimeout, "idle": lb.IdleTimeout, "response": lb.ResponseTimeout} {
		if v < 0 || v > MaxTimeoutMs {
			return fmt.Errorf("%s timeout must be between 0 and %d ms", name, MaxTimeoutMs)
		}
	}
	if lb.HTTP3Enabled && !(lb.Protocol == "https" && lb.SSLEnabled) {
		return errors.New("HTTP/3 requires the HTTPS protocol with SSL enabled")
	}

	if len(lb.BackendGroup.Backends) == 0 {
		return errors.New("add at least one backend node")
	}
	for i, b := range lb.BackendGroup.Backends {
		if err := b.validate(); err != nil {
			return fmt.Errorf("node #%d: %w", i+1, err)
		}
	}
	return nil
}

func (b *BackendServer) validate() error {
	if b.Address == "" {
		return errors.New("address is required")
	}
	if strings.ContainsAny(b.Address, " /") {
		return fmt.Errorf("address %q must be an IP or hostname without scheme or path", b.Address)
	}
	if b.Port < 1 || b.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if b.MaxConns < 0 {
		return errors.New("max connections cannot be negative")
	}
	if b.HCEnabled {
		if b.HCProtocol != "" && !validHCProtocols[b.HCProtocol] {
			return fmt.Errorf("unknown health check protocol %q", b.HCProtocol)
		}
		if b.HCPort < 0 || b.HCPort > 65535 {
			return errors.New("health check port must be between 0 and 65535")
		}
		if b.HCInterval < 0 || b.HCTimeout < 0 || b.HCFailureThreshold < 0 || b.HCRecoveryThreshold < 0 {
			return errors.New("health check interval, timeout and thresholds cannot be negative")
		}
	}
	return nil
}

func isWildcardIP(ip string) bool {
	return ip == "" || ip == "0.0.0.0" || ip == "::"
}

// ConflictsWith returns the port two balancers would both bind, or 0.
func (lb *LoadBalancer) ConflictsWith(other *LoadBalancer) int {
	if !isWildcardIP(lb.ListenIP) && !isWildcardIP(other.ListenIP) && lb.ListenIP != other.ListenIP {
		return 0
	}
	for _, a := range lb.ListenSockets() {
		for _, b := range other.ListenSockets() {
			if a == b {
				return a.Port
			}
		}
	}
	return 0
}
