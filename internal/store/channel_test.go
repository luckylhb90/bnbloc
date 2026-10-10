package store

import (
	"context"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

func chctx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func TestMemoryChannelDefaultAndCRUD(t *testing.T) {
	ctx := chctx()
	s := NewMemoryChannel()

	def := RoomChannel{ChannelID: "ch_def", TenantID: tenant.Default, RoomID: "room-1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}
	if err := s.PutChannel(ctx, def); err != nil {
		t.Fatalf("put default: %v", err)
	}
	if err := s.PutChannel(ctx, RoomChannel{ChannelID: "ch_fe", TenantID: tenant.Default, RoomID: "room-1", Name: "frontend", CreatedBy: "bob", CreatedAtMs: 2}); err != nil {
		t.Fatalf("put channel: %v", err)
	}

	list := s.ListChannels(ctx, "room-1")
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	// default must sort first
	if !list[0].IsDefault {
		t.Fatalf("first listed channel should be the default, got %+v", list[0])
	}

	d, ok := s.DefaultChannel(ctx, "room-1")
	if !ok || d.ChannelID != "ch_def" {
		t.Fatalf("default = %+v,%v want ch_def", d, ok)
	}

	got, ok := s.GetChannel(ctx, "room-1", "ch_fe")
	if !ok || got.Name != "frontend" {
		t.Fatalf("get = %+v,%v", got, ok)
	}

	if err := s.DeleteChannel(ctx, "room-1", "ch_fe", 3); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := s.GetChannel(ctx, "room-1", "ch_fe"); ok {
		t.Fatal("deleted channel still present in GetChannel")
	}
	// A soft-deleted channel disappears from every live view, not just Get.
	if got := s.ListChannels(ctx, "room-1"); len(got) != 1 || got[0].ChannelID != "ch_def" {
		t.Fatalf("after delete, list = %+v, want only ch_def", got)
	}
	if _, ok := s.DefaultChannel(ctx, "room-1"); !ok {
		t.Fatal("default channel must survive a sibling's deletion")
	}
}

// Room ids may be reused across tenants, so every channel operation must be
// scoped by the context tenant: tenant B must not see or touch tenant A's
// channels even under a colliding room id.
func TestMemoryChannelTenantIsolation(t *testing.T) {
	ctxA := chctx()
	ctxB := tenant.NewContext(context.Background(), "acme")
	s := NewMemoryChannel()

	put := func(ctx context.Context, id, name string, def bool) {
		t.Helper()
		if err := s.PutChannel(ctx, RoomChannel{ChannelID: id, RoomID: "room-1", Name: name, IsDefault: def, CreatedBy: "x", CreatedAtMs: 1}); err != nil {
			t.Fatalf("put %s: %v", id, err)
		}
	}
	put(ctxA, "ch_a", "general", true)
	put(ctxB, "ch_b", "general", true) // same room id, other tenant

	if got := s.ListChannels(ctxA, "room-1"); len(got) != 1 || got[0].ChannelID != "ch_a" {
		t.Fatalf("tenant A list = %+v, want only ch_a", got)
	}
	if got := s.ListChannels(ctxB, "room-1"); len(got) != 1 || got[0].ChannelID != "ch_b" {
		t.Fatalf("tenant B list = %+v, want only ch_b", got)
	}
	if _, ok := s.GetChannel(ctxA, "room-1", "ch_b"); ok {
		t.Fatal("tenant A must not read tenant B's channel")
	}
	if _, ok := s.DefaultChannel(ctxB, "room-1"); !ok {
		t.Fatal("tenant B must see its own default channel")
	}
	// A delete under tenant B never removes tenant A's channel.
	if err := s.DeleteChannel(ctxB, "room-1", "ch_a", 9); err != nil {
		t.Fatalf("cross-tenant delete call errored: %v", err)
	}
	if _, ok := s.GetChannel(ctxA, "room-1", "ch_a"); !ok {
		t.Fatal("tenant B's delete removed tenant A's channel")
	}
}

// Rename is conditional on liveness: renaming a soft-deleted channel fails
// with ErrChannelNotFound and never resurrects the row.
func TestMemoryChannelRenameDoesNotResurrect(t *testing.T) {
	ctx := chctx()
	s := NewMemoryChannel()
	if err := s.PutChannel(ctx, RoomChannel{ChannelID: "ch_fe", RoomID: "room-1", Name: "frontend", CreatedBy: "bob", CreatedAtMs: 1}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.DeleteChannel(ctx, "room-1", "ch_fe", 2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.RenameChannel(ctx, "room-1", "ch_fe", "renamed"); err != ErrChannelNotFound {
		t.Fatalf("rename of deleted channel = %v, want ErrChannelNotFound", err)
	}
	if _, ok := s.GetChannel(ctx, "room-1", "ch_fe"); ok {
		t.Fatal("deleted channel must stay deleted after a failed rename")
	}
	if got := s.ListChannels(ctx, "room-1"); len(got) != 0 {
		t.Fatalf("list after delete+failed rename = %+v, want empty", got)
	}
	// Sanity: renaming a live channel does rename it.
	if err := s.PutChannel(ctx, RoomChannel{ChannelID: "ch_ok", RoomID: "room-1", Name: "ok", CreatedBy: "bob", CreatedAtMs: 3}); err != nil {
		t.Fatalf("put live: %v", err)
	}
	if err := s.RenameChannel(ctx, "room-1", "ch_ok", "ok-v2"); err != nil {
		t.Fatalf("rename live: %v", err)
	}
	if c, ok := s.GetChannel(ctx, "room-1", "ch_ok"); !ok || c.Name != "ok-v2" {
		t.Fatalf("live rename result = %+v,%v want ok-v2", c, ok)
	}
	// Renaming an absent channel is also ErrChannelNotFound.
	if err := s.RenameChannel(ctx, "room-1", "ch_missing", "x"); err != ErrChannelNotFound {
		t.Fatalf("rename of absent channel = %v, want ErrChannelNotFound", err)
	}
}

// CreateRoomWithOwnerAndChannel seeds the default channel in the same critical
// section as the room, and the Channels() view — the wiring the service and
// NewBackends use — reads that same storage.
func TestCreateRoomWithOwnerAndChannelSeedsDefault(t *testing.T) {
	ctx := chctx()
	rooms := NewMemoryRoom()
	channels := rooms.Channels()
	def := RoomChannel{ChannelID: "ch_gen", RoomID: "r1", Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}
	if err := rooms.CreateRoomWithOwnerAndChannel(ctx, Room{RoomID: "r1", Name: "r1", CreatedBy: "alice", CreatedAtMs: 1}, Membership{RoomID: "r1", UserID: "alice", Role: RoleOwner, JoinedAtMs: 1}, def); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := rooms.GetRoom(ctx, "r1"); err != nil {
		t.Fatalf("room missing after create: %v", err)
	}
	if role, ok := rooms.MemberRole(ctx, "r1", "alice"); !ok || role != RoleOwner {
		t.Fatalf("owner role = %v,%v want owner", role, ok)
	}
	got, ok := channels.DefaultChannel(ctx, "r1")
	if !ok || got.ChannelID != "ch_gen" || got.Name != "general" || !got.IsDefault {
		t.Fatalf("default via view = %+v,%v want ch_gen/general/default", got, ok)
	}
	if list := channels.ListChannels(ctx, "r1"); len(list) != 1 || !list[0].IsDefault {
		t.Fatalf("list via view = %+v, want exactly the default", list)
	}
}
