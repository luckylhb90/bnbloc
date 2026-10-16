package room

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func (r reply) note(t *testing.T) wire.RoomNote {
	t.Helper()
	var n wire.RoomNote
	if json.Unmarshal(r.body, &n) != nil {
		t.Fatalf("not a note: %d %s", r.status, r.body)
	}
	return n
}

func (e *appEnv) notes(user, query string) []wire.RoomNote {
	e.t.Helper()
	r := e.roomDo(user, http.MethodGet, "/v1/rooms/"+e.room+"/notes"+query, nil)
	var out wire.NotesReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil {
		e.t.Fatalf("list notes as %s = %d %s", user, r.status, r.body)
	}
	return out.Notes
}

// Members who may act write notes (checked by internal/notes); a visitor and an
// outsider cannot; every member, a visitor too, reads. The brief is one: a new
// one replaces it.
func TestRoomNotesAccessAndValidation(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.RoomNotes = b.RoomNotes
		base := "/v1/rooms/" + e.room + "/notes"

		r := e.roomDo("jack", http.MethodPost, base, wire.NoteCreateRequest{Type: wire.NoteKnowledge, Title: "  We deploy on Thursdays ", Body: "Because the freeze."})
		n := r.note(t)
		if r.status != http.StatusCreated || n.Title != "We deploy on Thursdays" || n.Author != "jack" || n.Status != wire.NoteActive || n.Revision != 1 || n.NoteID == "" {
			t.Fatalf("create = %d %+v", r.status, n)
		}
		for who, want := range map[string]int{"vic": http.StatusForbidden, "zed": http.StatusForbidden} {
			if r := e.roomDo(who, http.MethodPost, base, wire.NoteCreateRequest{Type: wire.NoteMemory, Title: "x"}); r.status != want {
				t.Fatalf("%s writes = %d, want %d", who, r.status, want)
			}
		}
		if r := e.roomDo("zed", http.MethodGet, base, nil); r.status != http.StatusForbidden {
			t.Fatalf("an outsider reads = %d", r.status)
		}
		if got := e.notes("vic", ""); len(got) != 1 || got[0].NoteID != n.NoteID {
			t.Fatalf("a visitor reads %+v", got)
		}
		for name, in := range map[string]wire.NoteCreateRequest{
			"no title":      {Type: wire.NoteMemory},
			"unknown type":  {Type: "idea", Title: "x"},
			"too long":      {Type: wire.NoteLesson, Title: "x", Body: strings.Repeat("x", wire.MaxNoteBodyBytes+1)},
			"bad source":    {Type: wire.NoteMemory, Title: "x", Sources: []wire.NoteSource{{Kind: "web", Ref: "u"}}},
			"replace other": {Type: wire.NoteLesson, Title: "x", Supersedes: n.NoteID},
		} {
			if r := e.roomDo("jack", http.MethodPost, base, in); r.status != http.StatusBadRequest {
				t.Fatalf("%s = %d %s", name, r.status, r.body)
			}
		}

		b1 := e.roomDo("jack", http.MethodPost, base, wire.NoteCreateRequest{Type: wire.NoteBrief, Title: "Project", Body: "v1"}).note(t)
		b2 := e.roomDo("maya", http.MethodPost, base, wire.NoteCreateRequest{Type: wire.NoteBrief, Title: "Project", Body: "v2"}).note(t)
		if b2.Supersedes != b1.NoteID {
			t.Fatalf("the new brief = %+v", b2)
		}
		if r := e.roomDo("jack", http.MethodGet, base+"/"+b1.NoteID, nil); r.note(t).Status != wire.NoteSuperseded {
			t.Fatalf("the old brief = %s", r.body)
		}
		if r := e.roomDo("jack", http.MethodGet, base+"?status=bogus", nil); r.status != http.StatusBadRequest {
			t.Fatalf("a bad filter = %d", r.status)
		}
		if got := e.notes("jack", "?type=brief&status=active"); len(got) != 1 || got[0].NoteID != b2.NoteID {
			t.Fatalf("the active brief = %+v", got)
		}
	})
}

