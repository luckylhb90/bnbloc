package membership

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// RoomClient authorizes room membership by calling bnbloc-room's member probe.
// A positive result is cached with a short TTL; any non-200 fails closed
// (returns false), never granting access on error.
type RoomClient struct {
	BaseURL string
	HTTP    *http.Client
	TTL     time.Duration
	Now     func() time.Time

	mu    sync.Mutex
	cache map[string]cachedMember // "tenant\x00roomID\x00userID" -> membership
}

type cachedMember struct {
	m   Member
	exp time.Time
}

// NewRoomClient constructs a RoomClient with sensible defaults.
func NewRoomClient(baseURL string) *RoomClient {
	return &RoomClient{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		TTL:     10 * time.Second,
		Now:     time.Now,
		cache:   map[string]cachedMember{},
	}
}

// IsMember reports whether the user belongs to the room in any way, an
// agent-host membership included (see Lookup).
func (c *RoomClient) IsMember(roomID, userID, tenantID string) bool {
	_, ok := c.Lookup(roomID, userID, tenantID)
	return ok
}

// IsHumanMember reports whether the user is a human member of the room: a
// member whose membership is not an agent host's (see store.Membership).
func (c *RoomClient) IsHumanMember(roomID, userID, tenantID string) bool {
	m, ok := c.Lookup(roomID, userID, tenantID)
	return ok && !m.AgentHost
}

// Lookup returns the user's membership of the room, consulting the cache
// first and then bnbloc-room's tenant-agnostic membership probe. Room ids are
// globally unique (the room's own tenant is authoritative), so membership is
// keyed on (room, user) alone — a caller whose token was minted in a different
// tenant than the room (a multi-tenant member, or the bridge acting as one) is
// still correctly authorized. The tenantID arg is retained for the interface
// and cache key but does not scope the probe. Fails closed on any error.
func (c *RoomClient) Lookup(roomID, userID, tenantID string) (Member, bool) {
	key := tenantID + "\x00" + roomID + "\x00" + userID
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok && c.Now().Before(hit.exp) {
		c.mu.Unlock()
		return hit.m, true
	}
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := c.BaseURL + "/v1/rooms/" + url.PathEscape(roomID) + "/membership/" + url.PathEscape(userID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Member{}, false
	}
	// The membership probe authorizes on the queried subject itself.
	req.Header.Set("X-Bnbloc-User", userID)
	svcauth.Sign(req, svcauth.KeyFromEnv(), userID)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Member{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Member{}, false
	}
	var out struct {
		Member    bool   `json:"member"`
		Role      string `json:"role"`
		AgentHost bool   `json:"agentHost"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Member {
		return Member{}, false
	}
	m := Member{Role: store.Role(out.Role), AgentHost: out.AgentHost}
	c.mu.Lock()
	now := c.Now()
	// Opportunistically evict expired entries so the cache cannot grow unbounded
	// with ephemeral rooms / churning users. TTL is short (seconds), so the map
	// stays small and this scan is cheap.
	for k, hit := range c.cache {
		if now.After(hit.exp) {
			delete(c.cache, k)
		}
	}
	c.cache[key] = cachedMember{m: m, exp: now.Add(c.TTL)}
	c.mu.Unlock()
	return m, true
}

// AgentHostSubject is the service identity the room service accepts on its
// agent-host routes: only shareagent (the platform-agent authority) adds or
// removes agent-host memberships.
var AgentHostSubject = svcauth.ServiceSubject("shareagent")

// PutAgentHost gives userID an agent-host membership of roomID (in tenantID,
// the room's tenant) through the room service's internal route. Idempotent;
// a human membership is never downgraded.
func (c *RoomClient) PutAgentHost(ctx context.Context, tenantID, roomID, userID string) error {
	return c.agentHost(ctx, http.MethodPut, tenantID, roomID, userID)
}

// DeleteAgentHost removes userID's agent-host membership of roomID (never a
// human membership). Idempotent.
func (c *RoomClient) DeleteAgentHost(ctx context.Context, tenantID, roomID, userID string) error {
	return c.agentHost(ctx, http.MethodDelete, tenantID, roomID, userID)
}

func (c *RoomClient) agentHost(ctx context.Context, method, tenantID, roomID, userID string) error {
	endpoint := c.BaseURL + "/internal/v1/rooms/" + url.PathEscape(roomID) + "/agent-hosts/" + url.PathEscape(userID)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set(svcauth.HeaderUser, AgentHostSubject)
	if tenantID != "" {
		req.Header.Set(svcauth.HeaderTenant, tenantID)
	}
	svcauth.Sign(req, svcauth.KeyFromEnv(), AgentHostSubject)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("membership: agent-host %s answered %d", method, resp.StatusCode)
	}
	// The cached probe answer for this user is stale now.
	c.mu.Lock()
	for k := range c.cache {
		if strings.HasSuffix(k, "\x00"+roomID+"\x00"+userID) {
			delete(c.cache, k)
		}
	}
	c.mu.Unlock()
	return nil
}
