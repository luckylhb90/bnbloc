// Package e2e exercises the bnbloc control-plane end to end in one process:
// gateway -> room/activity/realtime/shareagent, proving the acceptance
// scenarios US-1 (activity visibility) and US-3 (agent borrowing) from the PRD.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/adapter"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
	activitysvc "github.com/luckylhb90/bnbloc/services/activity"
	gatewaysvc "github.com/luckylhb90/bnbloc/services/gateway"
	realtimesvc "github.com/luckylhb90/bnbloc/services/realtime"
	shareagentsvc "github.com/luckylhb90/bnbloc/services/shareagent"
)

// tctx scopes seeds and lookups to the default tenant, matching what the
// gateway injects on proxied requests (the stores fail closed without one).
func tctx() context.Context {
	return tenant.NewContext(context.Background(), tenant.Default)
}

// staticMembers is a shared membership set for the in-process backends.
type staticMembers struct{ *store.MemoryRoom }

func (s staticMembers) Lookup(roomID, userID string) (membership.Member, bool) {
	m, ok := s.MemoryRoom.MembershipAnyTenant(tctx(), roomID, userID)
	return membership.Member{Role: m.Role, AgentHost: m.AgentHost, Tenant: m.TenantID}, ok
}

func (s staticMembers) Fresh(roomID, userID string) (membership.Member, bool) {
	return s.Lookup(roomID, userID)
}

func (staticMembers) Forget(string, string) {}

type onlinePresence struct{}

func (onlinePresence) IsOwnerOnline(string, string) bool { return true }

// harness holds the composed control-plane for the test.
type harness struct {
	gateway *httptest.Server
	rooms   *store.MemoryRoom
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	rooms := store.NewMemoryRoom()
	members := staticMembers{rooms}
	hub := realtime.NewHub(256, 64)

	// activity + realtime share one hub so a report fans out to subscribers.
	realtimeService := &realtimesvc.Service{Hub: hub, Membership: members}
	activity := &activitysvc.Service{Store: store.NewMemory(), Publisher: realtime.HubPublisher{Hub: hub}, Membership: members}
	grants := store.NewMemoryGrant()
	seq := 0
	controller := &shareagent.Controller{
		Grants: grants, Presence: onlinePresence{}, Audit: grants,
		NewLeaseID: func() string { seq++; return "lease" }, Now: func() int64 { return 1000 },
	}
	// Fake owner daemon holds a secret and returns a result without echoing it.
	ownerDaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"borrowed turn complete"}`))
	}))
	t.Cleanup(ownerDaemon.Close)
	shareService := &shareagentsvc.Service{
		Controller: controller, Grants: grants, Presence: onlinePresence{},
		Owners: fixedOwner{ownerDaemon.URL}, Now: func() int64 { return 1000 },
		Membership: members,
	}

	activitySrv := httptest.NewServer(activity.Handler())
	realtimeSrv := httptest.NewServer(realtimeService.Handler())
	shareSrv := httptest.NewServer(shareService.Handler())
	roomSrv := httptest.NewServer(newRoomHandler(rooms))
	t.Cleanup(activitySrv.Close)
	t.Cleanup(realtimeSrv.Close)
	t.Cleanup(shareSrv.Close)
	t.Cleanup(roomSrv.Close)

	gw := &gatewaysvc.Service{
		Verifier: devVerifier{},
		Signer:   auth.NewSigner([]byte("e2e-key")),
		Backends: gatewaysvc.Backends{
			Room: roomSrv.URL, Activity: activitySrv.URL,
			Realtime: realtimeSrv.URL, ShareAgent: shareSrv.URL,
		},
		Rooms: func(subject, tenantID string) []string {
			var out []string
			for _, m := range rooms.Members(tctx(), "room-1") {
				if m.UserID == subject {
					out = append(out, "room-1")
				}
			}
			return out
		},
	}
	gwSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(gwSrv.Close)

	// Grant seed for US-3.
	_ = grants.PutGrant(tctx(), store.Grant{
		GrantID: "g1", RoomID: "room-1", TargetID: "shared-agent:codex",
		OwnerUserID: "alice", GranteeUserID: "bob",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}},
		Capabilities:  store.Capabilities{Consult: true}, MaxConcurrent: 2,
	})

	return &harness{gateway: gwSrv, rooms: rooms}
}

func (h *harness) login(t *testing.T, subject string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"idpToken": subject})
	resp, err := http.Post(h.gateway.URL+"/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		SessionToken string `json:"sessionToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.SessionToken
}

