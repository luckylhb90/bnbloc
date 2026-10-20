package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// namedExpert is expertForm under another name.
func namedExpert(name string) map[string]any {
	form := expertForm("Be helpful.")
	form["form"].(map[string]any)["name"] = name
	return form
}

// The member catalog with a job grant is the requester's Library as an agent
// reads it (the room tools' components list, the bridge's .room/library.md):
// exactly what the visibility owners admit for the requester, content and MCP
// tools only. The private component of another member never appears, the
// bridge owner's own included.
func TestGrantedCatalogIsTheRequestersLibrary(t *testing.T) {
	g, _ := mcpGateway(t, false)
	for user, name := range map[string]string{"maya": "maya-reviewer", "jack": "jack-private", "bob": "bob-private"} {
		if code, raw := g.json(user, http.MethodPost, "/v1/content-components", namedExpert(name)); code != http.StatusCreated {
			t.Fatalf("%s create = %d %s", user, code, raw)
		}
	}
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	// maya asked jack's agent: jack's bridge reads maya's Library.
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "opencode",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	read := func(user, path string) (int, wire.ComponentsReply) {
		t.Helper()
		code, raw := g.do(user, http.MethodGet, path, "", nil)
		var reply wire.ComponentsReply
		if code == http.StatusOK {
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatalf("decode %s: %v", raw, err)
			}
		}
		return code, reply
	}
	names := func(r wire.ComponentsReply) map[string]bool {
		out := map[string]bool{}
		for _, c := range r.ContentComponents {
			out[c.Name] = true
		}
		return out
	}

	code, granted := read("jack", "/v1/components?job=job-1&room=room-1")
	if code != http.StatusOK {
		t.Fatalf("granted catalog = %d", code)
	}
	if got := names(granted); !got["maya-reviewer"] || got["jack-private"] || got["bob-private"] {
		t.Fatalf("granted Library = %v, want maya's own and no one else's private component", got)
	}
	if len(granted.Components) != 2 {
		t.Fatalf("granted executables = %+v, want the two MCP tools", granted.Components)
	}
	for _, c := range granted.Components {
		if c.Kind != "mcp" || c.PublishedAtMs <= 0 {
			t.Fatalf("granted executable %+v: want an MCP tool with its publish time", c)
		}
	}
	// The same caller without the grant reads their own Library.
	if _, own := read("jack", "/v1/components"); !names(own)["jack-private"] || names(own)["maya-reviewer"] {
		t.Fatalf("jack's own Library = %v", names(own))
	}
	// A bridge that does not run the job is refused, never answered as itself.
	if code, _ := read("bob", "/v1/components?job=job-1&room=room-1"); code != http.StatusForbidden {
		t.Fatalf("another bridge's granted catalog = %d, want 403", code)
	}
}
