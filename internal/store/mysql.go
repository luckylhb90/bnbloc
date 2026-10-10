// Package store MySQL implementation. It targets database/sql and imports no
// specific driver, so it compiles offline; a deployment registers the MySQL
// driver (github.com/go-sql-driver/mysql) and passes a DSN. The schema is
// schema.sql (embedded), evolved by migrations.go.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MySQL implements ActivityStore over a database/sql connection. It satisfies
// the same contract as Memory, enforcing canonical version monotonicity and the
// idempotent submit-provenance barrier through SQL constraints.
type MySQL struct {
	DB *sql.DB
}

// NewMySQL constructs a MySQL-backed activity store.
func NewMySQL(db *sql.DB) *MySQL { return &MySQL{DB: db} }

// PutSessionState upserts a session snapshot, keeping the newer message_version.
func (m *MySQL) PutSessionState(ctx context.Context, in wire.ReportSessionStateInput) (int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	stateJSON, err := json.Marshal(in.State)
	if err != nil {
		return 0, err
	}
	// ON DUPLICATE KEY UPDATE only advances when the incoming version is newer;
	// GREATEST keeps the stored version otherwise (monotonic fence).
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO agent_sessions
  (tenant_id, room_id, agent_session_id, provider_session_id, owner_user_id, state_json, message_version, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  state_json = IF(VALUES(message_version) > message_version, VALUES(state_json), state_json),
  provider_session_id = IF(VALUES(message_version) > message_version, VALUES(provider_session_id), provider_session_id),
  updated_at_ms = IF(VALUES(message_version) > message_version, VALUES(updated_at_ms), updated_at_ms),
  message_version = GREATEST(message_version, VALUES(message_version))`,
		tnt, in.WorkspaceID, in.AgentSessionID, nullString(in.ProviderSessionID), in.OwnerUserID, stateJSON, in.MessageVersion, in.OccurredAtUnixMs)
	if err != nil {
		return 0, err
	}
	var stored int64
	if err := m.DB.QueryRowContext(ctx,
		`SELECT message_version FROM agent_sessions WHERE tenant_id = ? AND room_id = ? AND agent_session_id = ?`,
		tnt, in.WorkspaceID, in.AgentSessionID).Scan(&stored); err != nil {
		return 0, err
	}
	return stored, nil
}

// AppendMessages inserts messages idempotently and returns the messages newer
// than the prior stored cursor (so a reporter that re-drains from seq 0 does
// not re-publish) along with the highest cursor for the session.
func (m *MySQL) AppendMessages(ctx context.Context, roomID string, in wire.ReportMessagesInput) ([]wire.Message, int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var prior int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(message_cursor, 0) FROM sync_states
WHERE tenant_id = ? AND room_id = ? AND agent_session_id = ?`,
		tnt, roomID, in.AgentSessionID).Scan(&prior); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	cursor := prior
	var fresh []wire.Message
	for _, msg := range in.Messages {
		if msg.Seq <= prior {
			continue // replay at or below the durable cursor
		}
		payload, err := json.Marshal(msg.Payload)
		if err != nil {
			return nil, 0, err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO agent_messages (tenant_id, room_id, agent_session_id, seq, client_submit_id, payload_json, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
			tnt, roomID, in.AgentSessionID, msg.Seq, nullString(msg.ClientSubmitID), payload, msg.CreatedAtUnixMs); err != nil {
			return nil, 0, err
		}
		fresh = append(fresh, msg)
		if msg.Seq > cursor {
			cursor = msg.Seq
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO sync_states (tenant_id, room_id, agent_session_id, message_cursor)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE message_cursor = GREATEST(message_cursor, VALUES(message_cursor))`,
		tnt, roomID, in.AgentSessionID, cursor); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return fresh, cursor, nil
}

// RecordSubmitProvenance atomically persists the client-submit message. The
// unique key (room, session, client_submit_id) makes a replay a duplicate that
// maps to ErrDuplicateSubmit; the row is queryable after commit.
func (m *MySQL) RecordSubmitProvenance(ctx context.Context, in wire.SubmitProvenanceInput) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	admission, err := json.Marshal(in.TurnAdmission)
	if err != nil {
		return err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Compute the next per-session seq under a lock so concurrent submits for the
	// same session cannot pick the same seq. The range predicate on the PK prefix
	// (room_id, agent_session_id) takes InnoDB gap locks under REPEATABLE READ, so
	// even a first-ever submit (no rows yet) serializes a concurrent submit rather
	// than both computing seq=1 and colliding on the PK.
	var next int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(seq), 0) + 1 FROM agent_messages
WHERE room_id = ? AND agent_session_id = ? FOR UPDATE`,
		in.WorkspaceID, in.AgentSessionID).Scan(&next); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO agent_messages (tenant_id, room_id, agent_session_id, seq, client_submit_id, payload_json, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tnt, in.WorkspaceID, in.AgentSessionID, next, in.ClientSubmitID, admission, 0)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// The seq lock guarantees a distinct seq, so the only key INSERT IGNORE can
		// swallow is the unique (room, session, client_submit_id): a genuine replay.
		return ErrDuplicateSubmit
	}
	return tx.Commit()
}

// SubmitQueryable reports whether the client-submit id is durably persisted.
func (m *MySQL) SubmitQueryable(ctx context.Context, roomID, agentSessionID, clientSubmitID string) bool {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return false
	}
	var one int
	err = m.DB.QueryRowContext(ctx,
		`SELECT 1 FROM agent_messages WHERE tenant_id = ? AND room_id = ? AND agent_session_id = ? AND client_submit_id = ? LIMIT 1`,
		tnt, roomID, agentSessionID, clientSubmitID).Scan(&one)
	return err == nil
}

// SyncStates returns per-session sync states for a room.
func (m *MySQL) SyncStates(ctx context.Context, roomID string) map[string]wire.WorkspaceAgentSyncState {
	out := map[string]wire.WorkspaceAgentSyncState{}
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return out
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT agent_session_id, pending_count, failed_count, COALESCE(last_error, ''), message_cursor
FROM sync_states WHERE tenant_id = ? AND room_id = ?`, tnt, roomID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var st wire.WorkspaceAgentSyncState
		if err := rows.Scan(&id, &st.PendingCount, &st.FailedCount, &st.LastError, &st.MessageCursor); err != nil {
			continue
		}
		out[id] = st
	}
	return out
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// compile-time assertion that MySQL satisfies ActivityStore.
var _ ActivityStore = (*MySQL)(nil)

// ErrNoDB is returned by helpers that require a configured database.
var ErrNoDB = errors.New("store: no database configured")
