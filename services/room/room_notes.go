package room

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notes"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room notes (docs/prd/2026-10-16-room-memory-and-evolution.md): what a
// room, a project, knows. The room service owns them: they are the room's,
// written by its members, deleted with it.
//
//	GET    /v1/rooms/{roomId}/notes                    members: the notes (?status=, ?type=)
//	POST   /v1/rooms/{roomId}/notes                    members who may act: a new note
//	GET    /v1/rooms/{roomId}/notes/{noteId}           members
//	PATCH  /v1/rooms/{roomId}/notes/{noteId}           members who may act: edit, pin, archive, confirm
//	DELETE /v1/rooms/{roomId}/notes/{noteId}           members who may act: remove the note and its history
//	GET    /v1/rooms/{roomId}/notes/{noteId}/revisions members
//	POST   /v1/rooms/{roomId}/notes/{noteId}/rollback  members who may act: take an earlier revision's content
//	GET    /internal/v1/rooms/{roomId}/memory?as=      the gateway: what an agent is handed (as = the job's requester)
//
// What a note may hold is internal/notes' rule (Check); the rules among notes
// (the brief, superseding, the caps) are the store's.

func (s *Service) registerRoomNoteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rooms/{roomId}/notes", s.handleListRoomNotes)
	mux.HandleFunc("POST /v1/rooms/{roomId}/notes", s.handleCreateRoomNote)
	mux.HandleFunc("GET /v1/rooms/{roomId}/notes/{noteId}", s.handleGetRoomNote)
	mux.HandleFunc("PATCH /v1/rooms/{roomId}/notes/{noteId}", s.handleUpdateRoomNote)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/notes/{noteId}", s.handleDeleteRoomNote)
	mux.HandleFunc("GET /v1/rooms/{roomId}/notes/{noteId}/revisions", s.handleRoomNoteRevisions)
	mux.HandleFunc("POST /v1/rooms/{roomId}/notes/{noteId}/rollback", s.handleRollbackRoomNote)
	mux.Handle("GET /internal/v1/rooms/{roomId}/memory", svcauth.RequireService(svcauth.KeyFromEnv(), []string{"gateway"}, http.HandlerFunc(s.handleRoomMemory)))
}

// viewNote is a stored note as the API shows it.
func viewNote(n store.RoomNote) wire.RoomNote {
	return wire.RoomNote{
		NoteID: n.NoteID, Type: n.Type, Title: n.Title, Body: n.Body, Status: n.Status, Supersedes: n.Supersedes, Sources: n.Sources,
		Author: n.Author, Pinned: n.Pinned, Revision: n.Revision, CreatedAtMs: n.CreatedAtMs, UpdatedAtMs: n.UpdatedAtMs, VerifiedAtMs: n.VerifiedAtMs,
	}
}

func viewNotes(list []store.RoomNote) []wire.RoomNote {
	out := make([]wire.RoomNote, 0, len(list))
	for _, n := range list {
		out = append(out, viewNote(n))
	}
	return out
}

// noteMember checks user is a member of the room (mayAct: one who may act in
// it, not a visitor) and that the notes are available. Agent hosts are not
// people of the room: they read through a job's requester (handleRoomMemory).
func (s *Service) noteMember(w http.ResponseWriter, r *http.Request, user string, mayAct bool) (string, bool) {
	if s.RoomNotes == nil {
		httpx.Unavailable(w, "room notes")
		return "", false
	}
	roomID := r.PathValue("roomId")
	m, ok, err := s.Store.GetMember(r.Context(), roomID, user)
	switch {
	case err != nil:
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return "", false
	case !ok || m.AgentHost:
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return "", false
	case mayAct && !(membership.Member{Role: m.Role}).MayChat():
		httpx.Error(w, "visitors cannot change the room's notes", http.StatusForbidden)
		return "", false
	}
	return roomID, true
}

// writeNoteErr answers a store refusal in the words a member can act on; any
// other error is a 500.
func writeNoteErr(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, store.ErrRoomNoteNotFound):
		httpx.Error(w, "note not found", http.StatusNotFound)
	case errors.Is(err, store.ErrRoomNoteRevision):
		httpx.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, store.ErrRoomNoteConflict):
		httpx.Error(w, "the note changed since you opened it: reload it and edit again", http.StatusConflict)
	case errors.Is(err, store.ErrRoomNoteLimit):
		httpx.Error(w, "the room keeps as many notes as it may: archive or delete some first", http.StatusConflict)
	case errors.Is(err, store.ErrRoomNoteSuperseded):
		httpx.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrRoomNoteSupersede), errors.Is(err, store.ErrRoomNoteBrief):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	default:
		obs.ServerError(w, what+" failed", err)
		httpx.Error(w, what+" failed", http.StatusInternalServerError)
	}
}