// A note is edited (a stale editor is refused with 409), pinned, archived,
// confirmed and rolled back by those who may act, every change a revision;
// deleting removes the note and its history.
func TestRoomNotesEditHistoryAndDelete(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.RoomNotes = b.RoomNotes
		base := "/v1/rooms/" + e.room + "/notes"
		n := e.roomDo("jack", http.MethodPost, base, wire.NoteCreateRequest{Type: wire.NoteLesson, Title: "Weekly report", Body: "Use the app."}).note(t)
		path := base + "/" + n.NoteID

		body := "Use the app, format B."
		r := e.roomDo("maya", http.MethodPatch, path, wire.NoteUpdateRequest{Body: &body, ExpectedRevision: 1})
		if got := r.note(t); r.status != http.StatusOK || got.Revision != 2 || got.Body != body {
			t.Fatalf("edit = %d %s", r.status, r.body)
		}
		if r := e.roomDo("jack", http.MethodPatch, path, wire.NoteUpdateRequest{Body: &body, ExpectedRevision: 1}); r.status != http.StatusConflict {
			t.Fatalf("a stale editor = %d", r.status)
		}
		pin, archived := true, wire.NoteArchived
		e.roomDo("jack", http.MethodPatch, path, wire.NoteUpdateRequest{Pinned: &pin, Verify: true})
		if r := e.roomDo("jack", http.MethodPatch, path, wire.NoteUpdateRequest{Status: &archived}); r.note(t).Status != wire.NoteArchived {
			t.Fatalf("archive = %s", r.body)
		}
		if r := e.roomDo("jack", http.MethodPatch, path, wire.NoteUpdateRequest{}); r.status != http.StatusBadRequest {
			t.Fatalf("an empty edit = %d", r.status)
		}
		bad := wire.NoteSuperseded
		if r := e.roomDo("jack", http.MethodPatch, path, wire.NoteUpdateRequest{Status: &bad}); r.status != http.StatusBadRequest {
			t.Fatalf("status superseded by hand = %d", r.status)
		}
		if r := e.roomDo("vic", http.MethodPatch, path, wire.NoteUpdateRequest{Pinned: &pin}); r.status != http.StatusForbidden {
			t.Fatalf("a visitor edits = %d", r.status)
		}

		var revs wire.NoteRevisionsReply
		if r := e.roomDo("vic", http.MethodGet, path+"/revisions", nil); json.Unmarshal(r.body, &revs) != nil || len(revs.Revisions) != 4 || revs.Revisions[0].Revision != 4 || revs.Revisions[3].Body != "Use the app." {
			t.Fatalf("revisions = %d %s", r.status, r.body)
		}
		back := e.roomDo("maya", http.MethodPost, path+"/rollback", wire.NoteRollbackRequest{Revision: 1}).note(t)
		if back.Body != "Use the app." || back.Revision != 5 || back.Pinned {
			t.Fatalf("rollback = %+v", back)
		}
		if r := e.roomDo("maya", http.MethodPost, path+"/rollback", wire.NoteRollbackRequest{Revision: 99}); r.status != http.StatusNotFound {
			t.Fatalf("rollback to nothing = %d", r.status)
		}

		if r := e.roomDo("vic", http.MethodDelete, path, nil); r.status != http.StatusForbidden {
			t.Fatalf("a visitor deletes = %d", r.status)
		}
		if r := e.roomDo("jack", http.MethodDelete, path, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete = %d", r.status)
		}
		for _, p := range []string{path, path + "/revisions"} {
			if r := e.roomDo("jack", http.MethodGet, p, nil); r.status != http.StatusNotFound {
				t.Fatalf("GET %s after delete = %d", p, r.status)
			}
		}
	})
}

// An agent's read of the room's memory is the requester's: the brief and the
// ranked active notes (pinned first, decisions before dated memory), never a
// superseded or archived one, and only for a member; without the gateway's
// signature the route is refused.
func TestRoomMemoryForAnAgent(t *testing.T) {
	forAppBackends(t, func(t *testing.T, b *store.Backends) {
		e := newAppEnv(t, b)
		e.svc.RoomNotes = b.RoomNotes
		base := "/v1/rooms/" + e.room + "/notes"
		mk := func(typ, title string, pinned bool) wire.RoomNote {
			return e.roomDo("jack", http.MethodPost, base, wire.NoteCreateRequest{Type: typ, Title: title, Pinned: pinned}).note(t)
		}
		mk(wire.NoteBrief, "Project", false)
		mk(wire.NoteMemory, "dated", false)
		mk(wire.NoteKnowledge, "decision", false)
		mk(wire.NoteMemory, "pinned memory", true)
		gone := mk(wire.NoteLesson, "away", false)
		archived := wire.NoteArchived
		e.roomDo("jack", http.MethodPatch, base+"/"+gone.NoteID, wire.NoteUpdateRequest{Status: &archived})

		internal := "/internal/v1/rooms/" + e.room + "/memory?as="
		read := func(as string) (int, wire.RoomMemory) {
			r := e.roomDo(svcauth.ServiceSubject("gateway"), http.MethodGet, internal+as, nil)
			var m wire.RoomMemory
			_ = json.Unmarshal(r.body, &m)
			return r.status, m
		}
		code, mem := read("vic")
		var titles []string
		for _, n := range mem.Notes {
			titles = append(titles, n.Title)
		}
		if code != http.StatusOK || mem.Brief == nil || mem.Brief.Title != "Project" || strings.Join(titles, ",") != "pinned memory,decision,dated" {
			t.Fatalf("memory = %d brief %+v notes %v", code, mem.Brief, titles)
		}
		if code, _ := read("zed"); code != http.StatusForbidden {
			t.Fatalf("an outsider's memory = %d", code)
		}
		if code, _ := read(""); code != http.StatusForbidden {
			t.Fatalf("no requester = %d", code)
		}
		if r := e.roomDo("jack", http.MethodGet, internal+"jack", nil); r.status == http.StatusOK {
			t.Fatalf("a member reached the gateway-only route")
		}
	})
}
