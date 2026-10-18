package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/models"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type testEnv struct {
	t      *testing.T
	db     *gorm.DB
	server *Server
	tokens map[string]string // role -> token
}

func newTestEnv(t *testing.T) *testEnv {
	t.Setenv("ADMIN_PASSWORD", "admin-test-password")
	database, err := db.InitDB("sqlite://" + filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.NewEngine(database)
	t.Cleanup(eng.Stop)

	env := &testEnv{t: t, db: database, server: NewServer(database, eng, []byte("0123456789abcdef0123456789abcdef")), tokens: map[string]string{}}
	for _, role := range []string{models.RoleOperator, models.RoleViewer} {
		hash, _ := bcrypt.GenerateFromPassword([]byte("pw-"+role), bcrypt.MinCost)
		database.Create(&models.User{Username: strings.ToLower(role), Password: string(hash), Role: role})
	}
	env.tokens[models.RoleAdmin] = env.login("admin", "admin-test-password")
	env.tokens[models.RoleOperator] = env.login("operator", "pw-Operator")
	env.tokens[models.RoleViewer] = env.login("viewer", "pw-Viewer")
	return env
}

func (e *testEnv) do(method, path, token string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "192.0.2.1:12345"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) login(user, password string) string {
	rec := e.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{user, password})
	if rec.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	var resp LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp.Token
}

func sampleLB(port int) models.LoadBalancer {
	return models.LoadBalancer{
		Name: "web", Protocol: "tcp", ListenIP: "127.0.0.1", ListenPort: port,
		BackendGroup: models.BackendGroup{Backends: []models.BackendServer{
			{Name: "a", Address: "127.0.0.1", Port: 9, Enabled: true},
			{Name: "b", Address: "127.0.0.1", Port: 10, Enabled: true},
		}},
	}
}

func (e *testEnv) createLB(lb models.LoadBalancer) uint {
	rec := e.do(http.MethodPost, "/api/v1/load-balancers", e.tokens[models.RoleOperator], lb)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var resp map[string]uint
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp["id"]
}

