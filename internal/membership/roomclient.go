package membership

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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

	mu       sync.Mutex
	cache    map[string]cachedMember // memberKey(room, user) -> membership
	previews map[string]cachedMember // memberKey(room, user) -> preview
	swept    time.Time               // the caches' last sweep of expired answers
}

type cachedMember struct {
	m   Member
	exp time.Time
}

// NewRoomClient constructs a RoomClient with sensible defaults.
func NewRoomClient(baseURL string) *RoomClient {
	return &RoomClient{
		BaseURL:  baseURL,
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		TTL:      10 * time.Second,
		Now:      time.Now,
		cache:    map[string]cachedMember{},
		previews: map[string]cachedMember{},
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

// PreviewQuery is the probe's query parameter that asks for the room's
// preview answer too (services/room handleMembershipProbe): "1" asks.
const PreviewQuery = "preview"

// ProbeAnswer is bnbloc-room's membership probe answer, the one wire shape
// of both sides: Member for a membership, Preview for a non-member of a live
// previewable room (only when asked, PreviewQuery), TenantID the room's own
// tenant in both.
type ProbeAnswer struct {
	Member    bool   `json:"member"`
	Role      string `json:"role,omitempty"`
	AgentHost bool   `json:"agentHost,omitempty"`
	Preview   bool   `json:"preview,omitempty"`
	TenantID  string `json:"tenantId,omitempty"`
}

// probe asks the room service's tenant-agnostic membership probe. ok is
// false on any error and on an answer without the room's tenant (fail
// closed).
func (c *RoomClient) probe(roomID, userID string, preview bool) (ProbeAnswer, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := c.BaseURL + "/v1/rooms/" + url.PathEscape(roomID) + "/membership/" + url.PathEscape(userID)
	if preview {
		// Only a preview asks whether the room is previewable: every other
		// lookup of a non-member (fan-outs, the edge's fallback) stays the
		// one membership read it was.
		endpoint += "?" + PreviewQuery + "=1"
	}
	// The membership probe authorizes on the queried subject itself; it is
	// tenant-agnostic (the answer names the room's tenant).
	req, err := svcauth.NewRequest(ctx, http.MethodGet, endpoint, nil, userID, "")
	if err != nil {
		return ProbeAnswer{}, false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ProbeAnswer{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProbeAnswer{}, false
	}
	var out ProbeAnswer
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.TenantID == "" {
		return ProbeAnswer{}, false
	}
	return out, true
}

// remember caches m for (room, user) in cache (c.mu held). Expired answers
// are swept from both caches at most once a TTL, so a cache holds at most
// two TTLs of answers whatever the churn, and a burst of lookups (every
// stream of a replica reconnecting after a deploy, each naming its member's
// rooms) costs a sweep per TTL, not a scan of the whole cache per answer.
func (c *RoomClient) remember(cache map[string]cachedMember, roomID, userID string, m Member) {
	now := c.Now()
	if !now.Before(c.swept.Add(c.TTL)) {
		for _, cc := range []map[string]cachedMember{c.cache, c.previews} {
			for k, hit := range cc {
				if now.After(hit.exp) {
					delete(cc, k)
				}
			}
		}
		c.swept = now
	}
	cache[memberKey(roomID, userID)] = cachedMember{m: m, exp: now.Add(c.TTL)}
}

// Fresh asks bnbloc-room's tenant-agnostic membership probe, bypassing the
// cache, and caches a positive answer. Fails closed on any error, and on an
// answer without the room's tenant.
func (c *RoomClient) Fresh(roomID, userID string) (Member, bool) {
	out, ok := c.probe(roomID, userID, false)
	if !ok || !out.Member {
		return Member{}, false
	}
	m := Member{Role: store.Role(out.Role), AgentHost: out.AgentHost, Tenant: out.TenantID}
	c.mu.Lock()
	c.remember(c.cache, roomID, userID, m)
	c.mu.Unlock()
	return m, true
}

// MaxProbeRooms bounds one probe of many rooms (bnbloc-room's GET
// /v1/users/{id}/memberships?rooms=): LookupMany asks in pages of it.
const MaxProbeRooms = 200

// ProbeAnswers is the many-rooms probe's answer: the user's memberships by
// room id, a room they are not in left out.
type ProbeAnswers struct {
	Rooms map[string]ProbeAnswer `json:"rooms"`
}

// LookupMany is Lookup for many rooms of one user: fresh cached answers,
// and the rest from the room service's many-rooms probe, MaxProbeRooms a
// call (a stream naming its member's rooms costs a call per page, not a
// probe per room). A page that fails is no member (fail closed).
func (c *RoomClient) LookupMany(roomIDs []string, userID string) map[string]Member {
	out := make(map[string]Member, len(roomIDs))
	var ask []string
	c.mu.Lock()
	now := c.Now()
	for _, id := range roomIDs {
		if hit, ok := c.cache[memberKey(id, userID)]; ok && now.Before(hit.exp) {
			out[id] = hit.m
		} else {
			ask = append(ask, id)
		}
	}
	c.mu.Unlock()
	for page := range slices.Chunk(ask, MaxProbeRooms) {
		answers, ok := c.probeMany(page, userID)
		if !ok {
			continue
		}
		c.mu.Lock()
		for id, a := range answers.Rooms {
			if !a.Member || a.TenantID == "" || !slices.Contains(page, id) {
				continue
			}
			m := Member{Role: store.Role(a.Role), AgentHost: a.AgentHost, Tenant: a.TenantID}
			c.remember(c.cache, id, userID, m)
			out[id] = m
		}
		c.mu.Unlock()
	}
	return out
}

func (c *RoomClient) probeMany(roomIDs []string, userID string) (ProbeAnswers, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	endpoint := c.BaseURL + "/v1/users/" + url.PathEscape(userID) + "/memberships?rooms=" + url.QueryEscape(strings.Join(roomIDs, ","))
	req, err := svcauth.NewRequest(ctx, http.MethodGet, endpoint, nil, userID, "")
	if err != nil {
		return ProbeAnswers{}, false
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ProbeAnswers{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProbeAnswers{}, false
	}
	var out ProbeAnswers
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ProbeAnswers{}, false
	}
	return out, true
}

// Preview implements Directory.Preview from the probe: userID is no member
// and the room is live and previewable; the answer is the room's tenant. A
// positive answer is cached like a membership's (and dropped by Forget). The
// probe's answer is the room's, not the caller's: whether the caller's
// organization may read it is the edge's.
func (c *RoomClient) Preview(roomID, userID string) (string, bool) {
	c.mu.Lock()
	if hit, ok := c.previews[memberKey(roomID, userID)]; ok && c.Now().Before(hit.exp) {
		c.mu.Unlock()
		return hit.m.Tenant, true
	}
	c.mu.Unlock()
	out, ok := c.probe(roomID, userID, true)
	if !ok || out.Member || !out.Preview {
		return "", false
	}
	c.mu.Lock()
	c.remember(c.previews, roomID, userID, Member{Tenant: out.TenantID})
	c.mu.Unlock()
	return out.TenantID, true
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
		delete(c.previews, memberKey(roomID, userID))
		return
	}
	for _, cache := range []map[string]cachedMember{c.cache, c.previews} {
		for k := range cache {
			if strings.HasPrefix(k, roomID+"\x00") {
				delete(cache, k)
			}
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
