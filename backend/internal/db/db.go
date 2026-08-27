package db

import (
	"strings"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"golang.org/x/crypto/bcrypt"
	"github.com/balancer/backend/internal/models"
)

func InitDB(dsn string) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "host=") {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	} else {
		// fallback to sqlite
		db, err = gorm.Open(sqlite.Open("balancer.db"), &gorm.Config{})
	}

	if err != nil {
		return nil, err
	}

	// Auto-migrate schema
	err = db.AutoMigrate(
		&models.LoadBalancer{},
		&models.BackendGroup{},
		&models.BackendServer{},
		&models.User{},
		&models.Settings{},
	)
	
	if err != nil {
		return nil, err
	}

	err = SeedDatabase(db)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func SeedDatabase(db *gorm.DB) error {
	var userCount int64
	db.Model(&models.User{}).Count(&userCount)
	if userCount == 0 {
		hashed, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		db.Create(&models.User{
			Username: "admin",
			Password: string(hashed),
			Role:     "Admin",
		})
	}

	var count int64
	db.Model(&models.LoadBalancer{}).Count(&count)
	if count > 0 {
		return nil // Already seeded
	}

	// Create a default Load Balancer with HTTP/3 and ACME enabled
	lb := models.LoadBalancer{
		Name:         "Main Ingress",
		ListenIP:     "0.0.0.0",
		ListenPort:   443,
		Protocol:     "https",
		Algorithm:    "round_robin",
		SSLEnabled:   true,
		ACMEEnabled:  false, // Keep false for local dev testing
		HTTP3Enabled: true,
		BackendGroup: models.BackendGroup{
			Name: "Production Cluster",
			Backends: []models.BackendServer{
				{
					Name:    "App Node 1",
					Address: "127.0.0.1",
					Port:    8081,
					Enabled: true,
				},
				{
					Name:    "App Node 2",
					Address: "127.0.0.1",
					Port:    8082,
					Enabled: true,
				},
			},
		},
	}

	return db.Create(&lb).Error
}
