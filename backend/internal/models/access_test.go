package models

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeAccess(t *testing.T) {
	lb := LoadBalancer{
		Name: "x", AccessMode: " ALLOW ", AccessCountries: "ru, by;RU\nkz",
		AccessAllow: "203.0.113.7  203.0.113.0/24,\n2001:DB8::/32 1.2.3.4/24 203.0.113.7",
		AccessDeny:  "::ffff:198.51.100.5",
	}
	lb.Normalize()
	if lb.AccessMode != AccessOnly || lb.AccessCountries != "BY,KZ,RU" {
		t.Fatalf("%q %q", lb.AccessMode, lb.AccessCountries)
	}
	if want := "203.0.113.7\n203.0.113.0/24\n2001:db8::/32\n1.2.3.0/24"; lb.AccessAllow != want {
		t.Fatalf("allow list %q, want %q", lb.AccessAllow, want)
	}
	if lb.AccessDeny != "198.51.100.5" {
		t.Fatalf("deny list %q", lb.AccessDeny)
	}
	again := lb
	again.Normalize()
	if again.AccessCountries != lb.AccessCountries || again.AccessAllow != lb.AccessAllow {
		t.Fatal("normalizing twice changed the result")
	}
	if off := (LoadBalancer{Name: "x", AccessMode: "off"}); func() string { off.Normalize(); return off.AccessMode }() != AccessEveryone {
		t.Fatal(`"off" is not "everyone"`)
	}
}

func manyAddresses(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "10.%d.%d.1\n", i/250, i%250)
	}
	return b.String()
}

func TestValidateAccess(t *testing.T) {
	valid := func(mutate func(*LoadBalancer)) error {
		lb := LoadBalancer{
			Name: "x", Protocol: "tcp", ListenPort: 443,
			BackendGroup: BackendGroup{Backends: []BackendServer{{Address: "10.0.0.1", Port: 80, Enabled: true}}},
		}
		mutate(&lb)
		lb.Normalize()
		return lb.Validate()
	}
	ok := []func(*LoadBalancer){
		func(lb *LoadBalancer) {},
		func(lb *LoadBalancer) { lb.AccessMode, lb.AccessCountries = "allow", "RU,BY" },
		func(lb *LoadBalancer) { lb.AccessMode, lb.AccessAllow = "allow", "203.0.113.0/24" }, // addresses only
		func(lb *LoadBalancer) { lb.AccessMode = "deny" },                                    // nobody listed: no effect
		func(lb *LoadBalancer) { lb.AccessCountries = "US" },                                 // kept for later
	}
	for i, m := range ok {
		if err := valid(m); err != nil {
			t.Errorf("valid case %d: %v", i, err)
		}
	}
	bad := map[string]func(*LoadBalancer){
		"nobody could connect": func(lb *LoadBalancer) { lb.AccessMode = "allow" },
		"unknown access mode":  func(lb *LoadBalancer) { lb.AccessMode = "block" },
		"two-letter":           func(lb *LoadBalancer) { lb.AccessMode, lb.AccessCountries = "deny", "RUS" },
		`"R1" is not`:          func(lb *LoadBalancer) { lb.AccessMode, lb.AccessCountries = "deny", "R1" },
		"allow list":           func(lb *LoadBalancer) { lb.AccessAllow = "10.0.0.256" },
		"deny list":            func(lb *LoadBalancer) { lb.AccessDeny = "1.2.3.4/33" },
		"more than":            func(lb *LoadBalancer) { lb.AccessDeny = manyAddresses(6000) },
	}
	for want, m := range bad {
		if err := valid(m); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error containing %q, got %v", want, err)
		}
	}
}
