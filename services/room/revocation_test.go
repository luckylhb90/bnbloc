package room

import (
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// BE-1: re-creating an existing room id is a 409 and never hands the caller
// the room (the store used to upsert and stamp the caller owner).
func TestCreateRoomExistingIDConflicts(t *testing.T) {
	srv, svc := newTestServer(t)
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	resp = do(t, http.MethodPost, srv.URL+"/v1/rooms", "mallory", map[string]string{"roomId": "room-1", "name": "Mine"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-create status = %d, want 409", resp.StatusCode)
	}
	isMember, err := svc.Store.IsMember(dctx(), "room-1", "mallory")
	if err != nil {
		t.Fatal(err)
	}
	if isMember {
		t.Fatal("mallory must not have become a member")
	}
	if rm, _ := svc.Store.GetRoom(dctx(), "room-1"); rm.Name != "Team" {
		t.Fatalf("room renamed to %q", rm.Name)
	}
}

// BE-9: a room id that cannot be stored (VARCHAR(64)) or routed as one path
// segment is refused at create.
func TestCreateRoomRejectsInvalidID(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, id := range []string{"a/b", "chat?x", "50%", strings.Repeat("x", 65), "tab\there"} {
		resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": id, "name": "n"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("create %q = %d, want 400", id, resp.StatusCode)
		}
	}
}

// BE-17: control bodies are size-capped.
func TestCreateRoomRejectsOversizedBody(t *testing.T) {
	srv, _ := newTestServer(t)
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "r", "description": strings.Repeat("x", maxJSONBodyBytes)})
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized create = %d, want 413", resp.StatusCode)
	}
}

// BE-10: ending a member's access publishes member_removed (kick and leave)
// and deleting the room publishes room_deleted, so realtime closes the live
// streams immediately. Events carry the room's tenant in the publish context.
func TestAccessRevocationPublishesEvents(t *testing.T) {
	srv, svc := newTestServer(t)
	pub := &capturePublisher{}
	svc.Publisher = pub
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"})
	resp.Body.Close()
	for _, u := range []string{"bob", "carol"} {
		if err := svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: u, Role: store.RoleMember}); err != nil {
			t.Fatal(err)
		}
	}

	resp = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/members/bob", "alice", nil)
	resp.Body.Close()
	resp = do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/leave", "carol", nil)
	resp.Body.Close()
	resp = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1", "alice", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", resp.StatusCode)
	}

	if len(pub.events) != 3 {
		t.Fatalf("published %d events, want 3: %+v", len(pub.events), pub.events)
	}
	for i, want := range []string{"bob", "carol"} {
		ev := pub.events[i]
		p, ok := ev.Payload.(wire.MemberRemovedPayload)
		if ev.Kind != wire.EventMemberRemoved || ev.RoomID != "room-1" || !ok || p.UserID != want {
			t.Fatalf("event %d = %+v, want member_removed for %s", i, ev, want)
		}
	}
	if ev := pub.events[2]; ev.Kind != wire.EventRoomDeleted || ev.RoomID != "room-1" {
		t.Fatalf("event 2 = %+v, want room_deleted", ev)
	}
}

// A refused removal (owner, non-admin caller) publishes nothing.
func TestRefusedRemovalPublishesNothing(t *testing.T) {
	srv, svc := newTestServer(t)
	pub := &capturePublisher{}
	svc.Publisher = pub
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"})
	resp.Body.Close()
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember})
	resp = do(t, http.MethodDelete, srv.URL+"/v1/rooms/room-1/members/alice", "bob", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(pub.events) != 0 {
		t.Fatalf("refused kick = %d with %d events, want 403 and none", resp.StatusCode, len(pub.events))
	}
}

// Every way a person becomes a member publishes member_joined (realtime
// hands them the room's standing content shares); a re-role does not.
func TestJoinsPublishMemberJoined(t *testing.T) {
	srv, svc := newTestServer(t)
	pub := &capturePublisher{}
	svc.Publisher = pub
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team", "visibility": "shared"})
	resp.Body.Close()
	pub.events = nil
	steps := []struct {
		user, method, path string
		body               any
	}{
		{"alice", http.MethodPost, "/v1/rooms/room-1/members", map[string]string{"userId": "bob"}},
		{"alice", http.MethodPost, "/v1/rooms/room-1/members", map[string]string{"userId": "bob", "role": "admin"}}, // a re-role
		{"carol", http.MethodPost, "/v1/rooms/room-1/join", nil},
		{"dave", http.MethodPost, "/v1/invites/inv1/redeem", nil},
	}
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "inv1", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: 1 << 50, Capacity: 5})
	for _, st := range steps {
		resp := do(t, st.method, srv.URL+st.path, st.user, st.body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s = %d", st.method, st.path, resp.StatusCode)
		}
	}
	var joined []string
	for _, ev := range pub.events {
		if ev.Kind != wire.EventMemberJoined || ev.RoomID != "room-1" {
			t.Fatalf("event = %+v", ev)
		}
		joined = append(joined, ev.Payload.(wire.MemberJoinedPayload).UserID)
	}
	if strings.Join(joined, ",") != "bob,carol,dave" {
		t.Fatalf("joined = %v, want bob,carol,dave", joined)
	}
}
