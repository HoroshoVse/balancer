package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/balancer/backend/internal/api"
	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
	"gorm.io/gorm"
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

func main() {
	log.Printf("Starting Balancer %s...", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Init Database
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://balancer:balancer@localhost:5432/balancer?sslmode=disable"
		log.Println("DB_DSN not set, using default for local dev")
	}

	database, err := db.InitDB(dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	jwtSecret, err := loadJWTSecret(database)
	if err != nil {
		log.Fatalf("Failed to load JWT secret: %v", err)
	}

	if db.HasDefaultAdminPassword(database) {
		log.Println("WARNING: user 'admin' still has the default password 'admin'. Change it: balancer-cli users passwd admin <new_password>")
	}

	// 2. Init Engine (Load Balancer & Health Checker)
	balancerEngine := engine.NewEngine(database)
	if err := balancerEngine.Start(); err != nil {
		log.Fatalf("Failed to start engine: %v", err)
	}

	// 3. Init API Server (for Web UI). Loopback by default: the UI's nginx
	// proxies /api to it. Set API_ADDR=0.0.0.0:8080 to expose it directly.
	apiAddr := os.Getenv("API_ADDR")
	if apiAddr == "" {
		apiAddr = "127.0.0.1:8080"
	}
	apiServer := api.NewServer(database, balancerEngine, jwtSecret)
	apiServer.SetInfo(api.SystemInfo{Version: version, APIAddr: apiAddr, JWTFromEnv: os.Getenv("JWT_SECRET") != ""})
	apiErr := make(chan error, 1)
	go func() { apiErr <- apiServer.Start(ctx, apiAddr) }()

	select {
	case <-ctx.Done():
		log.Println("Shutting down...")
		<-apiErr
	case err := <-apiErr:
		log.Printf("API Server error: %v", err)
	}

	balancerEngine.Stop()
	log.Println("Stopped")
}

// loadJWTSecret prefers JWT_SECRET from the environment and otherwise
// falls back to a random secret persisted in the database.
func loadJWTSecret(database *gorm.DB) ([]byte, error) {
	if secret := os.Getenv("JWT_SECRET"); secret != "" {
		if len(secret) < 32 {
			return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters long")
		}
		return []byte(secret), nil
	}
	return db.EnsureJWTSecret(database)
}
