package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// dctx is a context scoped to the default tenant, matching what the
// service Handler (svcauth.EnsureTenant) injects for HTTP requests, so
// direct-store seeds land in the same tenant the handlers read.
func dctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func newTestServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	rooms := store.NewMemoryRoom()
	svc := New(rooms, rooms.Channels())
	clock := int64(1000)
	svc.Now = func() int64 { return clock }
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return srv, svc
}

func do(t *testing.T, method, url, user string, body any) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		rdr = bytes.NewReader(buf)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	if user != "" {
		req.Header.Set("X-Bnbloc-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// Creating a room makes the creator the owner and the room readable by them.
func TestCreateRoomMakesCreatorOwner(t *testing.T) {
	srv, svc := newTestServer(t)
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d, want 200", resp.StatusCode)
	}
	role, ok := svc.Store.MemberRole(dctx(), "room-1", "alice")
	if !ok || role != store.RoleOwner {
		t.Fatalf("creator role = %v ok=%v, want owner", role, ok)
	}
}

// A non-owner/admin may not add members (fail closed).
func TestAddMemberRequiresPrivilege(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "bob", Role: store.RoleMember})
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/members", "bob", map[string]string{"userId": "carol", "role": "member"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// Invite redemption is atomic and enforces expiry and capacity.
func TestInviteExpiryAndCapacity(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner})

	// Expired invite -> 403.
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "expired", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: 500, Capacity: 5})
	resp := do(t, http.MethodPost, srv.URL+"/v1/invites/expired/redeem", "dave", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expired redeem status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()

	// Capacity 1 -> second distinct user is rejected.
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "cap1", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: 1_000_000, Capacity: 1})
	r1 := do(t, http.MethodPost, srv.URL+"/v1/invites/cap1/redeem", "eve", nil)
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first redeem status = %d, want 200", r1.StatusCode)
	}
	r1.Body.Close()
	r2 := do(t, http.MethodPost, srv.URL+"/v1/invites/cap1/redeem", "frank", nil)
	if r2.StatusCode != http.StatusForbidden {
		t.Fatalf("over-capacity redeem status = %d, want 403", r2.StatusCode)
	}
	r2.Body.Close()
}

// Redeeming twice for the same user is idempotent (no capacity consumed twice).
func TestInviteRedeemIdempotent(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "code", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: 1_000_000, Capacity: 1})
	for range 2 {
		resp := do(t, http.MethodPost, srv.URL+"/v1/invites/code/redeem", "grace", nil)
		status := resp.StatusCode
		resp.Body.Close()
		if status != http.StatusOK {
			t.Fatalf("redeem status = %d, want 200", status)
		}
	}
}

// The member probe returns 200 for members and 404 for non-members.
func TestGetMemberProbe(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner})
	yes := do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/members/alice", "alice", nil)
	if yes.StatusCode != http.StatusOK {
		t.Fatalf("member probe = %d, want 200", yes.StatusCode)
	}
	yes.Body.Close()
	no := do(t, http.MethodGet, srv.URL+"/v1/rooms/room-1/members/stranger", "alice", nil)
	if no.StatusCode != http.StatusNotFound {
		t.Fatalf("non-member probe = %d, want 404", no.StatusCode)
	}
	no.Body.Close()
}
