package room

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's invite code is four characters of the unambiguous alphabet, so it
// can be read out and typed.
func TestInviteCodeIsShortAndUnambiguous(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 2000; i++ {
		code := randomInviteCode()
		if len(code) != wire.InviteCodeLen {
			t.Fatalf("code %q has %d characters, want %d", code, len(code), wire.InviteCodeLen)
		}
		for j := 0; j < len(code); j++ {
			if !strings.ContainsRune(wire.InviteCodeAlphabet, rune(code[j])) {
				t.Fatalf("code %q has %q outside the alphabet", code, code[j])
			}
			seen[code[j]] = true
		}
	}
	if len(seen) != len(wire.InviteCodeAlphabet) {
		t.Fatalf("2000 codes used %d of %d symbols", len(seen), len(wire.InviteCodeAlphabet))
	}
	if strings.ContainsAny(wire.InviteCodeAlphabet, "01OIL") {
		t.Fatalf("alphabet %q has symbols that read alike", wire.InviteCodeAlphabet)
	}
}

// takenCodes answers ErrInviteExists for the first n creates, as a store does
// when a drawn code is already taken.
type takenCodes struct {
	store.RoomStore
	n, tries int
}

func (t *takenCodes) CreateInvite(ctx context.Context, inv store.Invite) error {
	t.tries++
	if t.tries <= t.n {
		return store.ErrInviteExists
	}
	return t.RoomStore.CreateInvite(ctx, inv)
}

func TestInviteCreationDrawsAgainWhenACodeIsTaken(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner})
	taken := &takenCodes{RoomStore: svc.Store, n: maxInviteCodeDraws - 1}
	svc.Store = taken
	resp := do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/invites", "alice", map[string]any{"capacity": 1})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create with %d codes taken = %d, want 200", taken.n, resp.StatusCode)
	}
	resp.Body.Close()
	taken.tries, taken.n = 0, maxInviteCodeDraws
	resp = do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/invites", "alice", map[string]any{"capacity": 1})
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || taken.tries != maxInviteCodeDraws {
		t.Fatalf("create with every draw taken = %d after %d draws, want 500 after %d", resp.StatusCode, taken.tries, maxInviteCodeDraws)
	}
}

// An invite lives a day unless asked for longer and never more than a week
// (a short code is safe only while short-lived); a dead one frees its code.
func TestInviteLifeIsShortAndDeadCodesAreFreed(t *testing.T) {
	srv, svc := newTestServer(t)
	_ = svc.Store.CreateRoom(dctx(), store.Room{RoomID: "room-1"})
	_ = svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: "alice", Role: store.RoleOwner})
	now := svc.Now()
	create := func(body map[string]any) map[string]any {
		t.Helper()
		resp := do(t, http.MethodPost, srv.URL+"/v1/rooms/room-1/invites", "alice", body)
		return decodeJSON(t, resp)
	}
	if got := create(map[string]any{"capacity": 1})["expiresAtMs"].(float64); int64(got) != now+defaultInviteTTLMs {
		t.Fatalf("default expiry = %v, want now + a day", got)
	}
	if got := create(map[string]any{"capacity": 1, "expiresAtMs": now + 30*24*3600*1000})["expiresAtMs"].(float64); int64(got) != now+maxInviteTTLMs {
		t.Fatalf("a month's expiry = %v, want capped at a week", got)
	}
	// A used-up and an expired invite are gone after the next create.
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "FULL", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: now + 1e9, Capacity: 1, RedeemedCount: 1, CreatedBy: "alice"})
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "OLD2", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: now - 1, Capacity: 5, CreatedBy: "alice"})
	_ = svc.Store.CreateInvite(dctx(), store.Invite{Code: "LIVE", RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: now + 1e9, Capacity: 5, CreatedBy: "alice"})
	create(map[string]any{"capacity": 1})
	for code, freed := range map[string]bool{"FULL": true, "OLD2": true, "LIVE": false} {
		err := svc.Store.CreateInvite(dctx(), store.Invite{Code: code, RoomID: "room-1", Role: store.RoleMember, ExpiresAtMs: now + 1e9, Capacity: 1, CreatedBy: "alice"})
		if freed != (err == nil) {
			t.Errorf("code %s: create again = %v; a live code stays taken, a dead one is free", code, err)
		}
	}
}
