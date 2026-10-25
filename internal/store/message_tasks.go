package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Message tasks (F6): a persisted room message marked as a task, keyed by its
// room sequence. Tasks are messages, not a separate board: the row only holds
// status plus a title/author snapshot taken when the task was marked.

// ErrMessageTaskTaken refuses a task on a (room, seq) another tenant holds:
// the key carries no tenant, so a write never changes another tenant's row.
var ErrMessageTaskTaken = errors.New("store: message task key held by another tenant")

// MessageTaskStore persists message tasks. Every method is tenant-scoped by the
// context's tenant.
type MessageTaskStore interface {
	// ListMessageTasks returns a room's tasks, oldest message first.
	ListMessageTasks(ctx context.Context, roomID string) ([]wire.MessageTask, error)
	// GetMessageTask returns the task on one message, found=false when unmarked.
	GetMessageTask(ctx context.Context, roomID string, seq int64) (t wire.MessageTask, found bool, err error)
	// PutMessageTask upserts a task. On an existing row only the status and
	// done fields change; the creation snapshot is kept. It returns the row
	// as stored; ErrMessageTaskTaken when another tenant holds (room, seq).
	PutMessageTask(ctx context.Context, t wire.MessageTask) (wire.MessageTask, error)
	// DeleteMessageTask unmarks a message; removed=false when it was not a task.
	DeleteMessageTask(ctx context.Context, roomID string, seq int64) (removed bool, err error)
}

// --- memory ---

// MemoryMessageTasks is the in-memory MessageTaskStore (dev / tests).
type MemoryMessageTasks struct {
	mu   sync.Mutex
	rows map[string]map[int64]wire.MessageTask // tenant\x00room -> seq -> task
}

// NewMemoryMessageTasks constructs an empty in-memory task store.
func NewMemoryMessageTasks() *MemoryMessageTasks {
	return &MemoryMessageTasks{rows: map[string]map[int64]wire.MessageTask{}}
}

var _ MessageTaskStore = (*MemoryMessageTasks)(nil)

func (s *MemoryMessageTasks) ListMessageTasks(ctx context.Context, roomID string) ([]wire.MessageTask, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []wire.MessageTask{}
	for _, t := range s.rows[tid+"\x00"+roomID] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *MemoryMessageTasks) GetMessageTask(ctx context.Context, roomID string, seq int64) (wire.MessageTask, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.MessageTask{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rows[tid+"\x00"+roomID][seq]
	return t, ok, nil
}

func (s *MemoryMessageTasks) PutMessageTask(ctx context.Context, t wire.MessageTask) (wire.MessageTask, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.MessageTask{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + t.RoomID
	for k, byseq := range s.rows {
		if _, held := byseq[t.Seq]; held && k != rk && strings.HasSuffix(k, "\x00"+t.RoomID) {
			return wire.MessageTask{}, ErrMessageTaskTaken
		}
	}
	if s.rows[rk] == nil {
		s.rows[rk] = map[int64]wire.MessageTask{}
	}
	if cur, ok := s.rows[rk][t.Seq]; ok {
		cur.Status, cur.DoneBy, cur.DoneAtMs = t.Status, t.DoneBy, t.DoneAtMs
		t = cur
	}
	s.rows[rk][t.Seq] = t
	return t, nil
}

func (s *MemoryMessageTasks) DeleteMessageTask(ctx context.Context, roomID string, seq int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rk := tid + "\x00" + roomID
	if _, ok := s.rows[rk][seq]; !ok {
		return false, nil
	}
	delete(s.rows[rk], seq)
	return true, nil
}

// --- MySQL ---

// MySQLMessageTasks is the MySQL-backed MessageTaskStore.
type MySQLMessageTasks struct{ DB *DB }

// NewMySQLMessageTasks constructs a MySQL task store.
func NewMySQLMessageTasks(db *DB) *MySQLMessageTasks { return &MySQLMessageTasks{DB: db} }

var _ MessageTaskStore = (*MySQLMessageTasks)(nil)

const messageTaskCols = `room_id, channel_id, seq, status, title, message_author, created_by, created_at_ms, COALESCE(done_by, ''), COALESCE(done_at_ms, 0)`

func scanMessageTask(row rowScanner) (wire.MessageTask, error) {
	var t wire.MessageTask
	err := row.Scan(&t.RoomID, &t.ChannelID, &t.Seq, &t.Status, &t.Title, &t.MessageAuthor, &t.CreatedBy, &t.CreatedAtMs, &t.DoneBy, &t.DoneAtMs)
	return t, err
}

func (s *MySQLMessageTasks) ListMessageTasks(ctx context.Context, roomID string) ([]wire.MessageTask, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB, `SELECT `+messageTaskCols+` FROM message_tasks
		WHERE tenant_id = ? AND room_id = ? ORDER BY seq`, scanMessageTask, tid, roomID)
}

func (s *MySQLMessageTasks) GetMessageTask(ctx context.Context, roomID string, seq int64) (wire.MessageTask, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.MessageTask{}, false, err
	}
	t, err := scanMessageTask(s.DB.QueryRowContext(ctx, `SELECT `+messageTaskCols+` FROM message_tasks
		WHERE tenant_id = ? AND room_id = ? AND seq = ?`, tid, roomID, seq))
	if errors.Is(err, sql.ErrNoRows) {
		return wire.MessageTask{}, false, nil
	}
	if err != nil {
		return wire.MessageTask{}, false, err
	}
	return t, true, nil
}

func (s *MySQLMessageTasks) PutMessageTask(ctx context.Context, t wire.MessageTask) (wire.MessageTask, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return wire.MessageTask{}, err
	}
	// The key (room_id, seq) carries no tenant: the update applies only to
	// the ctx tenant's row; another tenant's row reads back as missing.
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO message_tasks
		(tenant_id, room_id, channel_id, seq, status, title, message_author, created_by, created_at_ms, done_by, done_at_ms)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE
		  status = IF(tenant_id = VALUES(tenant_id), VALUES(status), status),
		  done_by = IF(tenant_id = VALUES(tenant_id), VALUES(done_by), done_by),
		  done_at_ms = IF(tenant_id = VALUES(tenant_id), VALUES(done_at_ms), done_at_ms)`,
		tid, t.RoomID, t.ChannelID, t.Seq, t.Status, t.Title, t.MessageAuthor, t.CreatedBy, t.CreatedAtMs, nullIfZero(t.DoneBy), nullIfZero(t.DoneAtMs)); err != nil {
		return wire.MessageTask{}, err
	}
	stored, found, err := s.GetMessageTask(ctx, t.RoomID, t.Seq)
	if err != nil {
		return wire.MessageTask{}, err
	}
	if !found {
		return wire.MessageTask{}, ErrMessageTaskTaken
	}
	return stored, nil
}

func (s *MySQLMessageTasks) DeleteMessageTask(ctx context.Context, roomID string, seq int64) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx, `DELETE FROM message_tasks WHERE tenant_id = ? AND room_id = ? AND seq = ?`, tid, roomID, seq)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
