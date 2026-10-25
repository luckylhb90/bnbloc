package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A member's own bridge that takes sends gets their queued sends with its
// claim, once; a claim without a free slot takes sends and no job; another
// member's bridge and a bridge that does not take sends get none.
func TestClaimHandsTheOwnersSends(t *testing.T) {
	srv, _, jobs := newOwnershipServer(t, false)
	sends := jobs.(store.BridgeSendStore)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if err := sends.PutBridgeSend(ctx, store.BridgeSend{SendID: "s-1", OwnerUserID: "maya", Component: "connections", Method: "GET", URL: "https://api.example.com/me"}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	claim := func(user, bridge string, body map[string]any) ([]store.AgentJob, []store.BridgeSend) {
		body["bridgeId"] = bridge
		resp := post(t, srv, "/v1/bridge/jobs/claim", user, body)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("claim = %d", resp.StatusCode)
		}
		var out struct {
			Jobs  []store.AgentJob   `json:"jobs"`
			Sends []store.BridgeSend `json:"sends"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out.Jobs, out.Sends
	}
	heartbeat(t, srv, "mallory", "bridge-mallory", false).Body.Close()
	if _, got := claim("mallory", "bridge-mallory", map[string]any{"maxJobs": 1, "takesSends": true}); len(got) != 0 {
		t.Fatalf("another member's bridge took %+v", got)
	}
	if _, got := claim("maya", "bridge-maya", map[string]any{"maxJobs": 1, "takesSends": false, "sendsOnly": true}); len(got) != 0 {
		t.Fatalf("a bridge that does not take sends took %+v", got)
	}
	gotJobs, got := claim("maya", "bridge-maya", map[string]any{"maxJobs": 1, "takesSends": true, "sendsOnly": true})
	if len(got) != 1 || got[0].SendID != "s-1" || got[0].URL != "https://api.example.com/me" || len(gotJobs) != 0 {
		t.Fatalf("sends only = jobs %+v sends %+v", gotJobs, got)
	}
	if _, again := claim("maya", "bridge-maya", map[string]any{"maxJobs": 1, "takesSends": true}); len(again) != 0 {
		t.Fatalf("a send taken twice: %+v", again)
	}
}
