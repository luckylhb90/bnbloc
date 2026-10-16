package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room notes (docs/prd/2026-10-16-room-memory-and-evolution.md): what a room,
// a project, knows. A note belongs to its room and is deleted with it. Every
// change appends a revision (who, when, the state it left), so a note can be
// rolled back; deleting a note deletes its revisions too (a deleted note
// leaves no copy of its text). What a note may hold is internal/notes' rule
// (the room service checks it); this file owns the rules about notes among
// themselves (the brief is one, a superseded note is history, the caps), written
// once below and used by both backends.

// RoomNote is one note of a room.
type RoomNote struct {
	TenantID     string
	RoomID       string
	NoteID       string
	Type         string
	Title        string
	Body         string
	Status       string
	Supersedes   string
	Sources      []wire.NoteSource
	Author       string
	Pinned       bool
	Revision     int
	CreatedAtMs  int64
	UpdatedAtMs  int64
	VerifiedAtMs int64
}

// RoomNoteRevision is one stored state of a note.
type RoomNoteRevision struct {
	NoteID    string
	Revision  int
	Type      string
	Title     string
	Body      string
	Status    string
	Pinned    bool
	ChangedBy string
	AtMs      int64
}

// RoomNotePatch is an edit: only the fields set change. Status is "active"
// or "archived" (superseded is made by CreateRoomNote). ExpectedRevision,
// when above 0, must be the note's current revision.
type RoomNotePatch struct {
	Title            *string
	Body             *string
	Pinned           *bool
	Status           *string
	Verify           bool
	ExpectedRevision int
}

// RoomNoteQuery filters a room's notes: a status and a type ("" = any).
type RoomNoteQuery struct {
	RoomID string
	Status string
	Type   string
}

// Room note errors.
var (
	ErrRoomNoteNotFound = errors.New("room note not found")
	// ErrRoomNoteConflict: the note changed since the editor read it.
	ErrRoomNoteConflict = errors.New("the note changed since it was read")
	// ErrRoomNoteLimit: the room holds as many notes (or active notes) as it may.
	ErrRoomNoteLimit = errors.New("the room holds as many notes as it may")
	// ErrRoomNoteSupersede: the note to replace is missing, not active, or not
	// of the new note's type.
	ErrRoomNoteSupersede = errors.New("the note to replace must be an active note of the same type")
	// ErrRoomNoteSuperseded: a superseded note is history and cannot change.
	ErrRoomNoteSuperseded = errors.New("a replaced note is history and cannot change")
	// ErrRoomNoteBrief: the brief is edited or replaced, never archived.
	ErrRoomNoteBrief = errors.New("the brief is edited or replaced, not archived")
	// ErrRoomNoteRevision: the revision to roll back to does not exist.
	ErrRoomNoteRevision = errors.New("that revision does not exist")
)

// RoomNoteStore holds rooms' notes. The tenant is the context's.
type RoomNoteStore interface {
	// CreateRoomNote stores n as a new active note at revision 1 (the caller
	// sets NoteID, Type, Title, Body, Author and CreatedAtMs). When n.Supersedes
	// names an active note of the same type, or n is a brief and the room has
	// an active brief, that note becomes superseded in the same step.
	CreateRoomNote(ctx context.Context, n RoomNote) (RoomNote, error)
	GetRoomNote(ctx context.Context, roomID, noteID string) (RoomNote, error)
	// ListRoomNotes returns the room's notes matching q, newest-updated first.
	ListRoomNotes(ctx context.Context, q RoomNoteQuery) ([]RoomNote, error)
	// UpdateRoomNote applies p and records a revision at atMs by by.
	UpdateRoomNote(ctx context.Context, roomID, noteID string, p RoomNotePatch, by string, atMs int64) (RoomNote, error)
	// RollbackRoomNote gives the note the title, body and pin of an earlier
	// revision, as a new revision.
	RollbackRoomNote(ctx context.Context, roomID, noteID string, revision int, by string, atMs int64) (RoomNote, error)
	// DeleteRoomNote removes the note and every revision of it.
	DeleteRoomNote(ctx context.Context, roomID, noteID string) error
	// ListRoomNoteRevisions returns the note's revisions, newest first.
	ListRoomNoteRevisions(ctx context.Context, roomID, noteID string) ([]RoomNoteRevision, error)
}

