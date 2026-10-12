package gateway

import (
	"sync"
	"time"
)

// Edge lookup caches. Both are bounded in size and expire, so neither memory
// nor staleness grows without limit.
const (
	// memberCacheTTL bounds how long a positive live membership lookup is
	// reused for the room-scope fallback.
	memberCacheTTL = 30 * time.Second
	// roomTenantCacheTTL bounds reuse of a room's resolved tenant.
	roomTenantCacheTTL = 5 * time.Minute
	// roomCacheMaxEntries caps each cache.
	roomCacheMaxEntries = 10000
)

// boundedCache is a small concurrency-safe map with per-entry expiry and a hard
// size cap. When full it drops expired entries, then (if still full) an
// arbitrary quarter of the table, so an insert is always O(cap) worst case and
// the table never exceeds max.
type boundedCache[V any] struct {
	mu  sync.Mutex
	ttl time.Duration
	max int
	now func() time.Time
	m   map[string]boundedEntry[V]
}

type boundedEntry[V any] struct {
	v       V
	expires time.Time
}

func newBoundedCache[V any](ttl time.Duration, max int) *boundedCache[V] {
	return &boundedCache[V]{ttl: ttl, max: max, now: time.Now, m: map[string]boundedEntry[V]{}}
}

func (c *boundedCache[V]) get(k string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || c.now().After(e.expires) {
		delete(c.m, k)
		var zero V
		return zero, false
	}
	return e.v, true
}

func (c *boundedCache[V]) put(k string, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, exists := c.m[k]; !exists && len(c.m) >= c.max {
		for key, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, key)
			}
		}
		for key := range c.m {
			if len(c.m) <= c.max*3/4 {
				break
			}
			delete(c.m, key)
		}
	}
	c.m[k] = boundedEntry[V]{v: v, expires: now.Add(c.ttl)}
}

// deleteWhere drops every entry whose key matches.
func (c *boundedCache[V]) deleteWhere(match func(key string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if match(k) {
			delete(c.m, k)
		}
	}
}

func (c *boundedCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
