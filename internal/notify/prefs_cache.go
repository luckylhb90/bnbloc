package notify

import (
	"context"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// prefsTTL bounds how long the producer reuses a member's preferences: a
// change saved on another replica takes effect within it (one saved here at
// once, Forget).
const prefsTTL = 30 * time.Second

// prefsCache is the producer's read of members' preferences: one database
// read per member per prefsTTL instead of one per notification
// (docs/prd/2026-10-30-message-path-performance.md §4.5). Writes go to the
// store it wraps.
type prefsCache struct {
	store.NotificationPrefsStore
	now func() time.Time

	mu      sync.Mutex
	entries map[string]cachedPrefs
}

type cachedPrefs struct {
	prefs store.NotificationPrefs
	found bool
	at    time.Time
}

func newPrefsCache(s store.NotificationPrefsStore, now func() time.Time) *prefsCache {
	if now == nil {
		now = time.Now
	}
	return &prefsCache{NotificationPrefsStore: s, now: now, entries: map[string]cachedPrefs{}}
}

func prefsKey(ctx context.Context, userID string) string {
	return tenant.FromContext(ctx) + "\x00" + userID
}

func (c *prefsCache) GetNotificationPrefs(ctx context.Context, userID string) (store.NotificationPrefs, bool, error) {
	key, now := prefsKey(ctx, userID), c.now()
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < prefsTTL {
		return e.prefs, e.found, nil
	}
	p, found, err := c.NotificationPrefsStore.GetNotificationPrefs(ctx, userID)
	if err != nil {
		return p, found, err
	}
	c.mu.Lock()
	if len(c.entries) > 50000 {
		c.entries = map[string]cachedPrefs{} // a bound, not an eviction policy
	}
	c.entries[key] = cachedPrefs{prefs: p, found: found, at: now}
	c.mu.Unlock()
	return p, found, nil
}

// Forget drops a member's entry: their change takes effect at once here.
func (c *prefsCache) Forget(ctx context.Context, userID string) {
	c.mu.Lock()
	delete(c.entries, prefsKey(ctx, userID))
	c.mu.Unlock()
}
