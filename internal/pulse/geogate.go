package pulse

import (
	"context"
	"sync"
	"time"
)

// GeoGate answers whether an organization has member locations on (its
// pulse_geography setting). A read failure counts as off: a zone is never
// stored on the strength of a setting that could not be read. Answers are cached
// for ttl, so a presence refresh does not read the database each time.
type GeoGate struct {
	read func(ctx context.Context, tenantID string) (bool, error)
	ttl  time.Duration

	mu    sync.Mutex
	cache map[string]geoEntry
}

type geoEntry struct {
	on    bool
	until time.Time
}

// NewGeoGate returns a gate reading each organization's setting with read.
func NewGeoGate(read func(ctx context.Context, tenantID string) (bool, error), ttl time.Duration) *GeoGate {
	return &GeoGate{read: read, ttl: ttl, cache: map[string]geoEntry{}}
}

// On reports whether the tenant has member locations on.
func (g *GeoGate) On(ctx context.Context, tenantID string) bool {
	if g == nil || g.read == nil || tenantID == "" {
		return false
	}
	now := time.Now()
	g.mu.Lock()
	if e, ok := g.cache[tenantID]; ok && now.Before(e.until) {
		g.mu.Unlock()
		return e.on
	}
	g.mu.Unlock()
	on, err := g.read(ctx, tenantID)
	if err != nil {
		on = false
	}
	g.mu.Lock()
	g.cache[tenantID] = geoEntry{on: on, until: now.Add(g.ttl)}
	g.mu.Unlock()
	return on
}
