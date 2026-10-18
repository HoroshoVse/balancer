package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/balancer/backend/internal/models"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	maxLoginFailures = 5
	loginLockout     = 15 * time.Minute

	minPasswordLength = 8  // characters
	maxPasswordBytes  = 72 // bcrypt ignores anything longer
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type ctxUserKey struct{}

func mustDummyHash() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return hash
}

// clientIP returns the caller's address. Requests proxied by the bundled
// nginx arrive from loopback and carry the real address in X-Real-IP.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
	}
	return host
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}

	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		w.Header().Set("Retry-After", strconv.Itoa(int(loginLockout.Seconds())))
		http.Error(w, "Too many failed login attempts, try again later", http.StatusTooManyRequests)
		return
	}

	var req LoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var user models.User
	if err := s.db.Where("username = ?", req.Username).Limit(1).Find(&user).Error; err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	hash := []byte(user.Password)
	if user.ID == 0 {
		hash = s.dummyHash
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(req.Password)); err != nil || user.ID == 0 {
		s.limiter.fail(ip)
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}
	s.limiter.reset(ip)
	s.writeToken(w, &user)
}

// writeToken responds with a fresh 24-hour token for user.
func (s *Server) writeToken(w http.ResponseWriter, user *models.User) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":       user.ID,
		"username":      user.Username,
		"role":          user.Role,
		"token_version": user.TokenVersion,
		"iat":           time.Now().Unix(),
		"exp":           time.Now().Add(time.Hour * 24).Unix(),
	})

	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		http.Error(w, "Failed to generate token", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, LoginResponse{Token: tokenString})
}

// authed validates the JWT, loads the user (so deleted users and role
// changes take effect immediately) and requires at least minRole.
// Tokens issued before the user's last password change are rejected.
func (s *Server) authed(minRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Missing Authorization header", http.StatusUnauthorized)
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid Authorization header", http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(parts[1], func(token *jwt.Token) (interface{}, error) {
			return s.jwtSecret, nil
		}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
		if err != nil || !token.Valid {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}

		claims, _ := token.Claims.(jwt.MapClaims)
		userID, ok := claims["user_id"].(float64)
		if !ok {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}
		var user models.User
		if err := s.db.Limit(1).Find(&user, uint(userID)).Error; err != nil || user.ID == 0 {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}
		// Tokens from before this claim existed carry none and count as version 0
		version, _ := claims["token_version"].(float64)
		if uint(version) != user.TokenVersion {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return
		}
		if models.RoleRank(user.Role) < models.RoleRank(minRole) {
			http.Error(w, "Forbidden: requires role "+minRole, http.StatusForbidden)
			return
		}

		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserKey{}, &user)))
	}
}

func currentUser(r *http.Request) *models.User {
	user, _ := r.Context().Value(ctxUserKey{}).(*models.User)
	return user
}

// requireRole checks the authenticated user's role inside a handler.
func requireRole(w http.ResponseWriter, r *http.Request, role string) bool {
	if user := currentUser(r); user == nil || models.RoleRank(user.Role) < models.RoleRank(role) {
		http.Error(w, "Forbidden: requires role "+role, http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username, "role": user.Role})
}

// changePassword lets the signed-in user set a new password. Wrong current
// passwords count toward the login lockout for the caller's IP and for the
// account, so a stolen token can't be used to guess the password. On success
// every other session of the user is signed out and the caller gets a new token.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}

	user := currentUser(r)
	limitKeys := []string{clientIP(r), "user:" + strconv.FormatUint(uint64(user.ID), 10)}
	for _, key := range limitKeys {
		if !s.limiter.allow(key) {
			w.Header().Set("Retry-After", strconv.Itoa(int(loginLockout.Seconds())))
			http.Error(w, "Too many failed attempts, try again later", http.StatusTooManyRequests)
			return
		}
	}

	var req ChangePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	switch {
	case utf8.RuneCountInString(req.NewPassword) < minPasswordLength:
		http.Error(w, fmt.Sprintf("New password must be at least %d characters", minPasswordLength), http.StatusBadRequest)
		return
	case len(req.NewPassword) > maxPasswordBytes:
		http.Error(w, fmt.Sprintf("New password must be at most %d bytes", maxPasswordBytes), http.StatusBadRequest)
		return
	case req.NewPassword == req.CurrentPassword:
		http.Error(w, "New password must differ from the current one", http.StatusBadRequest)
		return
	}

	// 403, not 401: the session is valid, and the UI signs out on 401
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.CurrentPassword)); err != nil {
		for _, key := range limitKeys {
			s.limiter.fail(key)
		}
		http.Error(w, "Current password is incorrect", http.StatusForbidden)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.Model(user).Updates(map[string]interface{}{
		"password":      string(hash),
		"token_version": gorm.Expr("token_version + 1"),
	}).Error; err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if err := s.db.First(user, user.ID).Error; err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	for _, key := range limitKeys {
		s.limiter.reset(key)
	}
	s.writeToken(w, user)
}

// loginLimiter locks an IP out after repeated failed logins.
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

type loginEntry struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]*loginEntry)}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	return e == nil || time.Now().After(e.lockedUntil)
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e := l.entries[ip]
	if e == nil || now.Sub(e.lastFailure) > loginLockout {
		e = &loginEntry{}
		l.entries[ip] = e
	}
	e.failures++
	e.lastFailure = now
	if e.failures >= maxLoginFailures {
		e.lockedUntil = now.Add(loginLockout)
		e.failures = 0
	}

	if len(l.entries) > 10000 {
		for k, v := range l.entries {
			if now.Sub(v.lastFailure) > loginLockout && now.After(v.lockedUntil) {
				delete(l.entries, k)
			}
		}
	}
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}
