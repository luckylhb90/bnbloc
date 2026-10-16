package wire

// Room notes (docs/prd/2026-10-16-room-memory-and-evolution.md): what a room,
// which is a project, knows: its brief, knowledge, memory and lessons. Members
// read and edit them through /v1/rooms/{roomId}/notes; an agent reads the
// ranked active ones through the gateway's /v1/rooms/{roomId}/memory?job=,
// which the bridge writes as .room/memory/.

// NoteType is what kind of note a note is.
//
//wire:union NoteType
const (
	// NoteBrief is the project in one page, always read. A room has at most one
	// active brief: a new one supersedes the old.
	NoteBrief = "brief"
	// NoteKnowledge is a decision (with its reason), a convention, a glossary
	// entry, a pointer to a deliverable.
	NoteKnowledge = "knowledge"
	// NoteMemory is a dated fact distilled from the conversation.
	NoteMemory = "memory"
	// NoteLesson is how this project does something.
	NoteLesson = "lesson"
)

// NoteTypes is every type, in the order a listing groups them.
var NoteTypes = []string{NoteBrief, NoteKnowledge, NoteMemory, NoteLesson}

// NoteStatus is where a note stands. Only active notes are read by agents.
//
//wire:union NoteStatus
const (
	NoteActive = "active"
	// NoteSuperseded: a newer note replaced it (kept for its history).
	NoteSuperseded = "superseded"
	// NoteArchived: put away by a member; it can be made active again.
	NoteArchived = "archived"
)

// NoteSourceKind is what a note's source points at.
//
//wire:union NoteSourceKind
const (
	NoteSourceMessage  = "message"
	NoteSourceArtifact = "artifact"
)

// Note limits: one place for the API, the stores and the web.
const (
	MaxNoteTitleRunes = 120
	MaxNoteBodyBytes  = 4 << 10
	// MaxBriefBodyBytes is the brief's larger bound (about 1.5K tokens of
	// text and a little over).
	MaxBriefBodyBytes = 8 << 10
	MaxNoteSources    = 8
	// MaxActiveRoomNotes bounds the notes a room keeps active (its brief
	// included): the active set stays small by design (supersede, archive).
	MaxActiveRoomNotes = 200
	// MaxRoomNotes bounds every note of a room, whatever its status.
	MaxRoomNotes = 1000
)

// NoteSource says where a note came from, so a reader can check it: a
// message of the room (Ref is its sequence number) or a deliverable (Ref is
// its id, Version one of its versions).
type NoteSource struct {
	Kind    string `json:"kind" tstype:"NoteSourceKind"`
	Ref     string `json:"ref"`
	Version int    `json:"version,omitempty"`
}

// RoomNote is one note of a room.
type RoomNote struct {
	NoteID string `json:"noteId"`
	Type   string `json:"type" tstype:"NoteType"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Status string `json:"status" tstype:"NoteStatus"`
	// Supersedes is the note this one replaced, when it did.
	Supersedes string       `json:"supersedes,omitempty"`
	Sources    []NoteSource `json:"sources,omitempty"`
	// Author is the member who wrote it (or, later, the curator).
	Author string `json:"author"`
	Pinned bool   `json:"pinned,omitempty"`
	// Revision counts the note's changes, from 1.
	Revision    int   `json:"revision"`
	CreatedAtMs int64 `json:"createdAtMs"`
	UpdatedAtMs int64 `json:"updatedAtMs"`
	// VerifiedAtMs is when a member last confirmed the note is still true.
	VerifiedAtMs int64 `json:"verifiedAtMs,omitempty"`
}

// NoteCreateRequest is POST /v1/rooms/{roomId}/notes. Supersedes names an
// active note of the room the new one replaces (it becomes superseded in the
// same step); a new brief supersedes the room's active brief on its own.
type NoteCreateRequest struct {
	Type       string       `json:"type" tstype:"NoteType"`
	Title      string       `json:"title"`
	Body       string       `json:"body"`
	Pinned     bool         `json:"pinned,omitempty"`
	Supersedes string       `json:"supersedes,omitempty"`
	Sources    []NoteSource `json:"sources,omitempty"`
}

// NoteUpdateRequest is PATCH /v1/rooms/{roomId}/notes/{noteId}: only the
// fields present change. Status moves a note between active and archived
// (superseded is made by a create). Verify records that the note was just
// confirmed. ExpectedRevision, when set, refuses the change (409) if the note
// moved on since the editor read it.
type NoteUpdateRequest struct {
	Title            *string `json:"title,omitempty"`
	Body             *string `json:"body,omitempty"`
	Pinned           *bool   `json:"pinned,omitempty"`
	Status           *string `json:"status,omitempty" tstype:"NoteStatus"`
	Verify           bool    `json:"verify,omitempty"`
	ExpectedRevision int     `json:"expectedRevision,omitempty"`
}

// NoteRollbackRequest is POST /v1/rooms/{roomId}/notes/{noteId}/rollback: the
// note takes the content of an earlier revision, as a new revision.
type NoteRollbackRequest struct {
	Revision int `json:"revision"`
}

// NotesReply lists a room's notes, newest-updated first (the brief and the
// pinned ones first).
type NotesReply struct {
	Notes []RoomNote `json:"notes"`
}

// NoteRevision is one stored state of a note, for history and rollback.
type NoteRevision struct {
	Revision  int    `json:"revision"`
	Type      string `json:"type" tstype:"NoteType"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status" tstype:"NoteStatus"`
	Pinned    bool   `json:"pinned,omitempty"`
	ChangedBy string `json:"changedBy"`
	AtMs      int64  `json:"atMs"`
}

// NoteRevisionsReply lists a note's revisions, newest first.
type NoteRevisionsReply struct {
	Revisions []NoteRevision `json:"revisions"`
}

// RoomMemory is what an agent is handed of a room's notes: the active brief
// (nil when the room has none) and the other active notes in the order the
// agent's index lists them (internal/notes.Rank). It is the answer of GET
// /v1/rooms/{roomId}/memory?job=, read by the bridge for .room/memory/.
type RoomMemory struct {
	Brief *RoomNote  `json:"brief,omitempty"`
	Notes []RoomNote `json:"notes"`
}
