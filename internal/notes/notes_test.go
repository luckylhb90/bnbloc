package notes

import (
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestCheck(t *testing.T) {
	ok := func(typ, title, body string) {
		t.Helper()
		if err := Check(typ, title, body, nil); err != nil {
			t.Errorf("Check(%q, %q) = %v, want ok", typ, title, err)
		}
	}
	bad := func(want, typ, title, body string, sources ...wire.NoteSource) {
		t.Helper()
		if err := Check(typ, title, body, sources); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q, %q) = %v, want %q", typ, title, err, want)
		}
	}
	ok(wire.NoteKnowledge, "We deploy on Thursdays", "")
	ok(wire.NoteBrief, "Project", strings.Repeat("x", wire.MaxBriefBodyBytes))
	bad("type is one of", "idea", "t", "")
	bad("needs a title", wire.NoteMemory, "   ", "")
	bad("one line", wire.NoteMemory, "two\nlines", "")
	bad("at most 120", wire.NoteMemory, strings.Repeat("é", wire.MaxNoteTitleRunes+1), "")
	bad("at most 4096 bytes", wire.NoteLesson, "t", strings.Repeat("x", wire.MaxNoteBodyBytes+1))
	bad("is text", wire.NoteLesson, "t", "\xff\xfe")
	bad("at most 8 sources", wire.NoteMemory, "t", "", make([]wire.NoteSource, wire.MaxNoteSources+1)...)
	bad("a source is", wire.NoteMemory, "t", "", wire.NoteSource{Kind: "web", Ref: "x"})
	bad("a source is", wire.NoteMemory, "t", "", wire.NoteSource{Kind: wire.NoteSourceMessage})
}

// The agent's index lists pinned notes first, then decisions and lessons
// before dated memory, each newest first; superseded, archived notes and the
// brief are not in it, and the order is the same on every call.
func TestRank(t *testing.T) {
	n := func(id, typ, status string, pinned bool, updated, verified int64) wire.RoomNote {
		return wire.RoomNote{NoteID: id, Type: typ, Status: status, Pinned: pinned, UpdatedAtMs: updated, VerifiedAtMs: verified}
	}
	list := []wire.RoomNote{
		n("m-new", wire.NoteMemory, wire.NoteActive, false, 900, 0),
		n("k-old", wire.NoteKnowledge, wire.NoteActive, false, 100, 0),
		n("k-verified", wire.NoteKnowledge, wire.NoteActive, false, 50, 700),
		n("l-new", wire.NoteLesson, wire.NoteActive, false, 800, 0),
		n("m-pinned", wire.NoteMemory, wire.NoteActive, true, 1, 0),
		n("old", wire.NoteKnowledge, wire.NoteSuperseded, false, 999, 0),
		n("away", wire.NoteLesson, wire.NoteArchived, false, 999, 0),
		n("brief", wire.NoteBrief, wire.NoteActive, true, 999, 0),
		n("a-tie", wire.NoteMemory, wire.NoteActive, false, 900, 0),
	}
	var got []string
	for _, x := range Rank(list) {
		got = append(got, x.NoteID)
	}
	want := "m-pinned,l-new,k-verified,k-old,a-tie,m-new"
	if strings.Join(got, ",") != want {
		t.Fatalf("Rank = %v, want %s", got, want)
	}
	if b := Brief(list); b == nil || b.NoteID != "brief" {
		t.Fatalf("Brief = %+v", b)
	}
	if Brief(list[:3]) != nil {
		t.Fatal("a room without a brief has one")
	}
}