func TestLoginLockout(t *testing.T) {
	env := newTestEnv(t)
	for i := 0; i < maxLoginFailures; i++ {
		if rec := env.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{"admin", "wrong"}); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	// Locked out even with the right password
	if rec := env.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{"admin", "admin-test-password"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{"admin", "admin"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestTokensAndRoles(t *testing.T) {
	env := newTestEnv(t)

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": 1, "role": "Admin", "exp": time.Now().Add(time.Hour).Unix()})
	old, _ := forged.SignedString([]byte("super-secret-key-change-in-production"))
	if rec := env.do(http.MethodGet, "/api/v1/load-balancers", old, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token signed with the old hardcoded secret: %d", rec.Code)
	}

	viewer := env.tokens[models.RoleViewer]
	if rec := env.do(http.MethodGet, "/api/v1/load-balancers", viewer, nil); rec.Code != http.StatusOK {
		t.Fatalf("viewer GET: %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", viewer, sampleLB(18001)); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST: %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/api/v1/tools/test-connection", viewer, map[string]interface{}{"address": "127.0.0.1", "port": 1}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer test-connection: %d", rec.Code)
	}
	if rec := env.do(http.MethodGet, "/api/v1/settings", env.tokens[models.RoleOperator], nil); rec.Code != http.StatusForbidden {
		t.Fatalf("operator settings: %d", rec.Code)
	}

	// Deleting a user revokes their token immediately
	env.db.Where("username = ?", "viewer").Delete(&models.User{})
	if rec := env.do(http.MethodGet, "/api/v1/load-balancers", viewer, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user: %d", rec.Code)
	}
}

func TestSecretsAreWriteOnly(t *testing.T) {
	env := newTestEnv(t)
	admin := env.tokens[models.RoleAdmin]

	env.do(http.MethodPost, "/api/v1/settings/update", admin, map[string]interface{}{"telegram_bot_token": "123:secret", "telegram_chat_id": "42", "notifications_enabled": true})
	rec := env.do(http.MethodGet, "/api/v1/settings", admin, nil)
	if strings.Contains(rec.Body.String(), "123:secret") || !strings.Contains(rec.Body.String(), `"telegram_bot_token_set":true`) {
		t.Fatalf("settings leak or missing flag: %s", rec.Body)
	}
	// Saving with an empty token keeps the stored one
	env.do(http.MethodPost, "/api/v1/settings/update", admin, map[string]interface{}{"telegram_chat_id": "43", "notifications_enabled": false})
	settings, _ := db.LoadSettings(env.db)
	if settings.TelegramBotToken != "123:secret" || settings.TelegramChatID != "43" || settings.NotificationsEnabled {
		t.Fatalf("unexpected settings after update: %+v", settings)
	}

	lb := sampleLB(18002)
	lb.Protocol, lb.SSLEnabled = "https", true
	lb.CertData, lb.KeyData = "CERT", "PRIVATE-KEY"
	id := env.createLB(lb)
	rec = env.do(http.MethodGet, "/api/v1/load-balancers", admin, nil)
	if strings.Contains(rec.Body.String(), "PRIVATE-KEY") || !strings.Contains(rec.Body.String(), `"has_key_data":true`) {
		t.Fatalf("private key leaked: %s", rec.Body)
	}

	// An update without key_data keeps the stored key
	var lbs []models.LoadBalancer
	json.Unmarshal(rec.Body.Bytes(), &lbs)
	lbs[0].Name = "renamed"
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers", env.tokens[models.RoleOperator], lbs[0]); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	var stored models.LoadBalancer
	env.db.First(&stored, id)
	if stored.KeyData != "PRIVATE-KEY" || stored.Name != "renamed" {
		t.Fatalf("stored LB after update: name=%q key=%q", stored.Name, stored.KeyData)
	}
}

func TestUpdateKeepsBackendIDsAndDisabledState(t *testing.T) {
	env := newTestEnv(t)
	operator := env.tokens[models.RoleOperator]
	id := env.createLB(sampleLB(18003))

	var lb models.LoadBalancer
	env.db.Preload("BackendGroup.Backends").First(&lb, id)
	original := map[string]uint{}
	for _, b := range lb.BackendGroup.Backends {
		original[b.Name] = b.ID
	}

	// Disable "a", drop "b", add "c"; change LB-level settings too
	lb.IdleTimeout, lb.ConnectTimeout = 1234, 2500
	lb.BackendGroup.Backends[0].Enabled = false
	lb.BackendGroup.Backends = append(lb.BackendGroup.Backends[:1], models.BackendServer{Name: "c", Address: "127.0.0.1", Port: 11, Enabled: true})
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers", operator, lb); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}

	var after models.LoadBalancer
	env.db.Preload("BackendGroup.Backends").First(&after, id)
	if after.IdleTimeout != 1234 || after.ConnectTimeout != 2500 {
		t.Fatalf("timeouts not saved: idle=%d connect=%d", after.IdleTimeout, after.ConnectTimeout)
	}
	if len(after.BackendGroup.Backends) != 2 {
		t.Fatalf("expected 2 backends, got %+v", after.BackendGroup.Backends)
	}
	for _, b := range after.BackendGroup.Backends {
		switch b.Name {
		case "a":
			if b.ID != original["a"] || b.Enabled {
				t.Fatalf("backend a: id %d (was %d), enabled %v", b.ID, original["a"], b.Enabled)
			}
		case "c":
			if b.ID == original["b"] {
				t.Fatal("new backend reused a deleted ID")
			}
		default:
			t.Fatalf("unexpected backend %q", b.Name)
		}
	}
}

func TestSystemInfo(t *testing.T) {
	env := newTestEnv(t)
	env.server.SetInfo(SystemInfo{Version: "test", APIAddr: "127.0.0.1:8080"})

	if rec := env.do(http.MethodGet, "/api/v1/system", env.tokens[models.RoleOperator], nil); rec.Code != http.StatusForbidden {
		t.Fatalf("operator: %d", rec.Code)
	}
	rec := env.do(http.MethodGet, "/api/v1/system", env.tokens[models.RoleAdmin], nil)
	var info map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &info)
	if rec.Code != http.StatusOK || info["api_loopback"] != true || info["default_admin_password"] != false || info["db_driver"] != "sqlite" {
		t.Fatalf("system info: %d %s", rec.Code, rec.Body)
	}
}

func TestValidationAndPortConflicts(t *testing.T) {
	env := newTestEnv(t)
	operator := env.tokens[models.RoleOperator]

	bad := sampleLB(0)
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", operator, bad); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "listen port") {
		t.Fatalf("invalid LB: %d %s", rec.Code, rec.Body)
	}

	env.createLB(sampleLB(18004))
	clash := sampleLB(18004)
	clash.ListenIP = "0.0.0.0"
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", operator, clash); rec.Code != http.StatusConflict {
		t.Fatalf("port conflict: %d %s", rec.Code, rec.Body)
	}
	udp := sampleLB(18004)
	udp.Protocol = "udp"
	if rec := env.do(http.MethodPost, "/api/v1/load-balancers", operator, udp); rec.Code != http.StatusCreated {
		t.Fatalf("udp on the same port number should be allowed: %d %s", rec.Code, rec.Body)
	}
}

