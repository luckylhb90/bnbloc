package realtimehttp

import (
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The unread summary is read by every window and tab of an account that is
// not the one in front, each on its own timer: answering the same member from
// the last answer for a few seconds makes N windows cost one query. It is a
// cache of a number a client shows as a badge, so staleness of seconds is
// invisible, and it never holds more than unreadSummaryMax members.
const (
	unreadSummaryTTL = 10 * time.Second
	unreadSummaryMax = 10000
)

type unreadSummaryCache struct {
	mu sync.Mutex
	m  map[string]cachedUnread
}

type cachedUnread struct {
	summary wire.UnreadSummary
	at      time.Time
}

func (c *unreadSummaryCache) get(key string, now time.Time) (wire.UnreadSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || now.Sub(e.at) >= unreadSummaryTTL {
		return wire.UnreadSummary{}, false
	}
	return e.summary, true
}

func (c *unreadSummaryCache) put(key string, summary wire.UnreadSummary, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedUnread{}
	}
	if len(c.m) >= unreadSummaryMax {
		// Drop what has expired; a table still full is cleared (the next
		// answers refill it), so the cache can never grow past its bound.
		for k, e := range c.m {
			if now.Sub(e.at) >= unreadSummaryTTL {
				delete(c.m, k)
			}
		}
		if len(c.m) >= unreadSummaryMax {
			c.m = map[string]cachedUnread{}
		}
	}
	c.m[key] = cachedUnread{summary: summary, at: now}
}
