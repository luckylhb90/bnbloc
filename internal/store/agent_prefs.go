package store

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// AgentPrefs is a member's last-selected agent in one room+channel context.
// The selection follows the user across devices: desktop app and browser
// read/write the same row, so entering a room restores the agent (and model)
// the user last drove there without re-picking. Fields are strings keyed to
// the composer's vocabulary (core/provider/model/reasoning); empty means
// unset and falls through to the next preference layer.
type AgentPrefs struct {
	RoomID      string
	ChannelID   string // "" = room-level default
	Core        string
	Provider    string
	Model       string
	Reasoning   string
	UpdatedAtMs int64
}

// AgentPrefsStore persists per-user, per-room+channel agent selections.
type AgentPrefsStore interface {
	// GetAgentPrefs returns the stored selection for (room, channel); an
	// absent row is a zero value with ok=false, never an error.
	GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool)
	// PutAgentPrefs upserts the selection for (room, channel).
	PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error
}

// MySQLAgentPrefs is the MySQL-backed AgentPrefsStore.
type MySQLAgentPrefs struct{ DB *sql.DB }

// NewMySQLAgentPrefs builds a MySQLAgentPrefs over a shared pool.
func NewMySQLAgentPrefs(db *sql.DB) *MySQLAgentPrefs { return &MySQLAgentPrefs{DB: db} }

func (m *MySQLAgentPrefs) GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentPrefs{}, false
	}
	var p AgentPrefs
	err = m.DB.QueryRowContext(ctx,
		`SELECT selected_core, selected_provider, selected_model, selected_reasoning, updated_at_ms
		 FROM user_agent_prefs WHERE tenant_id=? AND user_id=? AND room_id=? AND channel_id=?`,
		tid, userID, roomID, channelID).
		Scan(&p.Core, &p.Provider, &p.Model, &p.Reasoning, &p.UpdatedAtMs)
	if err != nil {
		return AgentPrefs{}, false
	}
	p.RoomID, p.ChannelID = roomID, channelID
	return p, true
}

func (m *MySQLAgentPrefs) PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO user_agent_prefs (tenant_id, user_id, room_id, channel_id, selected_core, selected_provider, selected_model, selected_reasoning, updated_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE selected_core=VALUES(selected_core), selected_provider=VALUES(selected_provider),
		   selected_model=VALUES(selected_model), selected_reasoning=VALUES(selected_reasoning), updated_at_ms=VALUES(updated_at_ms)`,
		tid, userID, p.RoomID, p.ChannelID, p.Core, p.Provider, p.Model, p.Reasoning, p.UpdatedAtMs)
	return err
}

// MemoryAgentPrefs is the in-memory AgentPrefsStore (dev/tests).
type MemoryAgentPrefs struct {
	mu sync.Mutex
	// Keyed (tenant, user, room, channel).
	rows map[[4]string]AgentPrefs
	now  func() int64
}

func NewMemoryAgentPrefs() *MemoryAgentPrefs {
	return &MemoryAgentPrefs{rows: map[[4]string]AgentPrefs{}, now: func() int64 { return time.Now().UnixMilli() }}
}

func (m *MemoryAgentPrefs) GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentPrefs{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[[4]string{tid, userID, roomID, channelID}]
	return p, ok
}

func (m *MemoryAgentPrefs) PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.UpdatedAtMs == 0 {
		p.UpdatedAtMs = m.now()
	}
	m.rows[[4]string{tid, userID, p.RoomID, p.ChannelID}] = p
	return nil
}
