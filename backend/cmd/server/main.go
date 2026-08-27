package main

import (
	"log"
	"os"

	"github.com/balacer/backend/internal/api"
	"github.com/balacer/backend/internal/db"
	"github.com/balacer/backend/internal/engine"
)

func main() {
	log.Println("Starting Balacer...")

	// 1. Init Database
	dsn := os.Getenv("DB_DSN")
	if dsn == "" {
		dsn = "postgres://balacer:balacer@localhost:5432/balacer?sslmode=disable"
		log.Println("DB_DSN not set, using default for local dev")
	}
	
	database, err := db.InitDB(dsn)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	// 2. Init Engine (Load Balancer & Health Checker)
	balacerEngine := engine.NewEngine(database)
	if err := balacerEngine.Start(); err != nil {
		log.Fatalf("Failed to start engine: %v", err)
	}

	// 3. Init API Server (for Web UI)
	apiServer := api.NewServer(database, balacerEngine)
	go func() {
		if err := apiServer.Start(":8080"); err != nil {
			log.Fatalf("API Server error: %v", err)
		}
	}()

	// Block forever
	select {}
}
