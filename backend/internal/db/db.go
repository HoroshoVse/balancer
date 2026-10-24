package db

import (
	"time"
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strings"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"golang.org/x/crypto/bcrypt"
	"github.com/balancer/backend/internal/models"
)

// InitDB opens Postgres for postgres:// or host= DSNs and SQLite otherwise
// (DSN "sqlite://path/to/file.db" or a plain file path; default "balancer.db").
func InitDB(dsn string) (*gorm.DB, error) {
	var db *gorm.DB
	var err error

	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "host=") {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	} else {
		path := strings.TrimPrefix(dsn, "sqlite://")
		if path == "" {
			path = "balancer.db"
		}
		db, err = gorm.Open(sqlite.Open(path), &gorm.Config{})
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

	if err := runMigrations(db); err != nil {
		return nil, err
	}

	err = SeedDatabase(db)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// LoadSettings returns the single settings row, creating it if needed.
func LoadSettings(db *gorm.DB) (*models.Settings, error) {
	var settings models.Settings
	if err := db.Order("id").Limit(1).Find(&settings).Error; err != nil {
		return nil, err
	}
	if settings.ID == 0 {
		if err := db.Create(&settings).Error; err != nil {
			return nil, err
		}
	}
	return &settings, nil
}

// runMigrations applies one-off data migrations tracked by Settings.SchemaVersion.
func runMigrations(db *gorm.DB) error {
	settings, err := LoadSettings(db)
	if err != nil {
		return err
	}

	if settings.SchemaVersion < 1 {
		// HTTPS load balancers used to talk TLS to every backend regardless of
		// the per-node tls_enabled flag. Make that explicit now that the flag is honoured.
		https := db.Model(&models.LoadBalancer{}).Select("backend_group_id").Where("protocol = ? AND ssl_enabled = ?", "https", true)
		if err := db.Model(&models.BackendServer{}).Where("group_id IN (?)", https).Update("tls_enabled", true).Error; err != nil {
			return err
		}
		settings.SchemaVersion = 1
	}

	return db.Model(settings).Update("schema_version", settings.SchemaVersion).Error
}

// SeedDatabase creates the initial admin user on an empty database.
// The password comes from ADMIN_PASSWORD or is generated and printed to the log once.
func SeedDatabase(db *gorm.DB) error {
	var userCount int64
	if err := db.Model(&models.User{}).Count(&userCount).Error; err != nil {
		return err
	}
	if userCount > 0 {
		return nil
	}

	password := os.Getenv("ADMIN_PASSWORD")
	generated := password == ""
	if generated {
		var err error
		if password, err = randomHex(12); err != nil {
			return err
		}
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := db.Create(&models.User{
		Username: "admin",
		Password: string(hashed),
		Role:     models.RoleAdmin,
	}).Error; err != nil {
		return err
	}

	if generated {
		log.Printf("Created initial user 'admin' with generated password: %s", password)
		log.Printf("Change it with: balancer-cli users passwd admin <new_password>")
	} else {
		log.Printf("Created initial user 'admin' with the password from ADMIN_PASSWORD")
	}
	return nil
}

// HasDefaultAdminPassword reports whether user 'admin' still has the
// password 'admin' that older versions seeded.
func HasDefaultAdminPassword(db *gorm.DB) bool {
	var admin models.User
	if err := db.Where("username = ?", "admin").Limit(1).Find(&admin).Error; err != nil || admin.ID == 0 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte("admin")) == nil
}

// EnsureJWTSecret returns the JWT signing secret stored in settings,
// generating and persisting a random one on first use.
func EnsureJWTSecret(db *gorm.DB) ([]byte, error) {
	settings, err := LoadSettings(db)
	if err != nil {
		return nil, err
	}
	if settings.JWTSecret != "" {
		return []byte(settings.JWTSecret), nil
	}

	secret, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	if err := db.Model(settings).Update("jwt_secret", secret).Error; err != nil {
		return nil, err
	}
	return []byte(secret), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GeoSchedule reads how the country database is kept up to date: whether
// automatic updates are on, and how often.
func GeoSchedule(db *gorm.DB) (auto bool, every time.Duration) {
	settings, err := LoadSettings(db)
	if err != nil {
		return true, 24 * time.Hour
	}
	hours := settings.GeoUpdateHours
	if hours <= 0 {
		hours = DefaultGeoUpdateHours
	}
	return !settings.GeoUpdateOff, time.Duration(hours) * time.Hour
}

// Bounds of the update interval, in hours.
const (
	DefaultGeoUpdateHours = 24
	MinGeoUpdateHours     = 6
	MaxGeoUpdateHours     = 24 * 30
)
