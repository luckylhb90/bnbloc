package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// chView mirrors the channel wire shape; declared locally so the test pins the
// HTTP contract rather than the handler's struct.
type chView struct {
	ChannelID string `json:"channelId"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	CreatedBy string `json:"createdBy"`
}

func listChannels(t *testing.T, srv *httptest.Server, user string) []chView {
	t.Helper()
	resp := do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/channels", user, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list channels as %s: status = %d, want 200", user, resp.StatusCode)
	}
	var out struct {
		Channels []chView `json:"channels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode channels: %v", err)
	}
	return out.Channels
}

// The channel lifecycle: room creation seeds a default channel (created in the
// room's own transaction), members create and rename channels within the
// permission model (create: any member; rename: owner/admin or creator;
// delete: owner/admin only), the default channel is immutable, and non-members
// see nothing.
func TestChannelLifecycle(t *testing.T) {
	srv, svc := newTestServer(t)
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create room status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "carol", Role: store.RoleMember})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "erin", Role: store.RoleAdmin})

	// Room creation seeds exactly one channel: the default "general".
	channels := listChannels(t, srv, "alice")
	if len(channels) != 1 || channels[0].Name != "general" || !channels[0].IsDefault || channels[0].CreatedBy != "alice" {
		t.Fatalf("channels after create = %+v, want a single default general created by alice", channels)
	}
	def := channels[0]

	// Any member may create a channel; it appears in the list.
	created := do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/channels", "bob", map[string]string{"name": "frontend"})
	if created.StatusCode != http.StatusOK {
		t.Fatalf("member create channel status = %d, want 200", created.StatusCode)
	}
	var fe chView
	if err := json.NewDecoder(created.Body).Decode(&fe); err != nil {
		t.Fatalf("decode created channel: %v", err)
	}
	created.Body.Close()
	if fe.Name != "frontend" || fe.IsDefault || fe.ChannelID == "" || fe.CreatedBy != "bob" {
		t.Fatalf("created channel = %+v, want non-default frontend by bob with an id", fe)
	}
	if got := listChannels(t, srv, "alice"); len(got) != 2 {
		t.Fatalf("channels after member create = %d, want 2", len(got))
	}

	// A name of blanks is not a name.
	for _, bad := range []string{"", "   "} {
		r := do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/channels", "bob", map[string]string{"name": bad})
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("create channel with name %q status = %d, want 400", bad, r.StatusCode)
		}
		r.Body.Close()
	}

	// The default channel can never be deleted.
	r := do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/channels/"+def.ChannelID, "alice", nil)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("owner delete default status = %d, want 409", r.StatusCode)
	}
	r.Body.Close()

	// Delete is owner/admin only; the channel's creator is not enough.
	r = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/channels/"+fe.ChannelID, "bob", nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("member delete channel status = %d, want 403", r.StatusCode)
	}
	r.Body.Close()
	r = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/channels/"+fe.ChannelID, "alice", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("owner delete channel status = %d, want 200", r.StatusCode)
	}
	r.Body.Close()
	// The delete actually took: only the default channel remains.
	if left := listChannels(t, srv, "alice"); len(left) != 1 || left[0].ChannelID != def.ChannelID || !left[0].IsDefault {
		t.Fatalf("channels after owner delete = %+v, want only the default", left)
	}

	// Rename: the creator (a plain member) may rename their channel.
	r = do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/channels", "bob", map[string]string{"name": "design"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("create design status = %d, want 200", r.StatusCode)
	}
	var de chView
	if err := json.NewDecoder(r.Body).Decode(&de); err != nil {
		t.Fatalf("decode design channel: %v", err)
	}
	r.Body.Close()
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/"+de.ChannelID, "bob", map[string]string{"name": "design-v2"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("creator rename status = %d, want 200", r.StatusCode)
	}
	r.Body.Close()
	found := false
	for _, c := range listChannels(t, srv, "alice") {
		if c.ChannelID == de.ChannelID {
			found = true
			if c.Name != "design-v2" {
				t.Fatalf("renamed channel = %+v, want name design-v2", c)
			}
		}
	}
	if !found {
		t.Fatalf("channel %s missing after rename", de.ChannelID)
	}

	// A plain member may not rename someone else's channel.
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/"+de.ChannelID, "carol", map[string]string{"name": "carols"})
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("non-creator member rename status = %d, want 403", r.StatusCode)
	}
	r.Body.Close()

	// An admin may rename (and later delete) a channel they did not create.
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/"+de.ChannelID, "erin", map[string]string{"name": "design-v3"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin rename status = %d, want 200", r.StatusCode)
	}
	r.Body.Close()

	// The default channel can never be renamed, even by the owner.
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/"+def.ChannelID, "alice", map[string]string{"name": "lobby"})
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("owner rename default status = %d, want 409", r.StatusCode)
	}
	r.Body.Close()

	// A blank rename is rejected.
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/"+de.ChannelID, "bob", map[string]string{"name": "  "})
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank rename status = %d, want 400", r.StatusCode)
	}
	r.Body.Close()

	// Unknown channel ids are 404, not 403/500 — for both rename and delete.
	r = do(t, http.MethodPatch, srv.URL+"/v1/rooms/room-1/channels/ch_missing", "alice", map[string]string{"name": "x"})
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("rename unknown channel status = %d, want 404", r.StatusCode)
	}
	r.Body.Close()
	r = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/channels/ch_missing", "alice", nil)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown channel status = %d, want 404", r.StatusCode)
	}
	r.Body.Close()

	// An admin may delete a channel another member created.
	r = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/channels/"+de.ChannelID, "erin", nil)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("admin delete status = %d, want 200", r.StatusCode)
	}
	r.Body.Close()
	if left := listChannels(t, srv, "alice"); len(left) != 1 || left[0].ChannelID != def.ChannelID {
		t.Fatalf("channels after admin delete = %+v, want only the default", left)
	}

	// A non-member may not even list the channels.
	r = do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/channels", "stranger", nil)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member list status = %d, want 403", r.StatusCode)
	}
	r.Body.Close()
}