var (
	_ RoomNoteStore = (*MemoryRoomNotes)(nil)
	_ RoomNoteStore = (*MySQLRoomNotes)(nil)
)

// --- the rules among notes, used by both backends ---

// revisionOf is the revision row for n's current state.
func revisionOf(n RoomNote, by string, atMs int64) RoomNoteRevision {
	return RoomNoteRevision{NoteID: n.NoteID, Revision: n.Revision, Type: n.Type, Title: n.Title, Body: n.Body, Status: n.Status, Pinned: n.Pinned, ChangedBy: by, AtMs: atMs}
}

func countRoomNotes(existing []RoomNote) (total, active int) {
	for _, e := range existing {
		total++
		if e.Status == wire.NoteActive {
			active++
		}
	}
	return total, active
}

// planCreateNote decides a create against the room's notes (existing): the
// note it supersedes ("" for none) or an error, and the note to store. A
// brief replaces the room's active brief on its own.
func planCreateNote(existing []RoomNote, n RoomNote) (stored RoomNote, supersede *RoomNote, err error) {
	total, active := countRoomNotes(existing)
	if total >= wire.MaxRoomNotes {
		return RoomNote{}, nil, ErrRoomNoteLimit
	}
	target := n.Supersedes
	if target == "" && n.Type == wire.NoteBrief {
		for _, e := range existing {
			if e.Type == wire.NoteBrief && e.Status == wire.NoteActive {
				target = e.NoteID
			}
		}
	}
	if target != "" {
		for i := range existing {
			if existing[i].NoteID == target {
				supersede = &existing[i]
			}
		}
		if supersede == nil || supersede.Status != wire.NoteActive || supersede.Type != n.Type || supersede.NoteID == n.NoteID {
			return RoomNote{}, nil, ErrRoomNoteSupersede
		}
		active--
	}
	if active >= wire.MaxActiveRoomNotes {
		return RoomNote{}, nil, ErrRoomNoteLimit
	}
	n.Status, n.Revision, n.Supersedes = wire.NoteActive, 1, target
	n.UpdatedAtMs = n.CreatedAtMs
	return n, supersede, nil
}

// supersededBy marks old as replaced by a new note, at atMs.
func supersededBy(old RoomNote, by string, atMs int64) (RoomNote, RoomNoteRevision) {
	old.Status, old.UpdatedAtMs = wire.NoteSuperseded, atMs
	old.Revision++
	return old, revisionOf(old, by, atMs)
}

// planUpdateNote applies p to cur, with active the room's current count of
// active notes: the new state and its revision, or an error.
func planUpdateNote(cur RoomNote, p RoomNotePatch, active int, by string, atMs int64) (RoomNote, RoomNoteRevision, error) {
	if cur.Status == wire.NoteSuperseded {
		return RoomNote{}, RoomNoteRevision{}, ErrRoomNoteSuperseded
	}
	if p.ExpectedRevision > 0 && p.ExpectedRevision != cur.Revision {
		return RoomNote{}, RoomNoteRevision{}, ErrRoomNoteConflict
	}
	if p.Title != nil {
		cur.Title = *p.Title
	}
	if p.Body != nil {
		cur.Body = *p.Body
	}
	if p.Pinned != nil {
		cur.Pinned = *p.Pinned
	}
	if p.Status != nil && *p.Status != cur.Status {
		switch {
		case *p.Status == wire.NoteArchived && cur.Type == wire.NoteBrief:
			return RoomNote{}, RoomNoteRevision{}, ErrRoomNoteBrief
		case *p.Status == wire.NoteActive && active >= wire.MaxActiveRoomNotes:
			return RoomNote{}, RoomNoteRevision{}, ErrRoomNoteLimit
		}
		cur.Status = *p.Status
	}
	if p.Verify {
		cur.VerifiedAtMs = atMs
	}
	cur.UpdatedAtMs = atMs
	cur.Revision++
	return cur, revisionOf(cur, by, atMs), nil
}

