package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/geo"
	"github.com/balancer/backend/internal/models"
)

// geoEnv is a test environment with a small country database on disk.
func geoEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	dir := t.TempDir()
	file := func(reg string, recs ...string) {
		body := fmt.Sprintf("2|%s|1|%d|19700101|20261023|+0000\n", reg, len(recs))
		for _, r := range recs {
			body += reg + "|" + r + "\n"
		}
		if err := os.WriteFile(filepath.Join(dir, reg+".txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file("ripencc", "RU|ipv4|5.255.0.0|65536|20200101|allocated", "HK|ipv4|91.204.224.0|1024|20200101|allocated")
	file("arin", "US|ipv4|103.54.44.0|2048|20200101|allocated", "US|ipv4|8.8.8.0|256|20200101|allocated")
	m := geo.NewManager(dir, []geo.Source{{Name: "ripencc", URL: "https://example.invalid/a"}, {Name: "arin", URL: "https://example.invalid/b"}}, nil)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	env.server.SetGeo(m)
	old := engine.GeoIP
	engine.GeoIP = m
	t.Cleanup(func() { engine.GeoIP = old })
	return env
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v in %s", err, rec.Body)
	}
	return v
}

func TestGeoStatusAndSettings(t *testing.T) {
	env := geoEnv(t)
	admin, operator, viewer := env.tokens[models.RoleAdmin], env.tokens[models.RoleOperator], env.tokens[models.RoleViewer]

	if rec := env.do(http.MethodGet, "/api/v1/geo", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", rec.Code)
	}
	rec := env.do(http.MethodGet, "/api/v1/geo", viewer, nil) // everyone who edits balancers needs the country list
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer: %d %s", rec.Code, rec.Body)
	}
	st := decode[geoResponse](t, rec)
	if !st.Loaded || st.Countries != 3 || strings.Join(st.CountryCodes, ",") != "HK,RU,US" || !st.AutoUpdate || st.IntervalHours != 24 || len(st.Sources) != 2 {
		t.Fatalf("status %+v", st)
	}

	// Only admins change how the database is kept
	for _, path := range []string{"/api/v1/geo/update", "/api/v1/geo/settings"} {
		if rec := env.do(http.MethodPost, path, operator, map[string]any{"auto_update": true, "interval_hours": 24}); rec.Code != http.StatusForbidden {
			t.Fatalf("operator on %s: %d", path, rec.Code)
		}
	}
	if rec := env.do(http.MethodPost, "/api/v1/geo/settings", admin, map[string]any{"auto_update": true, "interval_hours": 1}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a 1-hour interval: %d", rec.Code)
	}
	rec = env.do(http.MethodPost, "/api/v1/geo/settings", admin, map[string]any{"auto_update": false, "interval_hours": 48})
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	if st := decode[geoResponse](t, rec); st.AutoUpdate || st.IntervalHours != 48 {
		t.Fatalf("answer %+v", st)
	}
	if auto, every := db.GeoSchedule(env.db); auto || every.Hours() != 48 {
		t.Fatalf("not saved: %v %v", auto, every)
	}
}

func TestGeoUnavailableWithoutManager(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/api/v1/geo", "/api/v1/geo/lookup?ip=8.8.8.8"} {
		if rec := env.do(http.MethodGet, path, env.tokens[models.RoleViewer], nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func TestGeoLookupAndBalancerDecision(t *testing.T) {
	env := geoEnv(t)
	viewer, operator := env.tokens[models.RoleViewer], env.tokens[models.RoleOperator]

	lb := sampleLB(18201)
	lb.AccessMode, lb.AccessCountries, lb.AccessDeny = models.AccessOnly, "ru", "5.255.9.9"
	id := env.createLB(lb)

	get := func(q string) (*httptest.ResponseRecorder, lookupResponse) {
		rec := env.do(http.MethodGet, "/api/v1/geo/lookup?"+q, viewer, nil)
		var res lookupResponse
		json.Unmarshal(rec.Body.Bytes(), &res)
		return rec, res
	}

	if _, r := get("ip=5.255.1.1"); r.Country != "RU" || !r.Loaded || r.Internal {
		t.Fatalf("%+v", r)
	}
	if _, r := get("ip=10.0.1.8"); !r.Internal || r.Country != "" {
		t.Fatalf("%+v", r)
	}
	if rec, _ := get("ip=not-an-ip"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ip: %d", rec.Code)
	}
	if rec, _ := get(fmt.Sprintf("ip=8.8.8.8&lb=%d", id+100)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown balancer: %d", rec.Code)
	}

	for _, c := range []struct {
		ip      string
		allowed bool
		reason  string
	}{
		{"5.255.1.1", true, engine.ReasonCountryOK},
		{"103.54.46.15", false, engine.ReasonCountryDenied}, // US here
		{"91.204.224.105", false, engine.ReasonCountryDenied},
		{"5.255.9.9", false, engine.ReasonDenyList}, // Russian, but on the deny list
		{"192.0.2.77", false, engine.ReasonUnknown},
	} {
		_, r := get(fmt.Sprintf("ip=%s&lb=%d", c.ip, id))
		if r.Decision == nil || r.Decision.Allowed != c.allowed || r.Decision.Reason != c.reason {
			t.Errorf("%s: %+v", c.ip, r.Decision)
		}
	}

	// Without ip it describes the caller, as nginx reports them
	req := httptest.NewRequest(http.MethodGet, "/api/v1/geo/lookup", nil)
	req.Header.Set("Authorization", "Bearer "+operator)
	req.Header.Set("X-Real-IP", "8.8.8.8")
	rec := httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	if r := decode[lookupResponse](t, rec); !r.You || r.IP != "8.8.8.8" || r.Country != "US" {
		t.Fatalf("caller: %+v", r)
	}
}

func TestBalancerAccessRulesAreValidatedAndSaved(t *testing.T) {
	env := geoEnv(t)
	operator := env.tokens[models.RoleOperator]

	bad := sampleLB(18211)
	bad.AccessMode = models.AccessOnly // no countries, no allowed addresses: nobody could connect
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", operator, bad); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nobody could connect") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	bad = sampleLB(18211)
	bad.AccessDeny = "999.1.1.1"
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", operator, bad); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "deny list") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}

	lb := sampleLB(18211)
	lb.AccessMode, lb.AccessCountries, lb.AccessAllow = " Deny", "us, hk, US", "203.0.113.5\n203.0.113.5 2001:DB8::/32"
	id := env.createLB(lb)
	saved := func() models.LoadBalancer {
		var got models.LoadBalancer
		env.db.First(&got, id)
		return got
	}
	if g := saved(); g.AccessMode != "deny" || g.AccessCountries != "HK,US" || g.AccessAllow != "203.0.113.5\n2001:db8::/32" {
		t.Fatalf("stored %+v", g)
	}

	// Editing carries the rules over, and clearing them works too
	edit := lb
	edit.ID = id
	edit.AccessMode, edit.AccessCountries, edit.AccessAllow, edit.AccessDeny = models.AccessOnly, "RU", "", "198.51.100.0/24"
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers/update", operator, edit); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if g := saved(); g.AccessMode != "allow" || g.AccessCountries != "RU" || g.AccessAllow != "" || g.AccessDeny != "198.51.100.0/24" {
		t.Fatalf("after edit %+v", g)
	}
	edit.AccessMode, edit.AccessCountries, edit.AccessDeny = "", "", ""
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers/update", operator, edit); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if g := saved(); g.AccessMode != "" || g.AccessCountries != "" || g.AccessDeny != "" {
		t.Fatalf("after clearing %+v", g)
	}
}
