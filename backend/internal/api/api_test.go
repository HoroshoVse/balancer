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
