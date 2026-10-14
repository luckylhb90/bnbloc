package bridge

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// People's display names in records and prompts
// (docs/prd/2026-10-14-multi-member-rooms.md rule 8), read from the
// gateway's profiles (GET /v1/users/profiles) and kept for nameTTL.

const (
	nameTTL = 10 * time.Minute
	// maxNameIDs is the most ids one profiles read names.
	maxNameIDs = 200
)

// nameCache is the display names the bridge read, by user id.
type nameCache struct {
	mu sync.Mutex
	m  map[string]cachedName
}

type cachedName struct {
	name string
	at   time.Time
}

// displayNames are the display names of ids (id → name), reading the ones
// the cache does not hold; a failed read leaves them unnamed (their ids
// show).
func (b *Bridge) displayNames(ctx context.Context, ids []string) map[string]string {
	c := &b.names
	out := map[string]string{}
	var missing []string
	c.mu.Lock()
	for _, id := range ids {
		if id == "" || out[id] != "" {
			continue
		}
		if n, ok := c.m[id]; ok && time.Since(n.at) < nameTTL {
			out[id] = n.name
		} else if len(missing) < maxNameIDs {
			missing = append(missing, id)
		}
	}
	c.mu.Unlock()
	if len(missing) == 0 {
		return out
	}
	raw, err := b.gatewayGET(ctx, "/v1/users/profiles?ids="+url.QueryEscape(strings.Join(missing, ",")))
	var page struct {
		Profiles []wire.UserProfile `json:"profiles"`
	}
	if err != nil || json.Unmarshal(raw, &page) != nil {
		page.Profiles = nil // kept unnamed until the TTL: no read every turn
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 4*maxNameIDs {
		c.m = map[string]cachedName{}
	}
	for _, id := range missing {
		c.m[id] = cachedName{at: time.Now()}
	}
	for _, p := range page.Profiles {
		c.m[p.UserID] = cachedName{name: p.DisplayName, at: time.Now()}
		out[p.UserID] = p.DisplayName
	}
	return out
}

// nameEntries gives the people's lines of entries their display names.
func (b *Bridge) nameEntries(ctx context.Context, entries []roomlog.Entry) []roomlog.Entry {
	var ids []string
	for _, e := range entries {
		if e.Kind == roomlog.KindHuman {
			ids = append(ids, e.Speaker)
		}
	}
	return roomlog.SetNames(entries, b.displayNames(ctx, ids))
}

// personLabel names a person where their identity matters (a turn's
// requester): "Name (id)", or the id.
func (b *Bridge) personLabel(ctx context.Context, id string) string {
	return roomlog.PersonLabel(b.displayNames(ctx, []string{id})[id], id)
}
