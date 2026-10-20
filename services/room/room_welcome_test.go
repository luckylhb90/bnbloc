package room

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room keeps its welcome and quick prompts: set at creation (a room
// package's install), changed by its owner or an admin, read by every
// member; a plain member and a visitor are refused, and the bounds are a
// room package's.
func TestRoomWelcome(t *testing.T) {
	srv, svc := newTestServer(t)
	create := map[string]any{"roomId": "room-1", "name": "Review", "welcome": "  Ask me.  ",
		"quickPrompts": []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}}}
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "maya", create)
	var created wire.RoomDetail
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || created.Welcome != "Ask me." || len(created.QuickPrompts) != 1 {
		t.Fatalf("create = %d %+v", resp.StatusCode, created)
	}
	for user, role := range map[string]store.Role{"ada": store.RoleAdmin, "jack": store.RoleMember, "vic": store.RoleVisitor} {
		_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: user, Role: role})
	}

	// An admin changes the welcome alone; the quick prompts stay.
	resp = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/welcome", "ada", map[string]any{"welcome": "Ask about last week."})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin = %d", resp.StatusCode)
	}
	for _, who := range []string{"jack", "vic", "olaf"} {
		resp := do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/welcome", who, map[string]any{"welcome": "Mine."})
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s = %d", who, resp.StatusCode)
		}
	}
	// Every member reads them.
	resp = do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1", "vic", nil)
	var got wire.RoomDetail
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Welcome != "Ask about last week." || !slices.Equal(got.QuickPrompts, []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}}) {
		t.Fatalf("a visitor reads %+v", got)
	}
	// The owner clears the quick prompts.
	resp = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/welcome", "maya", map[string]any{"quickPrompts": []any{}})
	resp.Body.Close()
	if rm, _ := svc.Store.GetRoom(dctx(), "room-1"); resp.StatusCode != http.StatusOK || len(rm.QuickPrompts) != 0 || rm.Welcome != "Ask about last week." {
		t.Fatalf("cleared = %d %+v", resp.StatusCode, rm)
	}

	// A room package's bounds: 12 quick prompts, each a title and its text,
	// a welcome of 4000 characters, a description of 500.
	many := make([]wire.RoomQuickPrompt, 13)
	for i := range many {
		many[i] = wire.RoomQuickPrompt{Title: "t", Prompt: "p"}
	}
	for name, c := range map[string]struct {
		path string
		body map[string]any
		want string
	}{
		"too many":      {"/v1/rooms/room-1/welcome", map[string]any{"quickPrompts": many}, "at most 12 quick prompts"},
		"no text":       {"/v1/rooms/room-1/welcome", map[string]any{"quickPrompts": []wire.RoomQuickPrompt{{Title: "Recap"}}}, "needs a title and a prompt"},
		"long welcome":  {"/v1/rooms/room-1/welcome", map[string]any{"welcome": strings.Repeat("é", 4001)}, "welcome is longer than 4000"},
		"long title":    {"/v1/rooms/room-1/welcome", map[string]any{"quickPrompts": []wire.RoomQuickPrompt{{Title: strings.Repeat("t", 81), Prompt: "p"}}}, "longer than 80"},
		"long describe": {"/v1/rooms/room-1", map[string]any{"description": strings.Repeat("d", 501)}, "longer than 500"},
	} {
		resp := do(t, http.MethodPatch, srv.URL+c.path, "maya", c.body)
		var body struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body.Error, c.want) {
			t.Errorf("%s = %d %q", name, resp.StatusCode, body.Error)
		}
	}
	resp = do(t, http.MethodPost, srv.URL+"/v1/rooms", "maya", map[string]any{"roomId": "room-2", "quickPrompts": many})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a create past the bounds = %d", resp.StatusCode)
	}
	if _, err := svc.Store.GetRoom(dctx(), "room-2"); err == nil {
		t.Fatal("a refused create made the room")
	}
}
