package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// Agent workspaces (docs/bugs/2026-10-13-agent-loses-its-workspace.md).
// Every agent of a room keeps ONE workspace per execution kind: a drive
// slot's sessions come and go (another expert, a stale session rebound, a
// core restart, a bridge restart), its workspace stays, so the files the
// agent wrote on one turn are there on the next. agentWorkspaces is the one
// owner of which workspace a slot uses (ensureSession reads and binds it
// only through it): a workspace is created once per slot, and replaced only
// when its core no longer holds it (core.NotFound on the session create).
//
// The bindings are persisted in the bridge's state directory
// (BNBLOC_BRIDGE_STATE_DIR, the member's own directory on the desktop), so
// a bridge restart finds its agents' workspaces; without one they live in
// memory and a restart starts each agent in a new workspace. A workspace
// also keeps what bnbloc copied into its .room/deliverables/ (the copies
// outlive the sessions that read them), and, while the bridge runs, what
// the sessions of that workspace share (workspaceShared): an agent has a
// session per channel (docs/prd/2026-10-13-sessions-per-channel.md) and
// one working tree.

// workspacesFile is the bindings file in the state directory.
const workspacesFile = "workspaces.json"

// agentWorkspace is one slot's workspace: the core's workspace id, the
// deliverable copies its .room/ holds, and what its sessions share.
type agentWorkspace struct {
	id     string
	copies roomlog.CopyState
	shared *workspaceShared
}

// workspaceShared is what the sessions of one workspace share while the
// bridge runs: the .room/ tree's pull state (the records are the room's,
// the same for every channel), the file listing deliverable publishing
// diffs against and the lock that serializes those publishes, and the
// sandbox fence's baseline (the protected paths bnbloc last left there).
type workspaceShared struct {
	mu    sync.Mutex
	room  roomlog.CtxState
	fence string
	// pass serializes the .room/ passes (drivePrompt).
	pass sync.Mutex

	publishMu sync.Mutex
	// published is the workspace's file listing as of the previous
	// deliverable publish (nil before the first): the diff against the
	// current listing is what changed since. Guarded by publishMu.
	published map[string]fileStamp
	// edits maps a path a room file was placed at for editing to that
	// file's artifact (file_edit.go): a later rewrite of the path, in any
	// turn, is a version of that file, never a new deliverable.
	edits map[string]string
}

// rememberEdit records that path holds the room file artifactID.
func (s *workspaceShared) rememberEdit(path, artifactID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.edits == nil {
		s.edits = map[string]string{}
	}
	s.edits[path] = artifactID
}

// editTargetOf is the room file path was placed for ("" = none).
func (s *workspaceShared) editTargetOf(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edits[path]
}

// roomState is the .room/ tree's pull state.
func (s *workspaceShared) roomState() roomlog.CtxState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.room
}

func (s *workspaceShared) setRoomState(st roomlog.CtxState) {
	s.mu.Lock()
	s.room = st
	s.mu.Unlock()
}

type agentWorkspaces struct {
	mu   sync.Mutex
	path string // "" = memory only
	m    map[driveKey]agentWorkspace
	// sessions are the channel sessions bound in these workspaces, kept so
	// a bridge restart resumes them (session_restore.go).
	sessions map[sessionKey]sessionRecord
}

// workspaceRecord is one binding as persisted. Fence is the sandbox fence
// baseline (the protected paths' fingerprint bnbloc last left there), so a
// restored session's next turn still finds what the guest changed.
type workspaceRecord struct {
	Room      string `json:"room"`
	Core      string `json:"core"`
	Provider  string `json:"provider"`
	Sandboxed bool   `json:"sandboxed,omitempty"`
	// OwnMachine: the slot runs on the member's own sandbox machine.
	OwnMachine bool `json:"ownMachine,omitempty"`
	// SeatOwner and Requester are the slot's seat (driveKey): empty for
	// the owner's own.
	SeatOwner   string            `json:"seatOwner,omitempty"`
	Requester   string            `json:"requester,omitempty"`
	WorkspaceID string            `json:"workspaceId"`
	Copies      roomlog.CopyState `json:"copies,omitempty"`
	Fence       string            `json:"fence,omitempty"`
	// Records is the .room/ tree's pull state (each channel's chunks, the
	// revisions cursor, the mirrored attachments): a restarted bridge
	// continues the records it wrote instead of re-chunking from scratch
	// (stale chunk files would survive, and edits or deletions made while
	// it was down would go unseen).
	Records *roomlog.CtxState `json:"records,omitempty"`
}