func changePassword(current, next string) ChangePasswordRequest {
	return ChangePasswordRequest{CurrentPassword: current, NewPassword: next}
}

func TestChangePassword(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.tokens[models.RoleViewer]

	// Another session of the same user, e.g. a second browser
	otherSession := env.login("viewer", "pw-Viewer")

	rec := env.do(http.MethodPost, "/api/v1/auth/password", viewer, changePassword("pw-Viewer", "new-viewer-pw"))
	if rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	var resp LoginResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Token == "" {
		t.Fatal("no new token in the response")
	}

	// The caller continues with the new token; every older token is revoked
	if rec := env.do(http.MethodGet, "/api/v1/auth/me", resp.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("new token: %d", rec.Code)
	}
	for name, token := range map[string]string{"caller's old token": viewer, "other session": otherSession} {
		if rec := env.do(http.MethodGet, "/api/v1/auth/me", token, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s after password change: %d", name, rec.Code)
		}
	}

	if rec := env.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{"viewer", "pw-Viewer"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("login with the old password: %d", rec.Code)
	}
	env.login("viewer", "new-viewer-pw")

	// Other users' tokens are untouched
	if rec := env.do(http.MethodGet, "/api/v1/auth/me", env.tokens[models.RoleAdmin], nil); rec.Code != http.StatusOK {
		t.Fatalf("admin token after viewer's change: %d", rec.Code)
	}
}

