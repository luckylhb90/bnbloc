package shareagenthttp

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Borrowing acts in the room's conversation: only a member who may chat
// authorizes a borrow, borrows, or asks for more runs (never a visitor,
// D1, and never a platform agent host's membership). Withdrawing your own
// authorization stays open to every person; reads to every member.
func TestHumanOnlyRoutes(t *testing.T) {
	e := newPlatformEnv(t)
	e.svc.QuotaRequests = store.NewMemoryQuotaRequests()
	// bob adds the platform agent: root becomes its agent host in room-1.
	if code, out := e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent); code != http.StatusCreated {
		t.Fatalf("add = %d %v", code, out)
	}
	if !e.isAgentHost("room-1", "root") {
		t.Fatal("root is not the agent host")
	}
	auth := "/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization"
	for _, tc := range []struct {
		name, method, path, user string
		body                     any
		want                     int
	}{
		{"authorize as visitor", http.MethodPut, auth, "vic", map[string]any{}, http.StatusForbidden},
		{"authorize as agent host", http.MethodPut, auth, "root", map[string]any{}, http.StatusForbidden},
		{"authorize as member", http.MethodPut, auth, "alice", map[string]any{}, http.StatusOK},
		{"deauthorize as agent host", http.MethodDelete, auth, "root", nil, http.StatusForbidden},
		{"deauthorize as visitor", http.MethodDelete, auth, "vic", nil, http.StatusOK},
		{"authorization read as visitor", http.MethodGet, auth + "?owner=alice", "vic", nil, http.StatusOK},
		{"authorization read as agent host", http.MethodGet, auth + "?owner=alice", "root", nil, http.StatusOK},
		{"quota request as visitor", http.MethodPost, "/v1/borrow/quota-requests", "vic", map[string]any{"roomId": "room-1", "owner": "alice", "provider": "codex", "requestedRuns": 5}, http.StatusForbidden},
		{"quota request as agent host", http.MethodPost, "/v1/borrow/quota-requests", "root", map[string]any{"roomId": "room-1", "owner": "alice", "provider": "codex", "requestedRuns": 5}, http.StatusForbidden},
		{"add platform agent as visitor", http.MethodPost, "/v1/rooms/room-1/platform-agents", "vic", claudeAgent, http.StatusForbidden},
		{"add platform agent as agent host", http.MethodPost, "/v1/rooms/room-1/platform-agents", "root", claudeAgent, http.StatusForbidden},
	} {
		if code, out := e.call(t, tc.method, tc.path, tc.user, "", tc.body); code != tc.want {
			t.Errorf("%s = %d %v, want %d", tc.name, code, out, tc.want)
		}
	}

	// The borrower of a reserved run may chat: not a visitor, not an agent
	// host (the owner's bridge reserves; root hosts the platform agent).
	for borrower, want := range map[string]int{"vic": http.StatusForbidden, "bob": http.StatusOK} {
		if code, out := borrowCall(t, e.svc, tenant.Default, "root", "root", "nimbus:codex", "reserve", reserveBorrowBody{Borrower: borrower, Sandboxed: true}); code != want {
			t.Errorf("reserve for %s = %d %v, want %d", borrower, code, out, want)
		}
	}
}

// A platform grant is its own id namespace: the host's room authorization of
// the same agent (once it is also a person in the room) neither overwrites
// nor revokes it.
func TestPlatformGrantNamespace(t *testing.T) {
	e := newPlatformEnv(t)
	e.call(t, http.MethodPost, "/v1/rooms/room-1/platform-agents", "bob", "", codexAgent)
	e.members.Add("room-1", "root") // root joins as a person too
	auth := "/v1/rooms/room-1/shared-agents/shared-agent:nimbus:codex/authorization"
	if code, out := e.call(t, http.MethodPut, auth, "root", "", map[string]any{"runQuota": 1}); code != http.StatusOK {
		t.Fatalf("root authorizes = %d %v", code, out)
	}
	platform := func() store.Grant {
		t.Helper()
		g, err := e.grants.GetGrant(dctx(), platformGrantID(dctx(), "room-1", "root", "nimbus:codex"))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	if g := platform(); g.Source != store.GrantSourcePlatform || g.RunQuota != nil || g.RevokedAtMs != nil {
		t.Fatalf("platform grant after the room authorization = %+v", g)
	}
	if code, _ := e.call(t, http.MethodDelete, auth, "root", "", nil); code != http.StatusOK {
		t.Fatalf("deauthorize = %d", code)
	}
	if g := platform(); g.RevokedAtMs != nil {
		t.Fatalf("deauthorizing revoked the platform grant: %+v", g)
	}
}
