package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func noteIn(room, id, typ string, at int64) RoomNote {
	return RoomNote{RoomID: room, NoteID: id, Type: typ, Title: "title " + id, Body: "body " + id, Author: "maya", CreatedAtMs: at}
}

// A note is created active at revision 1, edited with a revision per change,
// rolled back to an earlier revision's content as a new revision, and deleted
// with every revision; a stale editor is refused; tenants and rooms never see
// each other's notes.
func TestContractRoomNoteStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-note-"))
		room := newContractRoom(t, ctx, b, "maya")
		other := newContractRoom(t, ctx, b, "maya")

		n, err := b.RoomNotes.CreateRoomNote(ctx, RoomNote{RoomID: room, NoteID: uniq("n-"), Type: wire.NoteKnowledge, Title: "We deploy on Thursdays", Body: "Why: the freeze.",
			Author: "maya", CreatedAtMs: 10, Sources: []wire.NoteSource{{Kind: wire.NoteSourceMessage, Ref: "123"}}})
		if err != nil || n.Status != wire.NoteActive || n.Revision != 1 || n.UpdatedAtMs != 10 {
			t.Fatalf("create = %+v %v", n, err)
		}
		got, err := b.RoomNotes.GetRoomNote(ctx, room, n.NoteID)
		if err != nil || got.Title != n.Title || len(got.Sources) != 1 || got.Sources[0].Ref != "123" || got.VerifiedAtMs != 0 {
			t.Fatalf("get = %+v %v", got, err)
		}
		if _, err := b.RoomNotes.GetRoomNote(ctx, other, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("another room reads the note: %v", err)
		}
		if _, err := b.RoomNotes.GetRoomNote(tenantCtx(uniq("t-else-")), room, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("another tenant reads the note: %v", err)
		}

		title, body, pinned := "We deploy on Wednesdays", "Why: the freeze moved.", true
		up, err := b.RoomNotes.UpdateRoomNote(ctx, room, n.NoteID, RoomNotePatch{Title: &title, Body: &body, Pinned: &pinned, ExpectedRevision: 1}, "jack", 20)
		if err != nil || up.Revision != 2 || up.Title != title || !up.Pinned || up.UpdatedAtMs != 20 {
			t.Fatalf("update = %+v %v", up, err)
		}
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, n.NoteID, RoomNotePatch{Body: &body, ExpectedRevision: 1}, "maya", 30); !errors.Is(err, ErrRoomNoteConflict) {
			t.Fatalf("a stale editor: %v", err)
		}
		ver, err := b.RoomNotes.UpdateRoomNote(ctx, room, n.NoteID, RoomNotePatch{Verify: true}, "maya", 40)
		if err != nil || ver.Revision != 3 || ver.VerifiedAtMs != 40 {
			t.Fatalf("verify = %+v %v", ver, err)
		}

		revs, err := b.RoomNotes.ListRoomNoteRevisions(ctx, room, n.NoteID)
		if err != nil || len(revs) != 3 || revs[0].Revision != 3 || revs[2].Title != n.Title || revs[1].ChangedBy != "jack" {
			t.Fatalf("revisions = %+v %v", revs, err)
		}
		back, err := b.RoomNotes.RollbackRoomNote(ctx, room, n.NoteID, 1, "maya", 50)
		if err != nil || back.Revision != 4 || back.Title != n.Title || back.Pinned || back.Body != n.Body {
			t.Fatalf("rollback = %+v %v", back, err)
		}
		if _, err := b.RoomNotes.RollbackRoomNote(ctx, room, n.NoteID, 99, "maya", 60); !errors.Is(err, ErrRoomNoteRevision) {
			t.Fatalf("rollback to nothing: %v", err)
		}

		if err := b.RoomNotes.DeleteRoomNote(ctx, room, n.NoteID); err != nil {
			t.Fatal(err)
		}
		if _, err := b.RoomNotes.GetRoomNote(ctx, room, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("a deleted note is read: %v", err)
		}
		if _, err := b.RoomNotes.ListRoomNoteRevisions(ctx, room, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("a deleted note keeps revisions: %v", err)
		}
		if err := b.RoomNotes.DeleteRoomNote(ctx, room, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("a second delete: %v", err)
		}
	})
}

