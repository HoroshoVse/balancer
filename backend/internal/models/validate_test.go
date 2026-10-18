package models

import (
	"strings"
	"testing"
)

func validLB() LoadBalancer {
	return LoadBalancer{
		Name: "web", Protocol: "http", ListenPort: 80,
		BackendGroup: BackendGroup{Backends: []BackendServer{{Address: "10.0.0.2", Port: 8080, Enabled: true}}},
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*LoadBalancer)
		wantErr string
	}{
		{"valid", func(*LoadBalancer) {}, ""},
		{"missing name", func(lb *LoadBalancer) { lb.Name = "  " }, "name is required"},
		{"bad protocol", func(lb *LoadBalancer) { lb.Protocol = "ftp" }, "unknown protocol"},
		{"bad port", func(lb *LoadBalancer) { lb.ListenPort = 70000 }, "listen port"},
		{"bad listen ip", func(lb *LoadBalancer) { lb.ListenIP = "not-an-ip" }, "not a valid IP"},
		{"no backends", func(lb *LoadBalancer) { lb.BackendGroup.Backends = nil }, "at least one backend"},
		{"backend without port", func(lb *LoadBalancer) { lb.BackendGroup.Backends[0].Port = 0 }, "node #1: port"},
		{"backend with scheme", func(lb *LoadBalancer) { lb.BackendGroup.Backends[0].Address = "http://x" }, "without scheme"},
		{"cookie sticky on tcp", func(lb *LoadBalancer) {
			lb.Protocol, lb.StickySessionsEnabled, lb.StickySessionType = "tcp", true, "cookie"
		}, "cookie sticky sessions"},
		{"proxy v1 on udp", func(lb *LoadBalancer) {
			lb.Protocol, lb.ProxyProtocolEnabled, lb.ProxyProtocolVersion = "udp", true, 1
		}, "does not support UDP"},
		{"dns mode on tcp", func(lb *LoadBalancer) { lb.Protocol, lb.DNSMode = "tcp", true }, "only available for the UDP"},
		{"redirect without ssl", func(lb *LoadBalancer) { lb.HTTPRedirect = true }, "requires the HTTPS protocol"},
		{"redirect on port 80", func(lb *LoadBalancer) { lb.Protocol, lb.SSLEnabled, lb.HTTPRedirect = "https", true, true }, "can't use it"},
		{"redirect ok", func(lb *LoadBalancer) {
			lb.Protocol, lb.SSLEnabled, lb.HTTPRedirect, lb.ListenPort = "https", true, true, 443
		}, ""},
		{"negative timeout", func(lb *LoadBalancer) { lb.IdleTimeout = -1 }, "idle timeout"},
		{"dns mode on udp", func(lb *LoadBalancer) { lb.Protocol, lb.DNSMode = "udp", true }, ""},
		{"acme without domains", func(lb *LoadBalancer) {
			lb.Protocol, lb.SSLEnabled, lb.ACMEEnabled = "https", true, true
		}, "at least one domain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lb := validLB()
			tc.mutate(&lb)
			lb.Normalize()
			err := lb.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestNormalizeDefaults(t *testing.T) {
	lb := validLB()
	lb.Protocol = "HTTP"
	lb.SSLEnabled = true
	lb.BackendGroup.Backends[0].Weight = 0
	lb.BackendGroup.Backends[0].HCPath = "health"
	lb.Normalize()
	if lb.Protocol != "https" || lb.Algorithm != "round_robin" || lb.ProxyProtocolVersion != 2 {
		t.Fatalf("unexpected defaults: %+v", lb)
	}
	b := lb.BackendGroup.Backends[0]
	if b.Weight != 1 || b.HCPath != "/health" || b.Name != "10.0.0.2" {
		t.Fatalf("unexpected backend defaults: %+v", b)
	}
}

func TestConflictsWith(t *testing.T) {
	lb := func(proto, ip string, port int, h3 bool) *LoadBalancer {
		return &LoadBalancer{Protocol: proto, ListenIP: ip, ListenPort: port, SSLEnabled: proto == "https", HTTP3Enabled: h3}
	}
	cases := []struct {
		a, b *LoadBalancer
		want bool
	}{
		{lb("http", "0.0.0.0", 80, false), lb("tcp", "10.0.0.1", 80, false), true},
		{lb("http", "10.0.0.1", 80, false), lb("http", "10.0.0.2", 80, false), false},
		{lb("tcp", "", 53, false), lb("udp", "", 53, false), false},
		{lb("https", "", 443, true), lb("udp", "", 443, false), true},
		{lb("http", "", 80, false), lb("http", "", 81, false), false},
		// DNS mode serves TCP too, so it clashes with a TCP balancer on 53
		{&LoadBalancer{Protocol: "udp", DNSMode: true, ListenPort: 53}, lb("tcp", "", 53, false), true},
		// The HTTP->HTTPS redirect occupies TCP port 80
		{&LoadBalancer{Protocol: "https", SSLEnabled: true, HTTPRedirect: true, ListenPort: 443}, lb("http", "", 80, false), true},
	}
	for i, tc := range cases {
		if got := tc.a.ConflictsWith(tc.b) != 0; got != tc.want {
			t.Errorf("case %d: got %v, want %v", i, got, tc.want)
		}
	}
}