// planRollbackNote gives cur the content of rev, as a new revision.
func planRollbackNote(cur RoomNote, rev RoomNoteRevision, by string, atMs int64) (RoomNote, RoomNoteRevision, error) {
	if cur.Status == wire.NoteSuperseded {
		return RoomNote{}, RoomNoteRevision{}, ErrRoomNoteSuperseded
	}
	cur.Title, cur.Body, cur.Pinned = rev.Title, rev.Body, rev.Pinned
	cur.UpdatedAtMs = atMs
	cur.Revision++
	return cur, revisionOf(cur, by, atMs), nil
}

func matchNote(n RoomNote, q RoomNoteQuery) bool {
	return (q.Status == "" || n.Status == q.Status) && (q.Type == "" || n.Type == q.Type)
}

func sortNotes(list []RoomNote) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].UpdatedAtMs != list[j].UpdatedAtMs {
			return list[i].UpdatedAtMs > list[j].UpdatedAtMs
		}
		return list[i].NoteID > list[j].NoteID
	})
}

// --- memory ---

// MemoryRoomNotes is the in-memory RoomNoteStore.
type MemoryRoomNotes struct {
	mu    sync.Mutex
	notes map[[3]string]RoomNote // tenant, room, note
	revs  map[[3]string][]RoomNoteRevision
}

// NewMemoryRoomNotes builds an empty store.
func NewMemoryRoomNotes() *MemoryRoomNotes {
	return &MemoryRoomNotes{notes: map[[3]string]RoomNote{}, revs: map[[3]string][]RoomNoteRevision{}}
}

// roomLocked is the room's notes; the caller holds mu.
func (m *MemoryRoomNotes) roomLocked(tid, roomID string) []RoomNote {
	var out []RoomNote
	for k, n := range m.notes {
		if k[0] == tid && k[1] == roomID {
			out = append(out, n)
		}
	}
	return out
}

func cloneNote(n RoomNote) RoomNote {
	n.Sources = append([]wire.NoteSource(nil), n.Sources...)
	return n
}

func (m *MemoryRoomNotes) CreateRoomNote(ctx context.Context, n RoomNote) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n.TenantID = tid
	stored, old, err := planCreateNote(m.roomLocked(tid, n.RoomID), n)
	if err != nil {
		return RoomNote{}, err
	}
	if old != nil {
		replaced, rev := supersededBy(*old, stored.Author, stored.UpdatedAtMs)
		m.notes[[3]string{tid, n.RoomID, old.NoteID}] = replaced
		m.revs[[3]string{tid, n.RoomID, old.NoteID}] = append(m.revs[[3]string{tid, n.RoomID, old.NoteID}], rev)
	}
	k := [3]string{tid, n.RoomID, stored.NoteID}
	m.notes[k] = cloneNote(stored)
	m.revs[k] = []RoomNoteRevision{revisionOf(stored, stored.Author, stored.UpdatedAtMs)}
	return cloneNote(stored), nil
}

func (m *MemoryRoomNotes) GetRoomNote(ctx context.Context, roomID, noteID string) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.notes[[3]string{tid, roomID, noteID}]
	if !ok {
		return RoomNote{}, ErrRoomNoteNotFound
	}
	return cloneNote(n), nil
}

func (m *MemoryRoomNotes) ListRoomNotes(ctx context.Context, q RoomNoteQuery) ([]RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []RoomNote{}
	for _, n := range m.roomLocked(tid, q.RoomID) {
		if matchNote(n, q) {
			out = append(out, cloneNote(n))
		}
	}
	sortNotes(out)
	return out, nil
}

