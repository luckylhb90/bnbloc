// Package store MySQL implementation. It targets database/sql and imports no
// specific driver, so it compiles offline; a deployment registers the MySQL
// driver (github.com/go-sql-driver/mysql) and passes a DSN. The schema is
// schema.sql (embedded), evolved by migrations.go.
package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MySQL implements ActivityStore over a database/sql connection. It satisfies
// the same contract as Memory, enforcing canonical version monotonicity and
// idempotent message appends through SQL constraints.
type MySQL struct {
	DB *sql.DB
}

// NewMySQL constructs a MySQL-backed activity store.
func NewMySQL(db *sql.DB) *MySQL { return &MySQL{DB: db} }

// PutSessionState advances the session's message_version (a monotonic
// fence: GREATEST keeps the stored version when the incoming one is older)
// and returns the stored version. Only the version is kept: nothing reads a
// snapshot of the session.
func (m *MySQL) PutSessionState(ctx context.Context, in wire.ReportSessionStateInput) (int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return 0, err
	}
	_, err = m.DB.ExecContext(ctx, `
INSERT INTO agent_sessions (tenant_id, room_id, agent_session_id, message_version)
VALUES (?, ?, ?, ?)
ON DUPLICATE KEY UPDATE message_version = GREATEST(message_version, VALUES(message_version))`,
		tnt, in.WorkspaceID, in.AgentSessionID, in.MessageVersion)
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
//
// Concurrent reports for one session serialize on its sync_states row (read
// FOR UPDATE), and a message counts as fresh only when its own insert stored
// it: two racing reporters can never both publish the same reply.
func (m *MySQL) AppendMessages(ctx context.Context, roomID string, in wire.ReportMessagesInput) ([]wire.Message, int64, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, 0, err
	}
	// The cursor row must exist before it can be locked. This insert runs on
	// its own (autocommit): inside the transaction its shared lock on an
	// existing row would deadlock two reporters upgrading to FOR UPDATE.
	if _, err := m.DB.ExecContext(ctx, `
INSERT IGNORE INTO sync_states (tenant_id, room_id, agent_session_id, message_cursor) VALUES (?, ?, ?, 0)`,
		tnt, roomID, in.AgentSessionID); err != nil {
		return nil, 0, err
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var prior int64
	if err := tx.QueryRowContext(ctx, `
SELECT message_cursor FROM sync_states
WHERE tenant_id = ? AND room_id = ? AND agent_session_id = ? FOR UPDATE`,
		tnt, roomID, in.AgentSessionID).Scan(&prior); err != nil {
		return nil, 0, err
	}
	cursor := prior
	var fresh []wire.Message
	for _, msg := range in.Messages {
		if msg.Seq <= prior {
			continue // replay at or below the durable cursor
		}
		payload, err := jsonArg(msg.Payload)
		if err != nil {
			return nil, 0, err
		}
		// Reported usage is normalized once, here, into typed columns; the
		// budget reads sum those (AgentUsage), never the raw payload.
		u, reported := ReplyUsageOf(msg.Payload)
		var cost any
		if u.CostUsd != nil {
			cost = *u.CostUsd
		}
		res, err := tx.ExecContext(ctx, `
INSERT IGNORE INTO agent_messages (tenant_id, room_id, agent_session_id, seq, client_submit_id, payload_json, created_at_ms,
  usage_reported, usage_owner_user_id, usage_provider, usage_input_tokens, usage_output_tokens, usage_cost_usd)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tnt, roomID, in.AgentSessionID, msg.Seq, nullIfZero(msg.ClientSubmitID), payload, msg.CreatedAtUnixMs,
			reported, u.Owner, u.Provider, u.InputTokens, u.OutputTokens, cost)
		if err != nil {
			return nil, 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, 0, err
		} else if n != 1 {
			continue // already stored (a duplicate in this report): not fresh
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

// requireMatched maps an UPDATE that matched no row to notFound. The
// connection runs with clientFoundRows (mysqlConfig), so an UPDATE that
// rewrites a row with the values it already holds still counts as matched.
func requireMatched(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// rowScanner is one row to scan: a *sql.Row or the current row of *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// queryAll runs a list read and scans every row with scan. It is the one
// list-read shape: a query, scan or iteration (rows.Err) failure is returned,
// never a short or empty list. No rows is an empty, non-nil slice: a list
// that feeds JSON encodes as [], never null (the memory stores answer the
// same).
func queryAll[T any](ctx context.Context, db sqlConn, q string, scan func(rowScanner) (T, error), args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// scanString scans a single string column (a queryAll scan func).
func scanString(r rowScanner) (string, error) {
	var s string
	err := r.Scan(&s)
	return s, err
}

// compile-time assertion that MySQL satisfies ActivityStore.
var _ ActivityStore = (*MySQL)(nil)

// ErrNoDB is returned by helpers that require a configured database.
var ErrNoDB = errors.New("store: no database configured")
