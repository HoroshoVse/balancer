package engine

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/models"
)

func TestSharedAccessListLiveUpdates(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "test-lists-password")
	database, err := db.InitDB("sqlite://" + filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	defer sqlDB.Close()
	list := models.AccessList{Name: "blocked", SourceEntries: "198.51.100.0/24\n2001:db8::/32"}
	if err := database.Create(&list).Error; err != nil {
		t.Fatal(err)
	}
	e := NewEngine(database)
	if err := e.RefreshAccessLists(); err != nil {
		t.Fatal(err)
	}
	p := newAccessPolicy(models.LoadBalancer{AccessDenyLists: []uint{list.ID}})
	p.lists = &e.accessLists
	for _, raw := range []string{"198.51.100.7", "2001:db8::1"} {
		d := p.decide(netip.MustParseAddr(raw))
		if d.Allowed || d.Reason != ReasonDenyList || d.Rule == "" {
			t.Fatalf("did not block %s: %+v", raw, d)
		}
	}
	// Shared allow lists retain priority over a direct deny entry.
	allow := newAccessPolicy(models.LoadBalancer{AccessAllowLists: []uint{list.ID}, AccessDeny: "198.51.100.7"})
	allow.lists = &e.accessLists
	if d := allow.decide(netip.MustParseAddr("198.51.100.7")); !d.Allowed || d.Reason != ReasonAllowList {
		t.Fatalf("allow priority: %+v", d)
	}
	// Allow-only address rules cannot turn into allow-everyone while GeoIP is absent.
	only := newAccessPolicy(models.LoadBalancer{AccessMode: models.AccessOnly, AccessAllowLists: []uint{list.ID}})
	only.lists = &e.accessLists
	if d := only.decide(netip.MustParseAddr("203.0.113.7")); d.Allowed {
		t.Fatalf("allow-only list failed open: %+v", d)
	}
	// A policy already held by a listener sees refreshed data without recreation.
	list.SourceEntries = "192.0.2.0/24"
	database.Save(&list)
	if err := e.RefreshAccessLists(); err != nil {
		t.Fatal(err)
	}
	if !p.decide(netip.MustParseAddr("198.51.100.7")).Allowed || p.decide(netip.MustParseAddr("192.0.2.7")).Allowed {
		t.Fatal("existing policy did not see updated list")
	}
	// Concurrent lookups exercise the atomic publication under the race detector.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			p.decide(netip.MustParseAddr("192.0.2.7"))
		}
	}()
	for i := 0; i < 10; i++ {
		list.Entries = fmt.Sprintf("203.0.113.%d", i)
		if err := database.Save(&list).Error; err != nil {
			t.Fatal(err)
		}
		if err := e.RefreshAccessLists(); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