func (m *MemoryRoomNotes) UpdateRoomNote(ctx context.Context, roomID, noteID string, p RoomNotePatch, by string, atMs int64) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, noteID}
	cur, ok := m.notes[k]
	if !ok {
		return RoomNote{}, ErrRoomNoteNotFound
	}
	_, active := countRoomNotes(m.roomLocked(tid, roomID))
	next, rev, err := planUpdateNote(cur, p, active, by, atMs)
	if err != nil {
		return RoomNote{}, err
	}
	m.notes[k] = next
	m.revs[k] = append(m.revs[k], rev)
	return cloneNote(next), nil
}

func (m *MemoryRoomNotes) RollbackRoomNote(ctx context.Context, roomID, noteID string, revision int, by string, atMs int64) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, noteID}
	cur, ok := m.notes[k]
	if !ok {
		return RoomNote{}, ErrRoomNoteNotFound
	}
	var from *RoomNoteRevision
	for i := range m.revs[k] {
		if m.revs[k][i].Revision == revision {
			from = &m.revs[k][i]
		}
	}
	if from == nil {
		return RoomNote{}, ErrRoomNoteRevision
	}
	next, rev, err := planRollbackNote(cur, *from, by, atMs)
	if err != nil {
		return RoomNote{}, err
	}
	m.notes[k] = next
	m.revs[k] = append(m.revs[k], rev)
	return cloneNote(next), nil
}

func (m *MemoryRoomNotes) DeleteRoomNote(ctx context.Context, roomID, noteID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, noteID}
	if _, ok := m.notes[k]; !ok {
		return ErrRoomNoteNotFound
	}
	delete(m.notes, k)
	delete(m.revs, k)
	return nil
}

func (m *MemoryRoomNotes) ListRoomNoteRevisions(ctx context.Context, roomID, noteID string) ([]RoomNoteRevision, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [3]string{tid, roomID, noteID}
	if _, ok := m.notes[k]; !ok {
		return nil, ErrRoomNoteNotFound
	}
	out := make([]RoomNoteRevision, len(m.revs[k]))
	for i, r := range m.revs[k] {
		out[len(out)-1-i] = r
	}
	return out, nil
}

// room_notes, room_note_revisions: deleted with the room.
func (m *MemoryRoomNotes) deleteRoomRows(tenantID, roomID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.notes {
		if k[0] == tenantID && k[1] == roomID {
			delete(m.notes, k)
			delete(m.revs, k)
		}
	}
}

// --- MySQL ---

// MySQLRoomNotes is the MySQL RoomNoteStore over room_notes and
// room_note_revisions.
type MySQLRoomNotes struct{ DB *sql.DB }

// NewMySQLRoomNotes builds the MySQL store.
func NewMySQLRoomNotes(db *sql.DB) *MySQLRoomNotes { return &MySQLRoomNotes{DB: db} }

const roomNoteCols = `tenant_id, room_id, note_id, type, title, body, status, supersedes, sources_json, author, pinned, revision, created_at_ms, updated_at_ms, verified_at_ms`

func scanRoomNote(r rowScanner) (RoomNote, error) {
	var n RoomNote
	var supersedes, sources sql.NullString
	var verified sql.NullInt64
	if err := r.Scan(&n.TenantID, &n.RoomID, &n.NoteID, &n.Type, &n.Title, &n.Body, &n.Status, &supersedes, &sources, &n.Author,
		&n.Pinned, &n.Revision, &n.CreatedAtMs, &n.UpdatedAtMs, &verified); err != nil {
		return RoomNote{}, err
	}
	n.Supersedes, n.VerifiedAtMs = supersedes.String, verified.Int64
	if sources.Valid && sources.String != "" {
		if err := json.Unmarshal([]byte(sources.String), &n.Sources); err != nil {
			return RoomNote{}, err
		}
	}
	return n, nil
}

