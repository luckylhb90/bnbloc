package store

import (
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room keeps its welcome and quick prompts (GetRoom reads them): set at
// creation, patched alone, cleared; a patch of other fields leaves them.
func TestContractRoomStoreWelcome(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-welcome-"))
		s := b.Room
		id := uniq("room-")
		prompts := []wire.RoomQuickPrompt{{Title: "Recap", Prompt: "Recap the week"}, {Title: "Misses", Prompt: strings.Repeat("é", 4000)}}
		if err := s.CreateRoomWithOwner(ctx, Room{RoomID: id, Name: "n", CreatedBy: "maya", CreatedAtMs: 1, Welcome: "Hello.", QuickPrompts: prompts},
			Membership{RoomID: id, UserID: "maya", Role: RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetRoom(ctx, id); err != nil || got.Welcome != "Hello." || !slices.Equal(got.QuickPrompts, prompts) {
			t.Fatalf("GetRoom = %+v, %v", got, err)
		}
		desc, welcome := "d", strings.Repeat("w", 4000)
		if err := s.UpdateRoom(ctx, id, RoomPatch{Description: &desc}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpdateRoom(ctx, id, RoomPatch{Welcome: &welcome}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetRoom(ctx, id); got.Welcome != welcome || !slices.Equal(got.QuickPrompts, prompts) || got.Description != "d" {
			t.Fatalf("after a welcome patch = %+v", got)
		}
		none := []wire.RoomQuickPrompt{}
		if err := s.UpdateRoom(ctx, id, RoomPatch{QuickPrompts: &none}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetRoom(ctx, id); len(got.QuickPrompts) != 0 || got.Welcome != welcome {
			t.Fatalf("after clearing = %+v", got)
		}
		// A room created without them reads none.
		bare := uniq("room-")
		if err := s.CreateRoom(ctx, Room{RoomID: bare, Name: "n", CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetRoom(ctx, bare); got.Welcome != "" || len(got.QuickPrompts) != 0 {
			t.Fatalf("a bare room = %+v", got)
		}
	})
}
