// Package store defines the activity persistence boundary and an in-memory
// implementation used for M0 and tests. The MySQL implementation (M4) satisfies
// the same interface. Keys are the roomId scope identifier (== workspaceId),
// with no implicit translation.
package store

import (
	"context"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// ErrDuplicateSubmit indicates the client-submit id already exists for the
// session; the submit-provenance barrier is idempotent, so this is a benign
// replay and callers may treat it as success.
var ErrDuplicateSubmit = errors.New("store: duplicate client-submit id")

// ActivityStore persists projected agent activity per room. Implementations
// must be safe for concurrent use.
type ActivityStore interface {
	// PutSessionState upserts a session snapshot, rejecting a stale write whose
	// messageVersion is not greater than the stored version. Returns the sync
	// cursor (the accepted version).
	PutSessionState(ctx context.Context, in wire.ReportSessionStateInput) (int64, error)
	// AppendMessages appends messages idempotently and returns the messages that
	// were genuinely new (seq greater than the prior stored cursor) along with
	// the next cursor. Replays at or below the cursor are dropped so callers
	// publish each message to a room at most once.
	AppendMessages(ctx context.Context, roomID string, in wire.ReportMessagesInput) ([]wire.Message, int64, error)
	// RecordSubmitProvenance atomically persists the client-submit message and
	// turn admission. It returns only after clientSubmitId is queryable. A
	// duplicate id returns ErrDuplicateSubmit.
	RecordSubmitProvenance(ctx context.Context, in wire.SubmitProvenanceInput) error
	// SubmitQueryable reports whether a client-submit id is durably queryable,
	// proving the barrier completed.
	SubmitQueryable(ctx context.Context, roomID, agentSessionID, clientSubmitID string) bool
	// SyncStates returns per-session sync states for a room.
	SyncStates(ctx context.Context, roomID string) map[string]wire.WorkspaceAgentSyncState
}

type sessionKey struct{ tenant, room, session string }

// Memory is an in-memory ActivityStore for M0 and tests.
type Memory struct {
	mu       sync.Mutex
	versions map[sessionKey]int64
	cursors  map[sessionKey]int64
	submits  map[sessionKey]map[string]struct{}
	sync     map[sessionKey]wire.WorkspaceAgentSyncState
	// usage keeps reported reply metrics for AgentUsage (team dashboard).
	usage []usageSample
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		versions: map[sessionKey]int64{},
		cursors:  map[sessionKey]int64{},
		submits:  map[sessionKey]map[string]struct{}{},
		sync:     map[sessionKey]wire.WorkspaceAgentSyncState{},
	}
}

// PutSessionState enforces canonical version monotonicity.
func (m *Memory) PutSessionState(ctx context.Context, in wire.ReportSessionStateInput) (int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{tnt, in.WorkspaceID, in.AgentSessionID}
	if in.MessageVersion <= m.versions[key] && m.versions[key] != 0 {
		// Stale or equal write: keep the newer stored version.
		return m.versions[key], nil
	}
	m.versions[key] = in.MessageVersion
	return in.MessageVersion, nil
}

// AppendMessages advances the message cursor and returns only messages newer
// than the prior cursor, so replays (e.g. a reconnected reporter re-draining
// from seq 0) are dropped instead of re-published to the room.
func (m *Memory) AppendMessages(ctx context.Context, roomID string, in wire.ReportMessagesInput) ([]wire.Message, int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{tnt, roomID, in.AgentSessionID}
	cursor := m.cursors[key]
	prior := cursor
	var fresh []wire.Message
	for _, msg := range in.Messages {
		if msg.Seq <= prior {
			continue // replay at or below the durable cursor
		}
		fresh = append(fresh, msg)
		if msg.Seq > cursor {
			cursor = msg.Seq
		}
	}
	m.cursors[key] = cursor
	m.recordUsageLocked(tnt, fresh)
	st := m.sync[key]
	st.MessageCursor = cursor
	m.sync[key] = st
	return fresh, cursor, nil
}

// RecordSubmitProvenance is the atomic idempotent barrier.
func (m *Memory) RecordSubmitProvenance(ctx context.Context, in wire.SubmitProvenanceInput) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{tnt, in.WorkspaceID, in.AgentSessionID}
	set := m.submits[key]
	if set == nil {
		set = map[string]struct{}{}
		m.submits[key] = set
	}
	if _, ok := set[in.ClientSubmitID]; ok {
		return ErrDuplicateSubmit
	}
	set[in.ClientSubmitID] = struct{}{}
	return nil
}

// SubmitQueryable reports whether the client-submit id is durably recorded.
func (m *Memory) SubmitQueryable(ctx context.Context, roomID, agentSessionID, clientSubmitID string) bool {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.submits[sessionKey{tnt, roomID, agentSessionID}]
	if set == nil {
		return false
	}
	_, ok := set[clientSubmitID]
	return ok
}

// SyncStates returns a copy of per-session sync states for a room.
func (m *Memory) SyncStates(ctx context.Context, roomID string) map[string]wire.WorkspaceAgentSyncState {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return map[string]wire.WorkspaceAgentSyncState{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]wire.WorkspaceAgentSyncState{}
	for key, st := range m.sync {
		if key.tenant == tnt && key.room == roomID {
			out[key.session] = st
		}
	}
	return out
}
