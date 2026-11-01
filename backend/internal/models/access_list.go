package models

import "time"

// Entries are edited by the user; SourceEntries is the last good download.
// Keeping them separate lets a failed download retain the active rules.
type AccessList struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	Name          string     `json:"name"`
	Entries       string     `gorm:"type:text" json:"entries"`
	SourceURL     string     `json:"source_url"`
	SourceEntries string     `gorm:"type:text" json:"-"`
	UpdateHours   int        `json:"update_hours"` // 0 = manual updates only
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastUpdatedAt *time.Time `json:"last_updated_at"`
	LastError     string     `json:"last_error"`
	EntryCount    int        `json:"entry_count"`
}