func (s *Service) handleListRoomNotes(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.noteMember(w, r, caller(r), false)
	if !ok {
		return
	}
	q := store.RoomNoteQuery{RoomID: roomID, Status: r.URL.Query().Get("status"), Type: r.URL.Query().Get("type")}
	if (q.Status != "" && !notes.ValidStatus(q.Status)) || (q.Type != "" && !notes.ValidType(q.Type)) {
		httpx.Error(w, "unknown status or type", http.StatusBadRequest)
		return
	}
	list, err := s.RoomNotes.ListRoomNotes(r.Context(), q)
	if err != nil {
		writeNoteErr(w, "list room notes", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.NotesReply{Notes: viewNotes(list)})
}

func (s *Service) handleCreateRoomNote(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	roomID, ok := s.noteMember(w, r, user, true)
	if !ok {
		return
	}
	var in wire.NoteCreateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if err := notes.Check(in.Type, in.Title, in.Body, in.Sources); err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n, err := s.RoomNotes.CreateRoomNote(r.Context(), store.RoomNote{
		RoomID: roomID, NoteID: ids.New(""), Type: in.Type, Title: in.Title, Body: in.Body, Supersedes: strings.TrimSpace(in.Supersedes),
		Sources: in.Sources, Author: user, Pinned: in.Pinned, CreatedAtMs: s.Now(),
	})
	if err != nil {
		writeNoteErr(w, "create room note", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, viewNote(n))
}

func (s *Service) handleGetRoomNote(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.noteMember(w, r, caller(r), false)
	if !ok {
		return
	}
	n, err := s.RoomNotes.GetRoomNote(r.Context(), roomID, r.PathValue("noteId"))
	if err != nil {
		writeNoteErr(w, "read room note", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewNote(n))
}

func (s *Service) handleUpdateRoomNote(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	roomID, ok := s.noteMember(w, r, user, true)
	if !ok {
		return
	}
	var in wire.NoteUpdateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &in) {
		return
	}
	cur, err := s.RoomNotes.GetRoomNote(r.Context(), roomID, r.PathValue("noteId"))
	if err != nil {
		writeNoteErr(w, "read room note", err)
		return
	}
	patch := store.RoomNotePatch{Title: in.Title, Body: in.Body, Pinned: in.Pinned, Status: in.Status, Verify: in.Verify, ExpectedRevision: in.ExpectedRevision}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		patch.Title = &t
		if err := notes.CheckTitle(t); err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if in.Body != nil {
		if err := notes.CheckBody(cur.Type, *in.Body); err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if in.Status != nil && *in.Status != wire.NoteActive && *in.Status != wire.NoteArchived {
		httpx.Error(w, `status is "active" or "archived"`, http.StatusBadRequest)
		return
	}
	if in.Title == nil && in.Body == nil && in.Pinned == nil && in.Status == nil && !in.Verify {
		httpx.Error(w, "nothing to change", http.StatusBadRequest)
		return
	}
	n, err := s.RoomNotes.UpdateRoomNote(r.Context(), roomID, cur.NoteID, patch, user, s.Now())
	if err != nil {
		writeNoteErr(w, "update room note", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewNote(n))
}

func (s *Service) handleDeleteRoomNote(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.noteMember(w, r, caller(r), true)
	if !ok {
		return
	}
	if err := s.RoomNotes.DeleteRoomNote(r.Context(), roomID, r.PathValue("noteId")); err != nil {
		writeNoteErr(w, "delete room note", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRoomNoteRevisions(w http.ResponseWriter, r *http.Request) {
	roomID, ok := s.noteMember(w, r, caller(r), false)
	if !ok {
		return
	}
	list, err := s.RoomNotes.ListRoomNoteRevisions(r.Context(), roomID, r.PathValue("noteId"))
	if err != nil {
		writeNoteErr(w, "list note revisions", err)
		return
	}
	out := wire.NoteRevisionsReply{Revisions: make([]wire.NoteRevision, 0, len(list))}
	for _, v := range list {
		out.Revisions = append(out.Revisions, wire.NoteRevision{Revision: v.Revision, Type: v.Type, Title: v.Title, Body: v.Body, Status: v.Status, Pinned: v.Pinned, ChangedBy: v.ChangedBy, AtMs: v.AtMs})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) handleRollbackRoomNote(w http.ResponseWriter, r *http.Request) {
	user := caller(r)
	roomID, ok := s.noteMember(w, r, user, true)
	if !ok {
		return
	}
	var in wire.NoteRollbackRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &in) {
		return
	}
	n, err := s.RoomNotes.RollbackRoomNote(r.Context(), roomID, r.PathValue("noteId"), in.Revision, user, s.Now())
	if err != nil {
		writeNoteErr(w, "roll back room note", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewNote(n))
}

// handleRoomMemory is what an agent is handed of the room's notes, read for
// the requester of its job (as, named by the gateway, the only caller): who
// must be a member of the room. The answer is the active brief and the other
// active notes in internal/notes.Rank's order; the bridge renders and caps
// them. A visitor reads too: reading is not acting.
func (s *Service) handleRoomMemory(w http.ResponseWriter, r *http.Request) {
	if _, err := tenant.Require(r.Context()); err != nil {
		httpx.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	roomID, ok := s.noteMember(w, r, strings.TrimSpace(r.URL.Query().Get("as")), false)
	if !ok {
		return
	}
	list, err := s.RoomNotes.ListRoomNotes(r.Context(), store.RoomNoteQuery{RoomID: roomID, Status: wire.NoteActive})
	if err != nil {
		writeNoteErr(w, "read room memory", err)
		return
	}
	all := viewNotes(list)
	httpx.WriteJSON(w, http.StatusOK, wire.RoomMemory{Brief: notes.Brief(all), Notes: notes.Rank(all)})
}
