package main

import (
	"log"
	"os"

	"github.com/balancer/backend/internal/api"
	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
)

func main() {
	log.Println("Starting Balancer...")

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

	// 2. Init Engine (Load Balancer & Health Checker)
	balancerEngine := engine.NewEngine(database)
	if err := balancerEngine.Start(); err != nil {
		log.Fatalf("Failed to start engine: %v", err)
	}

	// 3. Init API Server (for Web UI)
	apiServer := api.NewServer(database, balancerEngine)
	go func() {
		if err := apiServer.Start(":8080"); err != nil {
			log.Fatalf("API Server error: %v", err)
		}
	}()

	// Block forever
	select {}
}
