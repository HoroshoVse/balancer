package models

import (
	"time"
	"gorm.io/gorm"
)

type LoadBalancer struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
	
	Name      string `json:"name"`
	Protocol  string `json:"protocol"` // http, https, tcp
	ListenIP  string `json:"listen_ip"`
	ListenPort int   `json:"listen_port"`
	Algorithm string `json:"algorithm"` // round_robin, least_conn, etc.

	BackendGroupID uint          `json:"backend_group_id"`
	BackendGroup   BackendGroup  `json:"backend_group"`
	
	// TLS settings
	SSLEnabled bool   `json:"ssl_enabled"`
	ACMEEnabled bool  `json:"acme_enabled"` // Let's encrypt
	ACMEEmail   string `json:"acme_email"`
	CertPath    string `json:"cert_path"`
	KeyPath     string `json:"key_path"`
	
	// HTTP/3 (QUIC)
	HTTP3Enabled bool `json:"http3_enabled"`

	// Proxy Protocol
	ProxyProtocolEnabled bool `json:"proxy_protocol_enabled"`
	ProxyProtocolVersion int  `json:"proxy_protocol_version" gorm:"default:2"` // 1 or 2

	// Sticky Sessions
	StickySessionsEnabled bool   `json:"sticky_sessions_enabled"`
	StickySessionType     string `json:"sticky_session_type"` // ip, cookie

	// Runtime state
	Metrics map[string]interface{} `gorm:"-" json:"metrics,omitempty"`
}

type BackendGroup struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	Name      string         `json:"name"`
	Backends  []BackendServer `json:"backends" gorm:"foreignKey:GroupID"`
	
	// Health Check settings
	HCEnabled   bool   `json:"hc_enabled"`
	HCProtocol  string `json:"hc_protocol"` // http, tcp
	HCPath      string `json:"hc_path"`
	HCInterval  int    `json:"hc_interval"` // in seconds
	HCTimeout   int    `json:"hc_timeout"`
	HCFailureThreshold int `json:"hc_failure_threshold"`
	HCRecoveryThreshold int `json:"hc_recovery_threshold"`
}

type BackendServer struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	GroupID   uint           `json:"group_id"`
	Name      string         `json:"name"`
	Address   string         `json:"address"` // IP or Hostname
	Port      int            `json:"port"`
	Weight    int            `json:"weight" gorm:"default:1"`
	Enabled   bool           `json:"enabled" gorm:"default:true"`
	Backup    bool           `json:"backup" gorm:"default:false"` // Backup server
	MaxConns  int            `json:"max_conns" gorm:"default:0"`
	
	// State (Not persisted in DB, but tracked by HealthChecker)
	Status    string         `gorm:"-" json:"status"` // UP, DOWN, DISABLED, DRAIN
}

type User struct {
	ID       uint   `gorm:"primarykey" json:"id"`
	Username string `gorm:"uniqueIndex" json:"username"`
	Password string `json:"-"`
	Role     string `json:"role"` // Admin, Operator, Viewer
}
