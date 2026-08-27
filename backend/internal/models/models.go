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
	SSLEnabled  bool   `json:"ssl_enabled"`
	AutoSSL     bool   `json:"auto_ssl"`
	CertPath    string `json:"cert_path"`
	KeyPath     string `json:"key_path"`
	CertData    string `json:"cert_data" gorm:"type:text"`
	KeyData     string `json:"key_data" gorm:"type:text"`
	ACMEEnabled bool   `json:"acme_enabled"`
	ACMEEmail   string `json:"acme_email"`
	ACMEDomains string `json:"acme_domains"` // Comma separated list of domains
	ACMEStatus  string `json:"acme_status" gorm:"-"` // issuing, ok, error (not saved in db)
	ACMEError   string `json:"acme_error" gorm:"-"` // error message if any (not saved in db)
	
	// HTTP/3 (QUIC) for client connections
	HTTP3Enabled bool `json:"http3_enabled"`
	
	// HTTP/2 to backend
	BackendHTTP2Enabled bool `json:"backend_http2_enabled"`

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
	MaxConns  int            `json:"max_conns"` // Maximum concurrent connections
	TLSEnabled bool          `json:"tls_enabled"` // proxy to backend via HTTPS
	// Health Check settings
	HCEnabled   bool   `json:"hc_enabled"`
	HCProtocol  string `json:"hc_protocol"` // http, tcp
	HCPort      int    `json:"hc_port"`     // Optional override port for HC
	HCPath      string `json:"hc_path"`
	HCInterval  int    `json:"hc_interval"` // in seconds
	HCTimeout   int    `json:"hc_timeout"`
	HCFailureThreshold int `json:"hc_failure_threshold"`
	HCRecoveryThreshold int `json:"hc_recovery_threshold"`
	
	// State (Not persisted in DB, but tracked by HealthChecker)
	Status    string         `gorm:"-" json:"status"` // UP, DOWN, DISABLED, DRAIN
}

type User struct {
	ID       uint   `gorm:"primarykey" json:"id"`
	Username string `gorm:"uniqueIndex" json:"username"`
	Password string `json:"-"`
	Role     string `json:"role"` // Admin, Operator, Viewer
}

type Settings struct {
	ID                uint   `gorm:"primarykey" json:"id"`
	TelegramBotToken  string `json:"telegram_bot_token"`
	TelegramChatID    string `json:"telegram_chat_id"`
	NotificationsEnabled bool `json:"notifications_enabled" gorm:"default:false"`
}
