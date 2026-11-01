package accesslists

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/balancer/backend/internal/models"
	"gorm.io/gorm"
)

var ErrInvalid = errors.New("invalid address list")
var ErrNotFound = errors.New("address list not found")
var ErrInUse = errors.New("address list is in use")

type Input struct {
	ID          uint   `json:"id"`
	Name        string `json:"name"`
	Entries     string `json:"entries"`
	SourceURL   string `json:"source_url"`
	UpdateHours int    `json:"update_hours"`
}

type Manager struct {
	db      *gorm.DB
	mu      sync.Mutex
	client  *http.Client
	changed func() error
}

func New(database *gorm.DB, changed func() error) *Manager {
	return &Manager{db: database, client: newHTTPClient(), changed: changed}
}

// LockConfig serializes list deletion against balancers selecting that list.
func (m *Manager) LockConfig() func() { m.mu.Lock(); return m.mu.Unlock }

func (m *Manager) List() ([]models.AccessList, error) {
	lists := []models.AccessList{}
	err := m.db.Order("id").Find(&lists).Error
	return lists, err
}

func ValidateReferences(tx *gorm.DB, lb *models.LoadBalancer) error {
	for _, ids := range [][]uint{lb.AccessAllowLists, lb.AccessDenyLists} {
		for _, id := range ids {
			var n int64
			if err := tx.Model(&models.AccessList{}).Where("id = ?", id).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("%w: список #%d не найден", ErrInvalid, id)
			}
		}
	}
	return nil
}

func (m *Manager) apply() error {
	if m.changed != nil {
		return m.changed()
	}
	return nil
}

func (m *Manager) Save(ctx context.Context, in Input) (*models.AccessList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in.Name, in.SourceURL = strings.TrimSpace(in.Name), strings.TrimSpace(in.SourceURL)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return nil, fmt.Errorf("%w: название должно содержать от 1 до 100 символов", ErrInvalid)
	}
	if in.UpdateHours < 0 || in.UpdateHours > 720 {
		return nil, fmt.Errorf("%w: интервал обновления — от 1 до 720 часов, 0 — вручную", ErrInvalid)
	}
	entries, _, err := Parse(in.Entries)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if entries == "" && in.SourceURL == "" {
		return nil, fmt.Errorf("%w: добавьте адреса или ссылку на список", ErrInvalid)
	}
	var list models.AccessList
	if in.ID != 0 {
		if err := m.db.First(&list, in.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
	} else {
		var n int64
		if err := m.db.Model(&models.AccessList{}).Count(&n).Error; err != nil {
			return nil, err
		}
		if n >= 100 {
			return nil, fmt.Errorf("%w: можно создать не больше 100 списков", ErrInvalid)
		}
	}
	if in.SourceURL != "" {
		if err := ValidateURL(in.SourceURL); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		// Changing the source requires a successful first download. Cached rules
		// from the previous source must never masquerade as the new source's data.
		if in.SourceURL != list.SourceURL || list.SourceEntries == "" {
			remote, err := fetch(ctx, m.client, in.SourceURL)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			now := time.Now().UTC()
			list.SourceEntries, list.LastUpdatedAt, list.LastAttemptAt, list.LastError = remote, &now, &now, ""
		}
	} else {
		list.SourceEntries, list.LastUpdatedAt, list.LastAttemptAt, list.LastError = "", nil, nil, ""
	}
	list.Name, list.Entries, list.SourceURL, list.UpdateHours = in.Name, entries, in.SourceURL, in.UpdateHours
	_, list.EntryCount, err = Parse(entries + "\n" + list.SourceEntries)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := m.db.Save(&list).Error; err != nil {
		return nil, err
	}
	if err := m.apply(); err != nil {
		return nil, err
	}
	return &list, nil
}

func (m *Manager) Refresh(ctx context.Context, id uint) (*models.AccessList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refresh(ctx, id)
}

func (m *Manager) refresh(ctx context.Context, id uint) (*models.AccessList, error) {
	var list models.AccessList
	if err := m.db.First(&list, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if list.SourceURL == "" {
		return nil, fmt.Errorf("%w: у списка нет ссылки на источник", ErrInvalid)
	}
	now := time.Now().UTC()
	list.LastAttemptAt = &now
	remote, err := fetch(ctx, m.client, list.SourceURL)
	if err == nil {
		_, list.EntryCount, err = Parse(list.Entries + "\n" + remote)
	}
	if err != nil {
		// Only status changes on failure; the last successfully applied entries stay.
		list.LastError = err.Error()
		if saveErr := m.db.Model(&list).Select("last_attempt_at", "last_error").Updates(&list).Error; saveErr != nil {
			return nil, saveErr
		}
		return &list, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	list.SourceEntries, list.LastUpdatedAt, list.LastError = remote, &now, ""
	if err := m.db.Save(&list).Error; err != nil {
		return nil, err
	}
	if err := m.apply(); err != nil {
		return nil, err
	}
	return &list, nil
}

func (m *Manager) Delete(id uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var lbs []models.LoadBalancer
	if err := m.db.Find(&lbs).Error; err != nil {
		return err
	}
	for _, lb := range lbs {
		for _, ids := range [][]uint{lb.AccessAllowLists, lb.AccessDenyLists} {
			for _, used := range ids {
				if used == id {
					return fmt.Errorf("%w: сначала уберите список из балансировщика «%s»", ErrInUse, lb.Name)
				}
			}
		}
	}
	result := m.db.Delete(&models.AccessList{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return m.apply()
}

func (m *Manager) refreshDue(ctx context.Context) {
	lists, err := m.List()
	if err != nil {
		return
	}
	for _, list := range lists {
		if ctx.Err() != nil {
			return
		}
		if list.SourceURL == "" || list.UpdateHours == 0 {
			continue
		}
		// Recheck under the mutation lock: manual refreshes and schedule edits
		// may have completed since the initial snapshot.
		m.mu.Lock()
		var current models.AccessList
		if m.db.First(&current, list.ID).Error == nil && current.SourceURL != "" && current.UpdateHours > 0 &&
			(current.LastAttemptAt == nil || time.Since(*current.LastAttemptAt) >= time.Duration(current.UpdateHours)*time.Hour) {
			m.refresh(ctx, current.ID)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) Run(ctx context.Context) {
	m.refreshDue(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	defer m.client.CloseIdleConnections()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshDue(ctx)
		}
	}
}
