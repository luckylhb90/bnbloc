package shareagenthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// A caller who is not a member of the room is refused (SEC3): membership is
// verified before any shared-agent action, failing closed with 403.
func TestNonMemberIsForbidden(t *testing.T) {
	svc, _ := newService(t, "")
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization",
		strings.NewReader(`{"allowedModels":["gpt-5"]}`))
	req.Header.Set("X-Bnbloc-User", "stranger") // not in room-1
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-member", resp.StatusCode)
	}
}

// The room shared-agents listing returns only ACTIVE (non-revoked, non-expired)
// shared-agent authorizations, projecting the discovery shape without leaking a
// credential or an owner endpoint.
func TestListSharedAgentsReturnsActiveOnly(t *testing.T) {
	svc, gs := newService(t, "")
	quota := 5
	// Active room authorization for alice/codex with a quota.
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "alice", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "alice", GranteeUserID: "*",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}, {Model: "o3"}},
		RunQuota:      &quota,
	})
	// Revoked authorization must be excluded.
	revoked := int64(500)
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "jack", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "jack", GranteeUserID: "*",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}}, RevokedAtMs: &revoked,
	})
	// Expired authorization must be excluded (now is 1000 in the fixture).
	expired := int64(900)
	_ = gs.PutGrant(dctx(), store.Grant{
		GrantID: roomAuthGrantID(dctx(), "room-1", "bob", "codex"), RoomID: "room-1",
		TargetID: "shared-agent:codex", OwnerUserID: "bob", GranteeUserID: "*",
		AllowedModels: []store.AllowedModel{{Model: "gpt-5"}}, ExpiresAtMs: &expired,
	})

	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/shared-agents", nil)
	req.Header.Set("X-Bnbloc-User", "bob")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var entries []sharedAgentEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 active: %+v", len(entries), entries)
	}
	e := entries[0]
	if e.OwnerUserID != "alice" || e.Provider != "codex" || e.TargetID != "shared-agent:codex" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	if !e.HasQuota {
		t.Fatal("entry with RunQuota must report hasQuota=true")
	}
	if len(e.AllowedModels) != 2 || e.AllowedModels[0] != "gpt-5" {
		t.Fatalf("allowedModels = %v, want [gpt-5 o3]", e.AllowedModels)
	}
}

// Listing requires room membership: a stranger is refused.
func TestListSharedAgentsNonMemberForbidden(t *testing.T) {
	svc, _ := newService(t, "")
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/shared-agents", nil)
	req.Header.Set("X-Bnbloc-User", "stranger")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-member", resp.StatusCode)
	}
}
