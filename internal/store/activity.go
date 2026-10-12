// Package store defines the activity persistence boundary and an in-memory
// implementation used for M0 and tests. The MySQL implementation (M4) satisfies
// the same interface. Keys are the roomId scope identifier (== workspaceId),
// with no implicit translation.
package store

import (
	"context"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

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
}

type sessionKey struct{ tenant, room, session string }

// Memory is an in-memory ActivityStore for M0 and tests.
type Memory struct {
	mu       sync.Mutex
	versions map[sessionKey]int64
	cursors  map[sessionKey]int64
	// usage keeps reported reply metrics for AgentUsage (team dashboard).
	usage []usageSample
}

// NewMemory constructs an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		versions: map[sessionKey]int64{},
		cursors:  map[sessionKey]int64{},
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
	if stored, ok := m.versions[key]; ok && in.MessageVersion <= stored {
		// Stale or equal write: keep the newer stored version (MySQL's
		// GREATEST).
		return stored, nil
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
	stored := map[int64]bool{}
	for _, msg := range in.Messages {
		if msg.Seq <= prior || stored[msg.Seq] {
			continue // replay at or below the durable cursor, or a duplicate in this report
		}
		stored[msg.Seq] = true
		fresh = append(fresh, msg)
		if msg.Seq > cursor {
			cursor = msg.Seq
		}
	}
	m.cursors[key] = cursor
	m.recordUsageLocked(tnt, roomID, fresh)
	return fresh, cursor, nil
}
