package store

import (
	"errors"
	"reflect"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// RoomImportStore contract: a room's import provenance is written once (the
// key is the global room id), read back exactly, scoped to its tenant, and
// cleared with the room.
func TestContractRoomImportStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx, octx := tenantCtx(uniq("t-imp-")), tenantCtx(uniq("t-imp-other-"))
		s := b.RoomImports
		room := newContractRoom(t, ctx, b, "maya")
		if _, ok, err := s.GetRoomImport(ctx, room); err != nil || ok {
			t.Fatalf("absent provenance = %v %v", ok, err)
		}
		info := wire.RoomHandoffInfo{
			RoomID: room, SourceRoomID: "src", SourceRoomName: "Source", SourceExportedAtMs: 10,
			SourceExportedBy: "maya", ImportedBy: "jack", ImportedAtMs: 20,
			SuggestedMembers: []wire.RoomExportMember{{UserID: "maya", Role: "owner"}, {UserID: "bea", Role: "member"}},
		}
		if err := s.PutRoomImport(ctx, info); err != nil {
			t.Fatal(err)
		}
		if got, ok, err := s.GetRoomImport(ctx, room); err != nil || !ok || !reflect.DeepEqual(got, info) {
			t.Fatalf("provenance = %+v %v %v", got, ok, err)
		}
		if _, ok, _ := s.GetRoomImport(octx, room); ok {
			t.Fatal("provenance visible across tenants")
		}
		again := info
		again.ImportedBy = "mallory"
		if err := s.PutRoomImport(ctx, again); !errors.Is(err, ErrRoomImportExists) {
			t.Fatalf("second put = %v, want ErrRoomImportExists", err)
		}
		if err := s.PutRoomImport(octx, again); !errors.Is(err, ErrRoomImportExists) {
			t.Fatalf("put from another tenant = %v, want ErrRoomImportExists", err)
		}
		if got, _, _ := s.GetRoomImport(ctx, room); got.ImportedBy != "jack" {
			t.Fatalf("a refused put changed the provenance: %+v", got)
		}

		// No suggested members read back as [] on both backends.
		bare := newContractRoom(t, ctx, b, "maya")
		if err := s.PutRoomImport(ctx, wire.RoomHandoffInfo{RoomID: bare, SourceRoomID: "src", ImportedBy: "jack", ImportedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if got, ok, _ := s.GetRoomImport(ctx, bare); !ok || got.SuggestedMembers == nil || len(got.SuggestedMembers) != 0 {
			t.Fatalf("no suggested members = %#v, want []", got.SuggestedMembers)
		}

		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.GetRoomImport(ctx, room); ok {
			t.Fatal("DeleteRoom left the room's import provenance behind")
		}
	})
}