func (s *MySQLRoomNotes) insertNote(ctx context.Context, tx *sql.Tx, n RoomNote) error {
	var sources any
	if len(n.Sources) > 0 {
		raw, err := json.Marshal(n.Sources)
		if err != nil {
			return err
		}
		sources = string(raw)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO room_notes (`+roomNoteCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, NULLIF(?, 0))`,
		n.TenantID, n.RoomID, n.NoteID, n.Type, n.Title, n.Body, n.Status, n.Supersedes, sources, n.Author, n.Pinned, n.Revision, n.CreatedAtMs, n.UpdatedAtMs, n.VerifiedAtMs)
	return err
}

func (s *MySQLRoomNotes) saveNote(ctx context.Context, tx *sql.Tx, n RoomNote, rev RoomNoteRevision) error {
	if _, err := tx.ExecContext(ctx, `UPDATE room_notes SET title = ?, body = ?, status = ?, pinned = ?, revision = ?, updated_at_ms = ?, verified_at_ms = NULLIF(?, 0)
		WHERE tenant_id = ? AND room_id = ? AND note_id = ?`,
		n.Title, n.Body, n.Status, n.Pinned, n.Revision, n.UpdatedAtMs, n.VerifiedAtMs, n.TenantID, n.RoomID, n.NoteID); err != nil {
		return err
	}
	return s.insertRevision(ctx, tx, n.TenantID, n.RoomID, rev)
}

func (s *MySQLRoomNotes) insertRevision(ctx context.Context, tx *sql.Tx, tid, roomID string, r RoomNoteRevision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO room_note_revisions (tenant_id, room_id, note_id, revision, type, title, body, status, pinned, changed_by, at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tid, roomID, r.NoteID, r.Revision, r.Type, r.Title, r.Body, r.Status, r.Pinned, r.ChangedBy, r.AtMs)
	return err
}

// roomNotesForUpdate reads the room's notes and locks them: writers to one
// room's notes take turns, so the caps and the brief hold under concurrency.
func (s *MySQLRoomNotes) roomNotesForUpdate(ctx context.Context, tx *sql.Tx, tid, roomID string) ([]RoomNote, error) {
	return queryAll(ctx, tx, `SELECT `+roomNoteCols+` FROM room_notes WHERE tenant_id = ? AND room_id = ? FOR UPDATE`, scanRoomNote, tid, roomID)
}

func (s *MySQLRoomNotes) CreateRoomNote(ctx context.Context, n RoomNote) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	n.TenantID = tid
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoomNote{}, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := s.roomNotesForUpdate(ctx, tx, tid, n.RoomID)
	if err != nil {
		return RoomNote{}, err
	}
	stored, old, err := planCreateNote(existing, n)
	if err != nil {
		return RoomNote{}, err
	}
	if old != nil {
		replaced, rev := supersededBy(*old, stored.Author, stored.UpdatedAtMs)
		if err := s.saveNote(ctx, tx, replaced, rev); err != nil {
			return RoomNote{}, err
		}
	}
	if err := s.insertNote(ctx, tx, stored); err != nil {
		return RoomNote{}, err
	}
	if err := s.insertRevision(ctx, tx, tid, n.RoomID, revisionOf(stored, stored.Author, stored.UpdatedAtMs)); err != nil {
		return RoomNote{}, err
	}
	return stored, tx.Commit()
}

func (s *MySQLRoomNotes) GetRoomNote(ctx context.Context, roomID, noteID string) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	n, err := scanRoomNote(s.DB.QueryRowContext(ctx, `SELECT `+roomNoteCols+` FROM room_notes WHERE tenant_id = ? AND room_id = ? AND note_id = ?`, tid, roomID, noteID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomNote{}, ErrRoomNoteNotFound
	}
	return n, err
}

