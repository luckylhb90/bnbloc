package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// AgentPrefs is a member's last-selected agent in one room+channel context.
// The selection follows the user across devices: desktop app and browser
// read/write the same row, so entering a room restores the agent (and model)
// the user last drove there without re-picking. Fields are strings keyed to
// the composer's vocabulary (core/provider/model/reasoning); empty means
// unset and falls through to the next preference layer.
type AgentPrefs struct {
	RoomID    string
	ChannelID string // "" = room-level default
	// OwnerID is whose agent it is: the member's own or one they borrow.
	// (core, provider) alone names a harness, not an agent.
	OwnerID     string
	Core        string
	Provider    string
	Model       string
	Reasoning   string
	UpdatedAtMs int64
}

// AgentPrefsStore persists per-user, per-room+channel agent selections.
type AgentPrefsStore interface {
	// GetAgentPrefs returns the stored selection for (room, channel); an
	// absent row is a zero value with ok=false, a read failure an error.
	GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool, error)
	// PutAgentPrefs upserts the selection for (room, channel): every field
	// is replaced, UpdatedAtMs included (the caller stamps it).
	PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error
}

var (
	_ AgentPrefsStore = (*MemoryAgentPrefs)(nil)
	_ AgentPrefsStore = (*MySQLAgentPrefs)(nil)
)

// MySQLAgentPrefs is the MySQL-backed AgentPrefsStore.
type MySQLAgentPrefs struct{ DB *sql.DB }

// NewMySQLAgentPrefs builds a MySQLAgentPrefs over a shared pool.
func NewMySQLAgentPrefs(db *sql.DB) *MySQLAgentPrefs { return &MySQLAgentPrefs{DB: db} }

func (m *MySQLAgentPrefs) GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentPrefs{}, false, err
	}
	var p AgentPrefs
	err = m.DB.QueryRowContext(ctx,
		`SELECT selected_owner, selected_core, selected_provider, selected_model, selected_reasoning, updated_at_ms
		 FROM user_agent_prefs WHERE tenant_id=? AND user_id=? AND room_id=? AND channel_id=?`,
		tid, userID, roomID, channelID).
		Scan(&p.OwnerID, &p.Core, &p.Provider, &p.Model, &p.Reasoning, &p.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentPrefs{}, false, nil
	}
	if err != nil {
		return AgentPrefs{}, false, err
	}
	p.RoomID, p.ChannelID = roomID, channelID
	return p, true, nil
}

func (m *MySQLAgentPrefs) PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO user_agent_prefs (tenant_id, user_id, room_id, channel_id, selected_owner, selected_core, selected_provider, selected_model, selected_reasoning, updated_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE selected_owner=VALUES(selected_owner), selected_core=VALUES(selected_core), selected_provider=VALUES(selected_provider),
		   selected_model=VALUES(selected_model), selected_reasoning=VALUES(selected_reasoning), updated_at_ms=VALUES(updated_at_ms)`,
		tid, userID, p.RoomID, p.ChannelID, p.OwnerID, p.Core, p.Provider, p.Model, p.Reasoning, p.UpdatedAtMs)
	return err
}

// MemoryAgentPrefs is the in-memory AgentPrefsStore (dev/tests).
type MemoryAgentPrefs struct {
	mu sync.Mutex
	// Keyed (tenant, user, room, channel).
	rows map[[4]string]AgentPrefs
}

func NewMemoryAgentPrefs() *MemoryAgentPrefs {
	return &MemoryAgentPrefs{rows: map[[4]string]AgentPrefs{}}
}

func (m *MemoryAgentPrefs) GetAgentPrefs(ctx context.Context, roomID, channelID, userID string) (AgentPrefs, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return AgentPrefs{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.rows[[4]string{tid, userID, roomID, channelID}]
	return p, ok, nil
}

func (m *MemoryAgentPrefs) PutAgentPrefs(ctx context.Context, p AgentPrefs, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[[4]string{tid, userID, p.RoomID, p.ChannelID}] = p
	return nil
}