type workspacesDoc struct {
	Workspaces []workspaceRecord `json:"workspaces"`
	Sessions   []sessionRecord   `json:"sessions,omitempty"`
}

// newAgentWorkspaces loads the bindings persisted under dir ("" = memory
// only). An unreadable file is moved aside and reported, never silently
// reset: the agents then start in new workspaces, and the old ones stay on
// their cores.
func newAgentWorkspaces(dir string) *agentWorkspaces {
	w := &agentWorkspaces{m: map[driveKey]agentWorkspace{}, sessions: map[sessionKey]sessionRecord{}}
	if dir == "" {
		return w
	}
	w.path = filepath.Join(dir, workspacesFile)
	raw, err := os.ReadFile(w.path)
	if errors.Is(err, fs.ErrNotExist) {
		return w
	}
	var doc workspacesDoc
	if err == nil {
		err = json.Unmarshal(raw, &doc)
	}
	if err != nil {
		aside := fmt.Sprintf("%s.unreadable-%d", w.path, time.Now().UnixMilli())
		_ = os.Rename(w.path, aside)
		slog.Warn("bnbloc bridge workspace bindings unreadable; moved aside",
			"event", "bnbloc.bridge.workspaces_unreadable", "path", w.path, "aside", aside, "error", err)
		return w
	}
	for _, r := range doc.Workspaces {
		if r.WorkspaceID == "" {
			continue
		}
		k := driveKey{room: r.Room, core: r.Core, provider: r.Provider, sandboxed: r.Sandboxed, ownMachine: r.OwnMachine, owner: r.SeatOwner, requester: r.Requester}
		shared := &workspaceShared{fence: r.Fence}
		if r.Records != nil {
			shared.room = *r.Records
		}
		w.m[k] = agentWorkspace{id: r.WorkspaceID, copies: r.Copies, shared: shared}
	}
	for _, r := range doc.Sessions {
		if r.SessionID == "" || r.WorkspaceID == "" {
			continue
		}
		w.sessions[r.key()] = r
	}
	return w
}

// id is the workspace slot k uses ("", false before its first session).
func (w *agentWorkspaces) id(k driveKey) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ws, ok := w.m[k]
	return ws.id, ok
}

// bind makes id slot k's workspace (a new workspace holds no copies).
func (w *agentWorkspaces) bind(k driveKey, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[k] = agentWorkspace{id: id, shared: &workspaceShared{}}
	w.dropSessionsLocked(func(sk sessionKey, r sessionRecord) bool { return sk.driveKey == k && r.WorkspaceID != id })
	w.saveLocked()
}

// shared is what the sessions of workspace id of slot k share (a detached
// one when the slot is bound to another workspace meanwhile).
func (w *agentWorkspaces) shared(k driveKey, id string) *workspaceShared {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ws := w.m[k]; ws.id == id && ws.shared != nil {
		return ws.shared
	}
	return &workspaceShared{}
}

// unbind drops slot k's binding when it is still workspace id (a
// workspace created for a session that never started).
func (w *agentWorkspaces) unbind(k driveKey, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ws, ok := w.m[k]; ok && ws.id == id {
		delete(w.m, k)
		w.dropSessionsLocked(func(sk sessionKey, _ sessionRecord) bool { return sk.driveKey == k })
		w.saveLocked()
	}
}

// forgetRoom drops the bindings of a room that is gone and returns its
// workspaces (slot → workspace id).
func (w *agentWorkspaces) forgetRoom(room string) map[driveKey]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := false
	gone := map[driveKey]string{}
	for k, ws := range w.m {
		if k.room == room {
			gone[k] = ws.id
			delete(w.m, k)
			changed = true
		}
	}
	if w.dropSessionsLocked(func(sk sessionKey, _ sessionRecord) bool { return sk.room == room }) {
		changed = true
	}
	if changed {
		w.saveLocked()
	}
	return gone
}

// session is channel session sk's persisted binding (false = none).
func (w *agentWorkspaces) session(sk sessionKey) (sessionRecord, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, ok := w.sessions[sk]
	return r, ok
}

// putSession records channel session sk's binding (and saves the
// workspace's fence with it).
func (w *agentWorkspaces) putSession(sk sessionKey, r sessionRecord) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if cur, ok := w.sessions[sk]; ok && reflect.DeepEqual(cur, r) {
		return
	}
	w.sessions[sk] = r
	w.saveLocked()
}

