package store

import (
	"errors"
	"testing"
)

func seedRoom(t *testing.T, s *MemoryRoom, id, owner string, vis Visibility) {
	t.Helper()
	if err := s.CreateRoom(testCtx(), Room{RoomID: id, Name: id, Visibility: vis, CreatedBy: owner, CreatedAtMs: 1}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if err := s.AddMember(testCtx(), Membership{RoomID: id, UserID: owner, Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("owner %s: %v", id, err)
	}
}

func TestCreateRoomDefaultsPrivate(t *testing.T) {
	s := NewMemoryRoom()
	if err := s.CreateRoom(testCtx(), Room{RoomID: "r1", Name: "r1", CreatedBy: "u"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoom(testCtx(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != VisibilityPrivate {
		t.Fatalf("unset visibility should default to private, got %q", got.Visibility)
	}
}

func TestSearchRoomsSubstringAndPaging(t *testing.T) {
	s := NewMemoryRoom()
	if err := s.CreateRoom(testCtx(), Room{RoomID: "alpha", Name: "Design Sync", Description: "ui work", CreatedAtMs: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(testCtx(), Room{RoomID: "beta", Name: "Backend", Description: "api design", CreatedAtMs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRoom(testCtx(), Room{RoomID: "gamma", Name: "Random", Description: "misc", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	// "design" matches alpha (name) and beta (description), not gamma.
	rooms, total, err := s.SearchRooms(testCtx(), "design", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rooms) != 2 {
		t.Fatalf("expected 2 matches, got total=%d len=%d", total, len(rooms))
	}
	// Newest-first: alpha (3) before beta (2).
	if rooms[0].RoomID != "alpha" || rooms[1].RoomID != "beta" {
		t.Fatalf("expected newest-first alpha,beta; got %s,%s", rooms[0].RoomID, rooms[1].RoomID)
	}
	// Empty query returns all; limit/offset paginate.
	page, total, _ := s.SearchRooms(testCtx(), "", 1, 1)
	if total != 3 || len(page) != 1 || page[0].RoomID != "beta" {
		t.Fatalf("paging: total=%d len=%d first=%v", total, len(page), page)
	}
}

func TestDeletedRoomHiddenFromPlazaAndGet(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "r1", "owner", VisibilityShared)
	if err := s.DeleteRoom(testCtx(), "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRoom(testCtx(), "r1"); err != ErrRoomNotFound {
		t.Fatalf("deleted room should be not found, got %v", err)
	}
	rooms, total, _ := s.SearchRooms(testCtx(), "", 0, 0)
	if total != 0 || len(rooms) != 0 {
		t.Fatalf("deleted room should not appear in plaza, got %d", total)
	}
	if must(s.IsMember(testCtx(), "r1", "owner"))(t) {
		t.Fatal("delete should drop memberships")
	}
}

func TestTransferOwnerSwapsRoles(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "r1", "owner", VisibilityShared)
	if err := s.AddMember(testCtx(), Membership{RoomID: "r1", UserID: "bob", Role: RoleMember, JoinedAtMs: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.TransferOwner(testCtx(), "r1", "bob"); err != nil {
		t.Fatal(err)
	}
	if role, _ := must2(s.MemberRole(testCtx(), "r1", "bob"))(t); role != RoleOwner {
		t.Fatalf("bob should be owner, got %q", role)
	}
	if role, _ := must2(s.MemberRole(testCtx(), "r1", "owner"))(t); role != RoleMember {
		t.Fatalf("prior owner should be demoted to member, got %q", role)
	}
}

func TestTransferOwnerRejectsNonMember(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "r1", "owner", VisibilityShared)
	if err := s.TransferOwner(testCtx(), "r1", "stranger"); err == nil {
		t.Fatal("transfer to a non-member must fail")
	}
	if role, _ := must2(s.MemberRole(testCtx(), "r1", "owner"))(t); role != RoleOwner {
		t.Fatalf("owner must be unchanged after failed transfer, got %q", role)
	}
}

func TestUpdateRoomPatchesOnlyProvided(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "r1", "owner", VisibilityPrivate)
	shared := VisibilityShared
	if err := s.UpdateRoom(testCtx(), "r1", RoomPatch{Visibility: &shared}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRoom(testCtx(), "r1")
	if got.Visibility != VisibilityShared {
		t.Fatalf("visibility not patched, got %q", got.Visibility)
	}
	if got.Name != "r1" {
		t.Fatalf("name should be unchanged, got %q", got.Name)
	}
}

func TestRoomsForUserDetailedFlagsOwnership(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "owned", "me", VisibilityPrivate)
	seedRoom(t, s, "theirs", "them", VisibilityShared)
	if err := s.AddMember(testCtx(), Membership{RoomID: "theirs", UserID: "me", Role: RoleMember, JoinedAtMs: 2}); err != nil {
		t.Fatal(err)
	}
	rows := must(s.RoomsForUserDetailed(testCtx(), "me"))(t)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rooms, got %d", len(rows))
	}
	byID := map[string]RoomMembershipDetail{}
	for _, r := range rows {
		byID[r.RoomID] = r
	}
	if !byID["owned"].IsOwner || byID["owned"].Role != RoleOwner {
		t.Fatal("owned room should be flagged owner")
	}
	if byID["theirs"].IsOwner || byID["theirs"].Role != RoleMember {
		t.Fatal("joined room should not be flagged owner")
	}
}

func TestNormalizeSyncModeDefaultsGit(t *testing.T) {
	cases := map[string]SyncMode{
		"":        SyncModeGit,
		"bogus":   SyncModeGit,
		"git":     SyncModeGit,
		"mutagen": SyncModeMutagen,
		"none":    SyncModeNone,
	}
	for in, want := range cases {
		if got := NormalizeSyncMode(in); got != want {
			t.Fatalf("NormalizeSyncMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCreateRoomDefaultsSyncGit(t *testing.T) {
	s := NewMemoryRoom()
	if err := s.CreateRoom(testCtx(), Room{RoomID: "r1", Name: "r1", CreatedBy: "u"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRoom(testCtx(), "r1")
	if got.SyncMode != SyncModeGit {
		t.Fatalf("unset sync mode should default to git, got %q", got.SyncMode)
	}
}

func TestUpdateRoomPatchesSyncMode(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "r1", "owner", VisibilityShared)
	m := SyncModeMutagen
	if err := s.UpdateRoom(testCtx(), "r1", RoomPatch{SyncMode: &m}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRoom(testCtx(), "r1")
	if got.SyncMode != SyncModeMutagen {
		t.Fatalf("sync mode not patched, got %q", got.SyncMode)
	}
	if got.Visibility != VisibilityShared {
		t.Fatalf("visibility should be unchanged, got %q", got.Visibility)
	}
}

// BE-1: creating a room whose id exists must fail, never upsert: the former
// ON DUPLICATE KEY UPDATE renamed the room and stamped the caller its owner.
func TestCreateRoomRejectsExistingID(t *testing.T) {
	s := NewMemoryRoom()
	seedRoom(t, s, "taken", "alice", VisibilityPrivate)
	err := s.CreateRoomWithOwner(testCtx(), Room{RoomID: "taken", Name: "mine now", CreatedBy: "mallory", CreatedAtMs: 2},
		Membership{RoomID: "taken", UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2})
	if !errors.Is(err, ErrRoomExists) {
		t.Fatalf("create existing id = %v, want ErrRoomExists", err)
	}
	if err := s.CreateRoom(testCtx(), Room{RoomID: "taken", Name: "x"}); !errors.Is(err, ErrRoomExists) {
		t.Fatalf("CreateRoom existing id = %v, want ErrRoomExists", err)
	}
	got, _ := s.GetRoom(testCtx(), "taken")
	if got.Name != "taken" {
		t.Fatalf("room renamed to %q by a failed create", got.Name)
	}
	if must(s.IsMember(testCtx(), "taken", "mallory"))(t) {
		t.Fatal("failed create must not add the caller as a member")
	}
}

// BE-24: deleting a room removes the rows this store owns for it — members,
// invites, and channels — and a second delete reports not found.
func TestDeleteRoomRemovesOwnedRows(t *testing.T) {
	s := NewMemoryRoom()
	ch := s.Channels()
	if err := s.CreateRoomWithOwnerAndChannel(testCtx(), Room{RoomID: "gone", Name: "gone", CreatedBy: "alice", CreatedAtMs: 1},
		Membership{RoomID: "gone", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1},
		RoomChannel{ChannelID: "ch-1", RoomID: "gone", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.CreateInvite(testCtx(), Invite{Code: "c1", RoomID: "gone", Role: RoleMember, ExpiresAtMs: 1 << 62, Capacity: 1}); err != nil {
		t.Fatalf("invite: %v", err)
	}
	if err := s.DeleteRoom(testCtx(), "gone"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := must(ch.ListChannels(testCtx(), "gone"))(t); len(got) != 0 {
		t.Fatalf("channels after delete = %+v, want none", got)
	}
	if _, ok := must2(ch.DefaultChannel(testCtx(), "gone"))(t); ok {
		t.Fatal("default channel must be gone")
	}
	if must(s.IsMember(testCtx(), "gone", "alice"))(t) || must(s.MemberCount(testCtx(), "gone"))(t) != 0 {
		t.Fatal("memberships must be gone")
	}
	if _, err := s.RedeemInvite(testCtx(), "c1", "bob", 2); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("invite redeem after delete = %v, want ErrInviteNotFound", err)
	}
	if err := s.DeleteRoom(testCtx(), "gone"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("second delete = %v, want ErrRoomNotFound", err)
	}
}
