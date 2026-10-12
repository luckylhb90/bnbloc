package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type fixedAdvertisements map[string][]store.BridgeAgent

func (f fixedAdvertisements) ListBridgeAgentsForOwner(_ context.Context, owner string, _ int64) ([]store.BridgeAgent, error) {
	return f[owner], nil
}

// A chat to a platform agent whose host is offline is refused with 409
// platform_agent_offline before anything is published or queued; an online
// host, the host's own agent and an ordinary borrow are never refused.
func TestChatToOfflinePlatformAgentRefused(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	hub := realtime.NewHub(64, 16)
	mem := membership.NewStatic()
	for _, u := range []string{"bob", "alice", "root"} {
		mem.Add("room-1", u)
	}
	grants := store.NewMemoryGrant()
	_ = grants.PutGrant(ctx, store.Grant{GrantID: "p", RoomID: "room-1", TargetID: "shared-agent:nimbus:codex", OwnerUserID: "root", GranteeUserID: "*", Source: store.GrantSourcePlatform})
	future := int64(1) << 60
	_ = grants.PutGrant(ctx, store.Grant{GrantID: "o", RoomID: "room-1", TargetID: "shared-agent:nimbus:claude", OwnerUserID: "alice", GranteeUserID: "*", ExpiresAtMs: &future})
	settings := store.NewMemoryPlatformSettings()
	_ = store.SavePlatformAgents(ctx, settings, wire.PlatformAgentsPolicy{Hosts: []wire.PlatformAgentHost{{HostUserID: "root", Enabled: true,
		Agents: []wire.PlatformAgentSpec{{Core: "nimbus", Provider: "codex"}}}}}, "root", 1)
	ads := fixedAdvertisements{}
	jobs := &captureBridgeJobs{}
	svc := &Service{Hub: hub, Membership: mem, BridgeJobs: jobs, PlatformAgents: &PlatformAgentGuard{Grants: grants, Settings: settings, Bridges: ads}}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	send := func(author, owner, provider, clientID string) (int, map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"text": "hello", "clientMsgId": clientID,
			"recipient": map[string]any{"ownerId": owner, "provider": provider, "core": "nimbus"}})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms/room-1/chat", bytes.NewReader(body))
		req.Header.Set("X-Bnbloc-User", author)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, out := send("bob", "root", "codex", "c1")
	if code != http.StatusConflict || out["error"] != wire.ErrPlatformAgentOffline || out["hostUserId"] != "root" ||
		out["core"] != "nimbus" || out["provider"] != "codex" || out["message"] == "" {
		t.Fatalf("offline platform agent = %d %v", code, out)
	}
	if jobs.job.JobID != "" {
		t.Fatalf("a job was created for an offline platform agent: %+v", jobs.job)
	}
	sub := hub.Subscribe("room-1") // replays the room's buffered events
	select {
	case ev := <-sub.Events():
		t.Fatalf("the refused chat was published: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
	hub.Unsubscribe("room-1", sub)
	// The host driving their own agent, and an owner-issued borrow of an
	// agent nobody advertises, are not platform drives.
	if code, _ := send("root", "root", "codex", "c2"); code != http.StatusOK || jobs.job.OwnerUserID != "root" {
		t.Fatalf("own agent = %d", code)
	}
	if code, _ := send("bob", "alice", "claude", "c3"); code != http.StatusOK || jobs.job.Provider != "claude" {
		t.Fatalf("ordinary borrow = %d", code)
	}
	// Host online: the platform chat goes through and queues a job.
	ads["root"] = []store.BridgeAgent{{OwnerUserID: "root", Core: "nimbus", Provider: "codex", TargetID: "nimbus/codex"}}
	if code, _ := send("bob", "root", "codex", "c4"); code != http.StatusOK || jobs.job.OwnerUserID != "root" || jobs.job.RequesterUserID != "bob" || jobs.job.Provider != "codex" {
		t.Fatalf("online platform agent = %d, job %+v", code, jobs.job)
	}
}
