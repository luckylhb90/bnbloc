package roomtools

import (
	"context"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Two agents of one owner and provider that differ only in their core: only
// the session's own (owner, core, provider) is "you", never a row whose core
// is unknown.
func TestListRoomAgentsMarksYouByTheWholeKey(t *testing.T) {
	gw := &fakeGateway{handle: func(method, path string, _ []byte) (int, any) {
		if method == http.MethodGet && path == "/v1/rooms/room-1/agents" {
			return 200, map[string]any{"agents": []wire.RosterAgent{
				{OwnerUserID: "jack", Core: "", Provider: "codex"},
				{OwnerUserID: "jack", Core: "acme", Provider: "codex"},
				{OwnerUserID: "jack", Core: "nimbus", Provider: "codex"},
			}}
		}
		return 404, map[string]any{"error": "no route"}
	}}
	_, _, sess := startServer(t, gw) // the session is jack/acme/codex
	agents, err := roomAgents(context.Background(), &Env{Gateway: gw, Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	var you []string
	for _, a := range agents {
		if a.You {
			you = append(you, a.Owner+"/"+a.Core+"/"+a.Provider)
		}
	}
	if len(you) != 1 || you[0] != "jack/acme/codex" {
		t.Fatalf("you = %v; want only jack/acme/codex", you)
	}
}