// US-1: member B, subscribed to the room, sees member A's reported agent
// session state fan out through the gateway within 2s.
func TestE2EActivityVisibility(t *testing.T) {
	h := newHarness(t)
	// Alice creates the room; both alice and bob are members.
	_ = h.rooms.CreateRoom(tctx(), store.Room{RoomID: "room-1", Name: "Team"})
	_ = h.rooms.AddMember(tctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner})
	_ = h.rooms.AddMember(tctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember})

	bobToken := h.login(t, "bob")

	// Bob subscribes to room events (SSE) through the gateway.
	req, _ := http.NewRequest(http.MethodGet, h.gateway.URL+"/v1/rooms/room-1/events", nil)
	req.Header.Set("Authorization", "Bearer "+bobToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", resp.StatusCode)
	}

	// Alice's daemon reports a session state via the adapter through the gateway.
	aliceToken := h.login(t, "alice")
	rep := adapter.NewActivityReporter(adapter.Config{GatewayBaseURL: h.gateway.URL, SessionToken: aliceToken})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = rep.ReportSessionState(tctx(), wire.ReportSessionStateInput{
			WorkspaceID: "room-1", AgentSessionID: "s1", OwnerUserID: "alice", MessageVersion: 1,
		})
	}()

	// Bob reads the SSE stream until the agent event arrives.
	deadline := time.After(2 * time.Second)
	events := make(chan string, 8)
	go readSSEEvents(resp.Body, events)
	for {
		select {
		case kind := <-events:
			if kind == "agent_session_state" {
				return // US-1 satisfied
			}
		case <-deadline:
			t.Fatal("US-1: timed out waiting for A's session state to reach B")
		}
	}
}

// US-3: member B borrows member A's shared agent through the gateway; the owner
// credential never appears in the response.
func TestE2EAgentBorrowing(t *testing.T) {
	h := newHarness(t)
	_ = h.rooms.CreateRoom(tctx(), store.Room{RoomID: "room-1"})
	_ = h.rooms.AddMember(tctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember})
	bobToken := h.login(t, "bob")

	c := adapter.NewSharedAgentAccessClient(adapter.Config{GatewayBaseURL: h.gateway.URL, SessionToken: bobToken})
	result, lease, err := c.Turn(tctx(), adapter.TurnRequest{
		RoomID: "room-1", TargetID: "shared-agent:codex", GrantID: "g1",
		Model: "gpt-5", TurnCapability: "consult",
	})
	if err != nil {
		t.Fatalf("US-3 borrow turn: %v", err)
	}
	if lease == "" {
		t.Fatal("US-3: expected a concurrency lease")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(strings.ToLower(string(raw)), "secret") || strings.Contains(strings.ToLower(string(raw)), "credential") {
		t.Fatalf("US-3: credential leaked in borrowed result: %s", raw)
	}
	if !strings.Contains(string(raw), "borrowed turn complete") {
		t.Fatalf("US-3: expected proxied owner result, got %s", raw)
	}
}

func readSSEEvents(r io.Reader, out chan<- string) {
	buf := make([]byte, 4096)
	var acc strings.Builder
	for {
		n, err := r.Read(buf)
		if n > 0 {
			acc.WriteString(string(buf[:n]))
			for {
				line, rest, found := strings.Cut(acc.String(), "\n")
				if !found {
					break
				}
				acc.Reset()
				acc.WriteString(rest)
				if strings.HasPrefix(line, "event: ") {
					out <- strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				}
			}
		}
		if err != nil {
			return
		}
	}
}

type fixedOwner struct{ url string }

func (o fixedOwner) OwnerEndpoint(string, string) (string, bool) { return o.url, true }

type devVerifier struct{}

func (devVerifier) Verify(token string) (auth.Identity, error) {
	if token == "" {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return auth.Identity{Subject: token}, nil
}
