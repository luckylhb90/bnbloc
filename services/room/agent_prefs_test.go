package room

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// A saved agent names its owner: (core, provider) alone is a harness that
// several members may run, and restoring it onto someone else's agent
// started a borrow (web H9).
func TestAgentPrefsKeepTheOwner(t *testing.T) {
	srv, svc := newTestServer(t)
	svc.AgentPrefs = store.NewMemoryAgentPrefs()
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "maya", Role: store.RoleMember})
	url := srv.URL + "/v1/rooms/room-1/agent-prefs"

	resp := do(t, http.MethodPut, url, "maya", map[string]string{"channelId": "ch1", "core": "acme", "provider": "codex"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("put without an owner: %d, want 400", resp.StatusCode)
	}
	resp = do(t, http.MethodPut, url, "maya", map[string]string{"channelId": "ch1", "ownerId": "jack", "core": "acme", "provider": "codex", "model": "m1"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d, want 204", resp.StatusCode)
	}
	resp = do(t, http.MethodGet, url+"?channel=ch1", "maya", nil)
	defer resp.Body.Close()
	var got struct {
		OwnerID  string `json:"ownerId"`
		Core     string `json:"core"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Found    bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || !got.Found || got.OwnerID != "jack" || got.Core != "acme" || got.Provider != "codex" || got.Model != "m1" {
		t.Fatalf("get = %+v (%v)", got, err)
	}
}
