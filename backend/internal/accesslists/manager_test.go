package accesslists

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/models"
)

func TestParseLists(t *testing.T) {
	raw := "\ufeff# networks\r\n203.0.113.42/24 # masked\n2001:DB8:1::/48\n203.0.113.0/24\n::ffff:198.51.100.3; 198.51.100.3\n// ignored\n"
	got, count, err := Parse(raw)
	if err != nil || count != 3 || got != "203.0.113.0/24\n2001:db8:1::/48\n198.51.100.3" {
		t.Fatalf("%q, count=%d, err=%v", got, count, err)
	}
	for _, bad := range []string{"1.2.3.4\n<html>error</html>", "1.2.3.4/33", "example.com", strings.Repeat("x", MaxBytes+1)} {
		if _, _, err := Parse(bad); err == nil {
			t.Fatalf("accepted invalid list %.30q", bad)
		}
	}
	var many strings.Builder
	for i := 0; i <= MaxEntries; i++ {
		fmt.Fprintf(&many, "10.%d.%d.%d\n", i>>16, (i>>8)&255, i&255)
	}
	if _, _, err := Parse(many.String()); err == nil {
		t.Fatal("entry limit was not enforced")
	}
}

func TestPublicSourcesOnly(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "ftp://example.com/list", "https://u:p@example.com/list", "https://example.com/#x", "http://example.com:99999/", "http://[bad"} {
		if ValidateURL(raw) == nil {
			t.Fatalf("accepted URL %q", raw)
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254", "::1", "fc00::1", "fe80::1", "100.64.0.1", "::ffff:127.0.0.1", "0.0.0.0", "224.0.0.1"} {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Fatalf("allowed private/service IP %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(ip)) {
			t.Fatalf("rejected public IP %s", ip)
		}
	}
	hit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true; fmt.Fprintln(w, "1.2.3.4") }))
	defer server.Close()
	client := newHTTPClient()
	defer client.CloseIdleConnections()
	if _, err := fetch(context.Background(), client, server.URL); err == nil {
		t.Fatal("local source accepted")
	}
	if hit {
		t.Fatal("request reached a local service")
	}
	req, _ := http.NewRequest("GET", "file:///etc/passwd", nil)
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("unsafe redirect accepted")
	}
}

func testManager(t *testing.T) (*Manager, *httptest.Server, *string, *int) {
	t.Helper()
	t.Setenv("ADMIN_PASSWORD", "test-access-lists-password")
	database, err := db.InitDB("sqlite://" + filepath.Join(t.TempDir(), "lists.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	t.Cleanup(func() { sqlDB.Close() })
	body := "198.51.100.0/24\n2001:db8::/32"
	status := http.StatusOK
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, body) }))
	t.Cleanup(source.Close)
	manager := New(database, nil)
	// Only tests substitute a client that can access the local fixture server.
	manager.client = source.Client()
	return manager, source, &body, &status
}

func TestLastGoodCacheAndSchedule(t *testing.T) {
	m, source, body, status := testManager(t)
	ctx := context.Background()
	applies := 0
	m.changed = func() error { applies++; return nil }
	list, err := m.Save(ctx, Input{Name: "networks", Entries: "203.0.113.7", SourceURL: source.URL, UpdateHours: 1})
	if err != nil {
		t.Fatal(err)
	}
	if list.EntryCount != 3 || applies != 1 {
		t.Fatalf("%+v, applies=%d", list, applies)
	}
	initial := list.SourceEntries
	for _, invalid := range []string{"<html>error</html>", "# empty", "1.1.1.1\nBAD"} {
		*body = invalid
		if _, err := m.Refresh(ctx, list.ID); err == nil {
			t.Fatal("accepted broken source")
		}
		var saved models.AccessList
		m.db.First(&saved, list.ID)
		if saved.SourceEntries != initial || saved.EntryCount != 3 || saved.LastError == "" || applies != 1 {
			t.Fatalf("lost working cache: %+v", saved)
		}
	}
	*status = http.StatusServiceUnavailable
	if _, err := m.Refresh(ctx, list.ID); err == nil {
		t.Fatal("accepted HTTP 503")
	}
	*status, *body = http.StatusOK, "192.0.2.0/24"
	old := time.Now().Add(-2 * time.Hour)
	m.db.Model(list).Update("last_attempt_at", &old)
	m.refreshDue(ctx)
	var saved models.AccessList
	m.db.First(&saved, list.ID)
	if saved.SourceEntries != *body || saved.Entries != "203.0.113.7" || saved.EntryCount != 2 || saved.LastError != "" || applies != 2 {
		t.Fatalf("failed to recover: %+v, applies=%d", saved, applies)
	}
	// A recent attempt is not fetched again; manual-only schedules stay manual.
	m.refreshDue(ctx)
	if applies != 2 {
		t.Fatal("refreshed before the scheduled interval")
	}
	saved.UpdateHours = 0
	m.db.Save(&saved)
	m.db.Model(&saved).Update("last_attempt_at", &old)
	m.refreshDue(ctx)
	if applies != 2 {
		t.Fatal("manual-only list auto-refreshed")
	}
	// The cache is persisted and visible to a new manager after restart.
	next := New(m.db, nil)
	lists, err := next.List()
	if err != nil || len(lists) != 1 || lists[0].SourceEntries != *body {
		t.Fatalf("cache not persisted: %v %v", lists, err)
	}
}

func TestSourceChangeAndInUseDeletion(t *testing.T) {
	m, source, body, _ := testManager(t)
	ctx := context.Background()
	list, err := m.Save(ctx, Input{Name: "shared", SourceURL: source.URL})
	if err != nil {
		t.Fatal(err)
	}
	*body = "not a list"
	if _, err := m.Save(ctx, Input{ID: list.ID, Name: "changed", SourceURL: source.URL + "/new"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	var saved models.AccessList
	m.db.First(&saved, list.ID)
	if saved.SourceURL != list.SourceURL || saved.Name != list.Name || saved.SourceEntries != list.SourceEntries {
		t.Fatal("failed edit replaced the original source")
	}
	lb := models.LoadBalancer{Name: "uses-shared", AccessDenyLists: []uint{list.ID}}
	if err := m.db.Create(&lb).Error; err != nil {
		t.Fatal(err)
	}
	if err := ValidateReferences(m.db, &lb); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(list.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("deleted in-use list: %v", err)
	}
	lb.AccessDenyLists = []uint{999999}
	if err := ValidateReferences(m.db, &lb); !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted missing reference: %v", err)
	}
	m.db.Unscoped().Delete(&lb)
	if err := m.Delete(list.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(list.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
