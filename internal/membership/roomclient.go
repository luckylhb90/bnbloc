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
// A positive result is cached with a short TTL (Forget clears it on a
// revocation signal); any non-200 fails closed (not a member) and is never
// cached.
type RoomClient struct {
	BaseURL string
	HTTP    *http.Client
	TTL     time.Duration
	Now     func() time.Time

	mu    sync.Mutex
	cache map[string]cachedMember // memberKey(room, user) -> membership
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

func memberKey(roomID, userID string) string { return roomID + "\x00" + userID }

// Lookup returns the user's membership of the room, from the cache while a
// positive answer is fresh, else from Fresh. Room ids are globally unique:
// the probe is keyed on (room, user) alone and answers the room's own
// tenant, which RoomScope compares with the request's.
func (c *RoomClient) Lookup(roomID, userID string) (Member, bool) {
	c.mu.Lock()
	if hit, ok := c.cache[memberKey(roomID, userID)]; ok && c.Now().Before(hit.exp) {
		c.mu.Unlock()
		return hit.m, true
	}
	c.mu.Unlock()
	return c.Fresh(roomID, userID)
}

// Fresh asks bnbloc-room's tenant-agnostic membership probe, bypassing the
// cache, and caches a positive answer. Fails closed on any error, and on an
// answer without the room's tenant.
func (c *RoomClient) Fresh(roomID, userID string) (Member, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := c.BaseURL + "/v1/rooms/" + url.PathEscape(roomID) + "/membership/" + url.PathEscape(userID)
	// The membership probe authorizes on the queried subject itself; it is
	// tenant-agnostic (the answer names the room's tenant).
	req, err := svcauth.NewRequest(ctx, http.MethodGet, endpoint, nil, userID, "")
	if err != nil {
		return Member{}, false
	}
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
		TenantID  string `json:"tenantId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.Member || out.TenantID == "" {
		return Member{}, false
	}
	m := Member{Role: store.Role(out.Role), AgentHost: out.AgentHost, Tenant: out.TenantID}
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
	c.cache[memberKey(roomID, userID)] = cachedMember{m: m, exp: now.Add(c.TTL)}
	c.mu.Unlock()
	return m, true
}

// Forget drops the cached answer for (room, user), or every cached answer of
// the room when userID is "". realtime calls it when it delivers
// member_removed / room_deleted, so a removed member's next request (a
// reconnect, a post) is refused at once instead of after the TTL.
func (c *RoomClient) Forget(roomID, userID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if userID != "" {
		delete(c.cache, memberKey(roomID, userID))
		return
	}
	for k := range c.cache {
		if strings.HasPrefix(k, roomID+"\x00") {
			delete(c.cache, k)
		}
	}
}

// AgentHostService is the only service the room service accepts on its
// agent-host routes: shareagent (the platform-agent authority) adds and
// removes agent-host memberships, asserting AgentHostSubject.
const AgentHostService = "shareagent"

// AgentHostSubject is the identity shareagent asserts on those routes.
var AgentHostSubject = svcauth.ServiceSubject(AgentHostService)

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
	req, err := svcauth.NewRequest(ctx, method, endpoint, nil, AgentHostSubject, tenantID)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("membership: agent-host %s answered %d", method, resp.StatusCode)
	}
	// The cached probe answer for this user is stale now.
	c.Forget(roomID, userID)
	return nil
}
