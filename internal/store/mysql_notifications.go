package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLNotifications is the MySQL-backed notification prefs + outbox store.
type MySQLNotifications struct{ DB *sql.DB }

// NewMySQLNotifications builds a MySQLNotifications over a shared pool.
func NewMySQLNotifications(db *sql.DB) *MySQLNotifications { return &MySQLNotifications{DB: db} }

var (
	_ NotificationPrefsStore  = (*MySQLNotifications)(nil)
	_ NotificationOutboxStore = (*MySQLNotifications)(nil)
)

func (m *MySQLNotifications) GetNotificationPrefs(ctx context.Context, userID string) (NotificationPrefs, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return NotificationPrefs{}, false, err
	}
	var p NotificationPrefs
	var quiet string
	err = m.DB.QueryRowContext(ctx,
		`SELECT mentions, agent_finished, agent_failed, borrow_requests, content_shared, content_review, quiet_rooms, updated_at_ms
		 FROM notification_prefs WHERE tenant_id=? AND user_id=?`, tid, userID).
		Scan(&p.Mentions, &p.AgentFinished, &p.AgentFailed, &p.BorrowRequests, &p.ContentShared, &p.ContentReview, &quiet, &p.UpdatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationPrefs{}, false, nil
	}
	if err != nil {
		return NotificationPrefs{}, false, err
	}
	p.QuietRooms = []string{}
	if quiet != "" {
		if err := json.Unmarshal([]byte(quiet), &p.QuietRooms); err != nil {
			return NotificationPrefs{}, false, err
		}
	}
	return p, true, nil
}

func (m *MySQLNotifications) PutNotificationPrefs(ctx context.Context, userID string, p NotificationPrefs) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	if p.QuietRooms == nil {
		p.QuietRooms = []string{}
	}
	quiet, err := json.Marshal(p.QuietRooms)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO notification_prefs (tenant_id, user_id, mentions, agent_finished, agent_failed, borrow_requests, content_shared, content_review, quiet_rooms, updated_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE mentions=VALUES(mentions), agent_finished=VALUES(agent_finished), agent_failed=VALUES(agent_failed),
		   borrow_requests=VALUES(borrow_requests), content_shared=VALUES(content_shared), content_review=VALUES(content_review),
		   quiet_rooms=VALUES(quiet_rooms), updated_at_ms=VALUES(updated_at_ms)`,
		tid, userID, p.Mentions, p.AgentFinished, p.AgentFailed, p.BorrowRequests, p.ContentShared, p.ContentReview, string(quiet), p.UpdatedAtMs)
	return err
}

func (m *MySQLNotifications) EnqueueNotification(ctx context.Context, item NotificationOutboxItem) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := m.DB.ExecContext(ctx,
		`INSERT IGNORE INTO notification_outbox (tenant_id, id, notifier, event_json, attempts, next_attempt_at_ms, claim_token, last_error, created_at_ms)
		 VALUES (?,?,?,?,0,?,'','',?)`,
		tid, item.ID, item.Notifier, string(item.EventJSON), item.NextAttemptAtMs, item.CreatedAtMs)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ClaimNotifications leases due rows with one UPDATE (row locks make it atomic
// across replicas: two workers never claim the same row), then reads back the
// rows carrying this claim's token. The lease is the pushed-out
// next_attempt_at_ms, so no transaction is held open during HTTP delivery.
// The claim walks pending rows only (idx_notification_outbox_due leads with
// delivered_at_ms, failed_at_ms), in a total order: due time, then the key.
func (m *MySQLNotifications) ClaimNotifications(ctx context.Context, c NotificationClaim) ([]NotificationOutboxItem, error) {
	if c.Limit <= 0 {
		c.Limit = 1
	}
	if _, err := m.DB.ExecContext(ctx,
		`UPDATE notification_outbox SET claim_token=?, next_attempt_at_ms=?
		 WHERE delivered_at_ms IS NULL AND failed_at_ms IS NULL AND next_attempt_at_ms <= ?
		 ORDER BY next_attempt_at_ms, tenant_id, id, notifier LIMIT ?`,
		c.Token, c.NowMs+c.LeaseMs, c.NowMs, c.Limit); err != nil {
		return nil, err
	}
	rows, err := m.DB.QueryContext(ctx,
		`SELECT tenant_id, id, notifier, event_json, attempts, next_attempt_at_ms, last_error, created_at_ms
		 FROM notification_outbox WHERE claim_token=? AND delivered_at_ms IS NULL AND failed_at_ms IS NULL
		 ORDER BY next_attempt_at_ms, tenant_id, id, notifier`, c.Token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationOutboxItem
	for rows.Next() {
		var it NotificationOutboxItem
		var body string
		if err := rows.Scan(&it.TenantID, &it.ID, &it.Notifier, &body, &it.Attempts, &it.NextAttemptAtMs, &it.LastError, &it.CreatedAtMs); err != nil {
			return nil, err
		}
		it.EventJSON = []byte(body)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (m *MySQLNotifications) RecordNotificationAttempt(ctx context.Context, a NotificationAttempt) error {
	var (
		res sql.Result
		err error
	)
	const guard = ` WHERE tenant_id=? AND id=? AND notifier=? AND claim_token=? AND delivered_at_ms IS NULL AND failed_at_ms IS NULL`
	errText := truncateErr(a.Error)
	switch {
	case a.Outcome == NotificationDispatched && len(a.EventJSON) > 0:
		// A hand-off keeps the claim (claim_token) and counts no attempt.
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET next_attempt_at_ms=?, event_json=?, last_error=?`+guard,
			a.NextAttemptAtMs, string(a.EventJSON), errText, a.TenantID, a.ID, a.Notifier, a.Token)
	case a.Outcome == NotificationDispatched:
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET next_attempt_at_ms=?, last_error=?`+guard,
			a.NextAttemptAtMs, errText, a.TenantID, a.ID, a.Notifier, a.Token)
	case a.Outcome == NotificationDelivered:
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET attempts=attempts+1, delivered_at_ms=?, last_error=?, claim_token=''`+guard,
			a.AtMs, errText, a.TenantID, a.ID, a.Notifier, a.Token)
	case a.Outcome == NotificationFailed:
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET attempts=attempts+1, failed_at_ms=?, last_error=?, claim_token=''`+guard,
			a.AtMs, errText, a.TenantID, a.ID, a.Notifier, a.Token)
	case len(a.EventJSON) > 0:
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET attempts=attempts+1, next_attempt_at_ms=?, event_json=?, last_error=?, claim_token=''`+guard,
			a.NextAttemptAtMs, string(a.EventJSON), errText, a.TenantID, a.ID, a.Notifier, a.Token)
	default:
		res, err = m.DB.ExecContext(ctx, `UPDATE notification_outbox SET attempts=attempts+1, next_attempt_at_ms=?, last_error=?, claim_token=''`+guard,
			a.NextAttemptAtMs, errText, a.TenantID, a.ID, a.Notifier, a.Token)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationLeaseLost
	}
	return nil
}

func (m *MySQLNotifications) PruneNotifications(ctx context.Context, beforeMs int64, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	res, err := m.DB.ExecContext(ctx,
		`DELETE FROM notification_outbox
		 WHERE created_at_ms < ? AND (delivered_at_ms IS NOT NULL OR failed_at_ms IS NOT NULL)
		 LIMIT ?`, beforeMs, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
