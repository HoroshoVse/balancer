package engine

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/balancer/backend/internal/models"
)

type namedPrefixSet struct {
	name     string
	prefixes prefixSet
}

type accessListCache struct {
	mu      sync.Mutex
	current atomic.Pointer[map[uint]namedPrefixSet]
}

func (c *accessListCache) snapshot() map[uint]namedPrefixSet {
	if c != nil {
		if snapshot := c.current.Load(); snapshot != nil {
			return *snapshot
		}
	}
	return nil
}

func matchAccessLists(lists map[uint]namedPrefixSet, ids []uint, addr netip.Addr) (string, bool) {
	for _, id := range ids {
		list := lists[id]
		if rule, ok := list.prefixes.match(addr); ok {
			return fmt.Sprintf("%s: %s", list.name, rule), true
		}
	}
	return "", false
}

// RefreshAccessLists publishes all lists atomically. Existing listeners and
// established connections stay up; new decisions immediately use the new data.
func (e *Engine) RefreshAccessLists() error {
	e.accessLists.mu.Lock()
	defer e.accessLists.mu.Unlock()
	var lists []models.AccessList
	if err := e.db.Find(&lists).Error; err != nil {
		return err
	}
	next := make(map[uint]namedPrefixSet, len(lists))
	for _, list := range lists {
		prefixes, err := models.ParsePrefixes(list.Entries + "\n" + list.SourceEntries)
		if err != nil {
			return fmt.Errorf("address list %q: %w", list.Name, err)
		}
		next[list.ID] = namedPrefixSet{name: list.Name, prefixes: newPrefixSet(prefixes)}
	}
	e.accessLists.current.Store(&next)
	return nil
}
