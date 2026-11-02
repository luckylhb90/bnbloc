package store

import (
	"context"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Message marks (docs/prd/2026-10-28-channels-and-threads.md §2.3): what
// people put on a stored room message, keyed by its room sequence. One store
// holds the three kinds: a reaction (a member's emoji, Value), a pin (the
// channel's, one per message: UserID and Value empty, By who pinned it) and
// a saved message (a member's own, Value empty).

// MessageMark is one mark on a message.
type MessageMark struct {
	RoomID      string
	ChannelID   string
	Seq         int64
	Kind        string // wire.MarkReaction, wire.MarkPin, wire.MarkSaved
	UserID      string // the member a reaction or a save is theirs; "" for a pin
	Value       string // a reaction's emoji; "" otherwise
	By          string // who made it
	CreatedAtMs int64
}

// View is the wire projection of a mark.
func (m MessageMark) View() wire.MessageMark {
	return wire.MessageMark{ChannelID: m.ChannelID, Seq: m.Seq, Kind: m.Kind, UserID: m.UserID, Value: m.Value, By: m.By, CreatedAtMs: m.CreatedAtMs}
}

// MessageMarkStore persists message marks. Every method is tenant-scoped by
// the context's tenant.
type MessageMarkStore interface {
	// PutMessageMark records a mark; created=false when the same mark (room,
	// seq, kind, user, value) was already there, left as it was.
	PutMessageMark(ctx context.Context, m MessageMark) (created bool, err error)
	// DeleteMessageMark removes one mark; removed=false when it was not there.
	DeleteMessageMark(ctx context.Context, roomID string, seq int64, kind, userID, value string) (removed bool, err error)
	// DeleteMessageMarks removes every mark of a message (it was deleted).
	DeleteMessageMarks(ctx context.Context, roomID string, seq int64) error
	// CountMessageReactions counts the distinct reaction values on a message.
	CountMessageReactions(ctx context.Context, roomID string, seq int64) (int, error)
	// ListMessageMarks returns a room's marks, newest message first, at most
	// limit.
	ListMessageMarks(ctx context.Context, roomID string, limit int) ([]MessageMark, error)
}

// --- memory ---

// MemoryMessageMarks is the in-memory MessageMarkStore (dev / tests).
type MemoryMessageMarks struct {
	mu   sync.Mutex
	rows map[string][]MessageMark // tenant\x00room -> marks
}

// NewMemoryMessageMarks constructs an empty in-memory mark store.
func NewMemoryMessageMarks() *MemoryMessageMarks {
	return &MemoryMessageMarks{rows: map[string][]MessageMark{}}
}

var _ MessageMarkStore = (*MemoryMessageMarks)(nil)

func sameMark(m MessageMark, seq int64, kind, userID, value string) bool {
	return m.Seq == seq && m.Kind == kind && m.UserID == userID && m.Value == value
}

func (s *MemoryMessageMarks) PutMessageMark(ctx context.Context, m MessageMark) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tid + "\x00" + m.RoomID
	for _, cur := range s.rows[k] {
		if sameMark(cur, m.Seq, m.Kind, m.UserID, m.Value) {
			return false, nil
		}
	}
	s.rows[k] = append(s.rows[k], m)
	return true, nil
}