// dropSession forgets channel session sk's binding when it is still
// session id ("" = whichever).
func (w *agentWorkspaces) dropSession(sk sessionKey, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dropSessionsLocked(func(k sessionKey, r sessionRecord) bool { return k == sk && (id == "" || r.SessionID == id) }) {
		w.saveLocked()
	}
}

func (w *agentWorkspaces) dropSessionsLocked(match func(sessionKey, sessionRecord) bool) bool {
	dropped := false
	for k, r := range w.sessions {
		if match(k, r) {
			delete(w.sessions, k)
			dropped = true
		}
	}
	return dropped
}

// copies is what workspace id of slot k holds of the room's deliverables.
func (w *agentWorkspaces) copies(k driveKey, id string) roomlog.CopyState {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ws := w.m[k]; ws.id == id {
		return ws.copies
	}
	return nil
}

// setCopies records the copies a pass left in workspace id (ignored when
// the slot was rebound meanwhile).
func (w *agentWorkspaces) setCopies(k driveKey, id string, st roomlog.CopyState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ws, ok := w.m[k]
	if !ok || ws.id != id || copiesEqual(ws.copies, st) {
		return
	}
	ws.copies = st
	w.m[k] = ws
	w.saveLocked()
}

// roomFilesCleared records that slot k's .room/ tree was removed (the
// sandbox workdir's reset): its records and copies are gone.
func (w *agentWorkspaces) roomFilesCleared(k driveKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ws, ok := w.m[k]
	if !ok {
		return
	}
	if ws.shared != nil {
		ws.shared.setRoomState(roomlog.CtxState{})
	}
	ws.copies = nil
	w.m[k] = ws
	w.saveLocked()
}

// roomStateWritten persists slot k's .room/ pull state after a pass
// (ignored when the slot was rebound meanwhile).
func (w *agentWorkspaces) roomStateWritten(k driveKey, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ws, ok := w.m[k]; ok && ws.id == id {
		w.saveLocked()
	}
}

// roomStateEmpty reports a pull state with nothing written yet.
func roomStateEmpty(st roomlog.CtxState) bool {
	return len(st.Channels) == 0 && st.ReadmeSum == "" && len(st.Attachments) == 0 && st.RevisedMs == 0
}

func copiesEqual(a, b roomlog.CopyState) bool {
	if len(a) != len(b) {
		return false
	}
	for id, v := range a {
		if bv, ok := b[id]; !ok || bv != v {
			return false
		}
	}
	return true
}

// saveLocked writes the bindings (atomically, owner-only). A failed write
// is logged: the bindings in memory still hold for this run.
func (w *agentWorkspaces) saveLocked() {
	if w.path == "" {
		return
	}
	doc := workspacesDoc{Workspaces: make([]workspaceRecord, 0, len(w.m))}
	for k, ws := range w.m {
		rec := workspaceRecord{Room: k.room, Core: k.core, Provider: k.provider, Sandboxed: k.sandboxed, OwnMachine: k.ownMachine, SeatOwner: k.owner, Requester: k.requester, WorkspaceID: ws.id, Copies: ws.copies}
		if ws.shared != nil {
			ws.shared.mu.Lock()
			rec.Fence = ws.shared.fence
			if st := ws.shared.room; !roomStateEmpty(st) {
				rec.Records = &st
			}
			ws.shared.mu.Unlock()
		}
		doc.Workspaces = append(doc.Workspaces, rec)
	}
	for _, r := range w.sessions {
		doc.Sessions = append(doc.Sessions, r)
	}
	sort.Slice(doc.Sessions, func(i, j int) bool { return sessionRecordLess(doc.Sessions[i], doc.Sessions[j]) })
	sort.Slice(doc.Workspaces, func(i, j int) bool {
		a, b := doc.Workspaces[i], doc.Workspaces[j]
		if a.Room != b.Room {
			return a.Room < b.Room
		}
		if a.Core != b.Core {
			return a.Core < b.Core
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Sandboxed != b.Sandboxed {
			return !a.Sandboxed
		}
		if a.OwnMachine != b.OwnMachine {
			return !a.OwnMachine
		}
		if a.SeatOwner != b.SeatOwner {
			return a.SeatOwner < b.SeatOwner
		}
		return a.Requester < b.Requester
	})
	if err := writeFileAtomic(w.path, doc); err != nil {
		slog.Warn("bnbloc bridge workspace bindings not saved",
			"event", "bnbloc.bridge.workspaces_save_failed", "path", w.path, "error", err)
	}
}

// writeFileAtomic writes v as JSON to path through a temp file and a
// rename, owner-only.
func writeFileAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