func (s *MySQLRoomNotes) ListRoomNotes(ctx context.Context, q RoomNoteQuery) ([]RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	where, args := `tenant_id = ? AND room_id = ?`, []any{tid, q.RoomID}
	if q.Status != "" {
		where += ` AND status = ?`
		args = append(args, q.Status)
	}
	if q.Type != "" {
		where += ` AND type = ?`
		args = append(args, q.Type)
	}
	return queryAll(ctx, s.DB, `SELECT `+roomNoteCols+` FROM room_notes WHERE `+where+` ORDER BY updated_at_ms DESC, note_id DESC LIMIT ?`, scanRoomNote, append(args, wire.MaxRoomNotes)...)
}

// lockNote reads one note of the room for update, with the room's active
// count.
func (s *MySQLRoomNotes) lockNote(ctx context.Context, tx *sql.Tx, tid, roomID, noteID string) (RoomNote, int, error) {
	all, err := s.roomNotesForUpdate(ctx, tx, tid, roomID)
	if err != nil {
		return RoomNote{}, 0, err
	}
	_, active := countRoomNotes(all)
	for _, n := range all {
		if n.NoteID == noteID {
			return n, active, nil
		}
	}
	return RoomNote{}, 0, ErrRoomNoteNotFound
}

func (s *MySQLRoomNotes) UpdateRoomNote(ctx context.Context, roomID, noteID string, p RoomNotePatch, by string, atMs int64) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoomNote{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, active, err := s.lockNote(ctx, tx, tid, roomID, noteID)
	if err != nil {
		return RoomNote{}, err
	}
	next, rev, err := planUpdateNote(cur, p, active, by, atMs)
	if err != nil {
		return RoomNote{}, err
	}
	if err := s.saveNote(ctx, tx, next, rev); err != nil {
		return RoomNote{}, err
	}
	return next, tx.Commit()
}

func (s *MySQLRoomNotes) RollbackRoomNote(ctx context.Context, roomID, noteID string, revision int, by string, atMs int64) (RoomNote, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return RoomNote{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RoomNote{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, _, err := s.lockNote(ctx, tx, tid, roomID, noteID)
	if err != nil {
		return RoomNote{}, err
	}
	var from RoomNoteRevision
	err = tx.QueryRowContext(ctx, `SELECT note_id, revision, type, title, body, status, pinned, changed_by, at_ms FROM room_note_revisions
		WHERE tenant_id = ? AND room_id = ? AND note_id = ? AND revision = ?`, tid, roomID, noteID, revision).
		Scan(&from.NoteID, &from.Revision, &from.Type, &from.Title, &from.Body, &from.Status, &from.Pinned, &from.ChangedBy, &from.AtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return RoomNote{}, ErrRoomNoteRevision
	}
	if err != nil {
		return RoomNote{}, err
	}
	next, rev, err := planRollbackNote(cur, from, by, atMs)
	if err != nil {
		return RoomNote{}, err
	}
	if err := s.saveNote(ctx, tx, next, rev); err != nil {
		return RoomNote{}, err
	}
	return next, tx.Commit()
}

func (s *MySQLRoomNotes) DeleteRoomNote(ctx context.Context, roomID, noteID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM room_notes WHERE tenant_id = ? AND room_id = ? AND note_id = ?`, tid, roomID, noteID)
	if err != nil {
		return err
	}
	if err := requireMatched(res, ErrRoomNoteNotFound); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM room_note_revisions WHERE tenant_id = ? AND room_id = ? AND note_id = ?`, tid, roomID, noteID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLRoomNotes) ListRoomNoteRevisions(ctx context.Context, roomID, noteID string) ([]RoomNoteRevision, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.GetRoomNote(ctx, roomID, noteID); err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB, `SELECT note_id, revision, type, title, body, status, pinned, changed_by, at_ms FROM room_note_revisions
		WHERE tenant_id = ? AND room_id = ? AND note_id = ? ORDER BY revision DESC`,
		func(r rowScanner) (RoomNoteRevision, error) {
			var v RoomNoteRevision
			err := r.Scan(&v.NoteID, &v.Revision, &v.Type, &v.Title, &v.Body, &v.Status, &v.Pinned, &v.ChangedBy, &v.AtMs)
			return v, err
		}, tid, roomID, noteID)
}