func (s *MemoryMessageMarks) DeleteMessageMark(ctx context.Context, roomID string, seq int64, kind, userID, value string) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tid + "\x00" + roomID
	for i, cur := range s.rows[k] {
		if sameMark(cur, seq, kind, userID, value) {
			s.rows[k] = append(s.rows[k][:i], s.rows[k][i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *MemoryMessageMarks) DeleteMessageMarks(ctx context.Context, roomID string, seq int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropSeqs(tid, roomID, map[int64]bool{seq: true})
	return nil
}

// dropSeqs removes the marks of the given messages (s.mu held); it returns
// how many went.
func (s *MemoryMessageMarks) dropSeqs(tid, roomID string, seqs map[int64]bool) int {
	k := tid + "\x00" + roomID
	kept := s.rows[k][:0]
	n := 0
	for _, cur := range s.rows[k] {
		if seqs[cur.Seq] {
			n++
			continue
		}
		kept = append(kept, cur)
	}
	s.rows[k] = kept
	return n
}

func (s *MemoryMessageMarks) CountMessageReactions(ctx context.Context, roomID string, seq int64) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]bool{}
	for _, cur := range s.rows[tid+"\x00"+roomID] {
		if cur.Seq == seq && cur.Kind == wire.MarkReaction {
			values[cur.Value] = true
		}
	}
	return len(values), nil
}

func (s *MemoryMessageMarks) ListMessageMarks(ctx context.Context, roomID string, limit int) ([]MessageMark, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]MessageMark{}, s.rows[tid+"\x00"+roomID]...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq > out[j].Seq
		}
		return out[i].CreatedAtMs < out[j].CreatedAtMs
	})
	if n := clampLimit(limit, wire.MaxRoomMarks, wire.MaxRoomMarks); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// deleteRoomRows is DeleteRoom's step for message_marks.
func (s *MemoryMessageMarks) deleteRoomRows(tenantID, roomID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, tenantID+"\x00"+roomID)
}

// --- MySQL ---

// MySQLMessageMarks is the MySQL-backed MessageMarkStore.
type MySQLMessageMarks struct{ DB *DB }

// NewMySQLMessageMarks constructs a MySQL mark store.
func NewMySQLMessageMarks(db *DB) *MySQLMessageMarks { return &MySQLMessageMarks{DB: db} }

var _ MessageMarkStore = (*MySQLMessageMarks)(nil)

func (s *MySQLMessageMarks) PutMessageMark(ctx context.Context, m MessageMark) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO message_marks
		(tenant_id, room_id, channel_id, seq, kind, user_id, value, created_by, created_at_ms)
		VALUES (?,?,?,?,?,?,?,?,?)`, tid, m.RoomID, m.ChannelID, m.Seq, m.Kind, m.UserID, m.Value, m.By, m.CreatedAtMs)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *MySQLMessageMarks) DeleteMessageMark(ctx context.Context, roomID string, seq int64, kind, userID, value string) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM message_marks
		WHERE tenant_id = ? AND room_id = ? AND seq = ? AND kind = ? AND user_id = ? AND value = ?`, tid, roomID, seq, kind, userID, value)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *MySQLMessageMarks) DeleteMessageMarks(ctx context.Context, roomID string, seq int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM message_marks WHERE tenant_id = ? AND room_id = ? AND seq = ?`, tid, roomID, seq)
	return err
}

func (s *MySQLMessageMarks) CountMessageReactions(ctx context.Context, roomID string, seq int64) (int, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT value) FROM message_marks
		WHERE tenant_id = ? AND room_id = ? AND seq = ? AND kind = ?`, tid, roomID, seq, wire.MarkReaction).Scan(&n)
	return n, err
}

func (s *MySQLMessageMarks) ListMessageMarks(ctx context.Context, roomID string, limit int) ([]MessageMark, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	out, err := queryAll(ctx, s.DB, `SELECT room_id, channel_id, seq, kind, user_id, value, created_by, created_at_ms
		FROM message_marks WHERE tenant_id = ? AND room_id = ? ORDER BY seq DESC, created_at_ms ASC LIMIT ?`,
		func(row rowScanner) (MessageMark, error) {
			var m MessageMark
			err := row.Scan(&m.RoomID, &m.ChannelID, &m.Seq, &m.Kind, &m.UserID, &m.Value, &m.By, &m.CreatedAtMs)
			return m, err
		}, tid, roomID, clampLimit(limit, wire.MaxRoomMarks, wire.MaxRoomMarks))
	if out == nil && err == nil {
		out = []MessageMark{}
	}
	return out, err
}