// A new note replaces an active note of its type (which becomes history and
// cannot change); a brief replaces the room's brief on its own and is never
// archived; archive and reactivate work for the rest; listings filter and order.
func TestContractRoomNoteSupersede(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-sup-"))
		room := newContractRoom(t, ctx, b, "maya")
		mk := func(typ string, at int64, supersedes string) (RoomNote, error) {
			n := noteIn(room, uniq("n-"), typ, at)
			n.Supersedes = supersedes
			return b.RoomNotes.CreateRoomNote(ctx, n)
		}

		old, _ := mk(wire.NoteKnowledge, 1, "")
		fresh, err := mk(wire.NoteKnowledge, 2, old.NoteID)
		if err != nil || fresh.Supersedes != old.NoteID {
			t.Fatalf("supersede = %+v %v", fresh, err)
		}
		gone, _ := b.RoomNotes.GetRoomNote(ctx, room, old.NoteID)
		if gone.Status != wire.NoteSuperseded || gone.Revision != 2 {
			t.Fatalf("the replaced note = %+v", gone)
		}
		title := "x"
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, old.NoteID, RoomNotePatch{Title: &title}, "maya", 3); !errors.Is(err, ErrRoomNoteSuperseded) {
			t.Fatalf("history changes: %v", err)
		}
		if _, err := b.RoomNotes.RollbackRoomNote(ctx, room, old.NoteID, 1, "maya", 3); !errors.Is(err, ErrRoomNoteSuperseded) {
			t.Fatalf("history rolls back: %v", err)
		}
		for name, tc := range map[string]struct{ typ, target string }{
			"already replaced": {wire.NoteKnowledge, old.NoteID},
			"another type":     {wire.NoteLesson, fresh.NoteID},
			"nothing":          {wire.NoteKnowledge, "missing"},
		} {
			if _, err := mk(tc.typ, 4, tc.target); !errors.Is(err, ErrRoomNoteSupersede) {
				t.Fatalf("%s: %v", name, err)
			}
		}

		brief1, _ := mk(wire.NoteBrief, 5, "")
		brief2, err := mk(wire.NoteBrief, 6, "")
		if err != nil || brief2.Supersedes != brief1.NoteID {
			t.Fatalf("a new brief = %+v %v", brief2, err)
		}
		if b1, _ := b.RoomNotes.GetRoomNote(ctx, room, brief1.NoteID); b1.Status != wire.NoteSuperseded {
			t.Fatalf("the old brief = %+v", b1)
		}
		archived := wire.NoteArchived
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, brief2.NoteID, RoomNotePatch{Status: &archived}, "maya", 7); !errors.Is(err, ErrRoomNoteBrief) {
			t.Fatalf("archive the brief: %v", err)
		}

		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, fresh.NoteID, RoomNotePatch{Status: &archived}, "maya", 8); err != nil {
			t.Fatal(err)
		}
		active, err := b.RoomNotes.ListRoomNotes(ctx, RoomNoteQuery{RoomID: room, Status: wire.NoteActive})
		if err != nil || len(active) != 1 || active[0].NoteID != brief2.NoteID {
			t.Fatalf("active = %+v %v", active, err)
		}
		activeStatus := wire.NoteActive
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, fresh.NoteID, RoomNotePatch{Status: &activeStatus}, "maya", 9); err != nil {
			t.Fatal(err)
		}
		all, _ := b.RoomNotes.ListRoomNotes(ctx, RoomNoteQuery{RoomID: room})
		if len(all) != 4 || all[0].NoteID != fresh.NoteID {
			t.Fatalf("all = %d, newest-updated first, got %+v", len(all), all)
		}
		if kn, _ := b.RoomNotes.ListRoomNotes(ctx, RoomNoteQuery{RoomID: room, Type: wire.NoteKnowledge}); len(kn) != 2 {
			t.Fatalf("knowledge notes = %d", len(kn))
		}
	})
}

// A room holds at most MaxActiveRoomNotes active notes (reactivating counts,
// replacing does not).
func TestContractRoomNoteLimits(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-lim-"))
		room := newContractRoom(t, ctx, b, "maya")
		var ids []string
		for i := 0; i < wire.MaxActiveRoomNotes; i++ {
			n, err := b.RoomNotes.CreateRoomNote(ctx, noteIn(room, uniq(fmt.Sprintf("n%d-", i)), wire.NoteMemory, int64(i+1)))
			if err != nil {
				t.Fatalf("note %d: %v", i, err)
			}
			ids = append(ids, n.NoteID)
		}
		if _, err := b.RoomNotes.CreateRoomNote(ctx, noteIn(room, uniq("over-"), wire.NoteMemory, 999)); !errors.Is(err, ErrRoomNoteLimit) {
			t.Fatalf("the 201st active note: %v", err)
		}
		// Replacing one is a net zero.
		over := noteIn(room, uniq("rep-"), wire.NoteMemory, 1000)
		over.Supersedes = ids[0]
		if _, err := b.RoomNotes.CreateRoomNote(ctx, over); err != nil {
			t.Fatalf("a replacement at the limit: %v", err)
		}
		// Archiving makes room, and putting it back is refused again at the limit.
		archived, active := wire.NoteArchived, wire.NoteActive
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, ids[1], RoomNotePatch{Status: &archived}, "maya", 1001); err != nil {
			t.Fatal(err)
		}
		if _, err := b.RoomNotes.CreateRoomNote(ctx, noteIn(room, uniq("fill-"), wire.NoteMemory, 1002)); err != nil {
			t.Fatalf("after archiving one: %v", err)
		}
		if _, err := b.RoomNotes.UpdateRoomNote(ctx, room, ids[1], RoomNotePatch{Status: &active}, "maya", 1003); !errors.Is(err, ErrRoomNoteLimit) {
			t.Fatalf("reactivating at the limit: %v", err)
		}
	})
}

// A deleted room takes its notes and their revisions with it.
func TestContractRoomNotesGoWithTheRoom(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-del-"))
		room := newContractRoom(t, ctx, b, "maya")
		n, err := b.RoomNotes.CreateRoomNote(ctx, noteIn(room, uniq("n-"), wire.NoteKnowledge, 1))
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Room.DeleteRoom(ctx, room); err != nil {
			t.Fatal(err)
		}
		if _, err := b.RoomNotes.GetRoomNote(ctx, room, n.NoteID); !errors.Is(err, ErrRoomNoteNotFound) {
			t.Fatalf("a deleted room keeps its notes: %v", err)
		}
	})
}