func TestChangePasswordValidation(t *testing.T) {
	env := newTestEnv(t)
	operator := env.tokens[models.RoleOperator]

	if rec := env.do(http.MethodPost, "/api/v1/auth/password", "", changePassword("pw-Operator", "long-enough-pw")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec := env.do(http.MethodGet, "/api/v1/auth/password", operator, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}

	for name, req := range map[string]ChangePasswordRequest{
		"too short":            changePassword("pw-Operator", "short12"),
		"too short (runes)":    changePassword("pw-Operator", "пароль7"),
		"too long":             changePassword("pw-Operator", strings.Repeat("x", maxPasswordBytes+1)),
		"same as current":      changePassword("pw-Operator", "pw-Operator"),
		"short, wrong current": changePassword("wrong", "short"),
	} {
		if rec := env.do(http.MethodPost, "/api/v1/auth/password", operator, req); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	// Nothing changed: the old password and token still work
	if rec := env.do(http.MethodGet, "/api/v1/auth/me", operator, nil); rec.Code != http.StatusOK {
		t.Fatalf("token after rejected changes: %d", rec.Code)
	}
	env.login("operator", "pw-Operator")
}

func TestChangePasswordLockout(t *testing.T) {
	env := newTestEnv(t)
	viewer := env.tokens[models.RoleViewer]

	for i := 0; i < maxLoginFailures; i++ {
		// 403 rather than 401, so the UI doesn't sign the user out over a typo
		if rec := env.do(http.MethodPost, "/api/v1/auth/password", viewer, changePassword("wrong", "long-enough-pw")); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	// Locked out even with the right current password, and the session survives
	if rec := env.do(http.MethodPost, "/api/v1/auth/password", viewer, changePassword("pw-Viewer", "long-enough-pw")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if rec := env.do(http.MethodGet, "/api/v1/auth/me", viewer, nil); rec.Code != http.StatusOK {
		t.Fatalf("session after lockout: %d", rec.Code)
	}
	// The failures count toward the login lockout of the same IP
	if rec := env.do(http.MethodPost, "/api/v1/auth/login", "", LoginRequest{"admin", "admin-test-password"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login from the same IP: expected 429, got %d", rec.Code)
	}

	// ...and to the account itself, whatever IP the token is used from
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/password", strings.NewReader(`{"current_password":"pw-Viewer","new_password":"long-enough-pw"}`))
	req.RemoteAddr = "198.51.100.7:4242"
	req.Header.Set("Authorization", "Bearer "+viewer)
	rec := httptest.NewRecorder()
	env.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same account from another IP: expected 429, got %d", rec.Code)
	}

	var user models.User
	env.db.Where("username = ?", "viewer").First(&user)
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("pw-Viewer")) != nil {
		t.Fatal("password changed despite the lockout")
	}
}

func TestTokensWithoutVersionStillWork(t *testing.T) {
	env := newTestEnv(t)
	var admin models.User
	env.db.Where("username = ?", "admin").First(&admin)

	// Tokens issued before token_version existed must survive the upgrade
	legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": admin.ID, "username": "admin", "role": "Admin", "exp": time.Now().Add(time.Hour).Unix()})
	token, _ := legacy.SignedString([]byte("0123456789abcdef0123456789abcdef"))
	if rec := env.do(http.MethodGet, "/api/v1/auth/me", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("legacy token: %d", rec.Code)
	}
}

func TestHostMetrics(t *testing.T) {
	env := newTestEnv(t)

	if rec := env.do(http.MethodGet, "/api/v1/metrics/host", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	rec := env.do(http.MethodGet, "/api/v1/metrics/host", env.tokens[models.RoleViewer], nil)
	var stats engine.HostStats
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &stats) != nil || !strings.Contains(rec.Body.String(), `"available"`) {
		t.Fatalf("viewer: %d %s", rec.Code, rec.Body)
	}
}

func TestBackupOrderAndMode(t *testing.T) {
	env := newTestEnv(t)
	operator := env.tokens[models.RoleOperator]

	lb := sampleLB(18005)
	lb.BackupMode = models.BackupOrdered
	lb.BackendGroup.Backends = append(lb.BackendGroup.Backends, models.BackendServer{Name: "c", Address: "127.0.0.1", Port: 11, Enabled: true, Backup: true})
	lb.BackendGroup.Backends[1].Backup = true
	id := env.createLB(lb)

	names := func() string {
		var lbs []models.LoadBalancer
		json.Unmarshal(env.do(http.MethodGet, "/api/v1/load-balancers", operator, nil).Body.Bytes(), &lbs)
		var out []string
		for _, b := range lbs[0].BackendGroup.Backends {
			out = append(out, b.Name)
		}
		return lbs[0].BackupMode + ":" + strings.Join(out, ",")
	}
	if got := names(); got != "ordered:a,b,c" {
		t.Fatalf("after create: %s", got)
	}

	// Reordering keeps the nodes (and their IDs) but changes who backs up first
	var stored models.LoadBalancer
	env.db.Preload("BackendGroup.Backends", models.OrderedBackends).First(&stored, id)
	nodes := stored.BackendGroup.Backends
	stored.BackendGroup.Backends = []models.BackendServer{nodes[2], nodes[1], nodes[0]}
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers", operator, stored); rec.Code != http.StatusOK {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body)
	}
	if got := names(); got != "ordered:c,b,a" {
		t.Fatalf("after reorder: %s", got)
	}

	stored.BackupMode = "random"
	if rec := env.do(http.MethodPut, "/api/v1/load-balancers", operator, stored); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "backup mode") {
		t.Fatalf("invalid backup mode: %d %s", rec.Code, rec.Body)
	}
}

func TestTelegramTestNeedsTokenAndChat(t *testing.T) {
	env := newTestEnv(t)
	if rec := env.do(http.MethodPost, "/api/v1/settings/telegram/test", env.tokens[models.RoleOperator], map[string]string{}); rec.Code != http.StatusForbidden {
		t.Fatalf("operator: %d", rec.Code)
	}
	rec := env.do(http.MethodPost, "/api/v1/settings/telegram/test", env.tokens[models.RoleAdmin], map[string]string{"telegram_chat_id": "42"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no token saved or given: %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(http.MethodGet, "/api/v1/settings", env.tokens[models.RoleAdmin], nil); strings.Contains(rec.Body.String(), "telegram_last") {
		t.Fatalf("no alert was sent yet: %s", rec.Body)
	}
}
