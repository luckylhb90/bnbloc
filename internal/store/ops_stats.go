package store

import (
	"context"
	"database/sql"
)

// Operational aggregates read by the services' metrics samplers (O2). Each
// is a cross-tenant snapshot of the shared database, so every replica of a
// service reports the same value (alert on max(), not sum()).

// NotificationOutboxStats is the outbox state at one instant. Pending items
// split into Queued (never attempted) and Retrying (attempted, due again);
// Delivered and Failed count the finished items still retained (finished
// items are pruned after the worker's retention window).
type NotificationOutboxStats struct {
	Queued    int64
	Retrying  int64
	Delivered int64
	Failed    int64
	// OldestPendingCreatedAtMs is the creation time of the oldest pending
	// item, 0 when nothing is pending.
	OldestPendingCreatedAtMs int64
}

// NotificationOutboxStats aggregates every tenant's outbox.
func (m *MemoryNotifications) NotificationOutboxStats(_ context.Context) (NotificationOutboxStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var st NotificationOutboxStats
	for _, row := range m.outbox {
		it := row.item
		switch {
		case it.DeliveredAtMs != 0:
			st.Delivered++
		case it.FailedAtMs != 0:
			st.Failed++
		default:
			if it.Attempts == 0 {
				st.Queued++
			} else {
				st.Retrying++
			}
			if st.OldestPendingCreatedAtMs == 0 || it.CreatedAtMs < st.OldestPendingCreatedAtMs {
				st.OldestPendingCreatedAtMs = it.CreatedAtMs
			}
		}
	}
	return st, nil
}

// NotificationOutboxStats aggregates every tenant's outbox in one scan (the
// table is bounded by the worker's prune of finished items).
func (m *MySQLNotifications) NotificationOutboxStats(ctx context.Context) (NotificationOutboxStats, error) {
	var st NotificationOutboxStats
	var oldest sql.NullInt64
	err := m.DB.QueryRowContext(ctx, `SELECT
    COALESCE(SUM(delivered_at_ms IS NULL AND failed_at_ms IS NULL AND attempts = 0), 0),
    COALESCE(SUM(delivered_at_ms IS NULL AND failed_at_ms IS NULL AND attempts > 0), 0),
    COALESCE(SUM(delivered_at_ms IS NOT NULL), 0),
    COALESCE(SUM(delivered_at_ms IS NULL AND failed_at_ms IS NOT NULL), 0),
    MIN(CASE WHEN delivered_at_ms IS NULL AND failed_at_ms IS NULL THEN created_at_ms END)
  FROM notification_outbox`).Scan(&st.Queued, &st.Retrying, &st.Delivered, &st.Failed, &oldest)
	if err != nil {
		return NotificationOutboxStats{}, err
	}
	st.OldestPendingCreatedAtMs = oldest.Int64
	return st, nil
}

// CountOnlineBridges counts bridges (every tenant) live at nowMs
// (BridgeLive) — the same liveness rule the team dashboard and job routing
// apply.
func (s *MemoryBridgeJobs) CountOnlineBridges(_ context.Context, nowMs int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, hb := range s.heartbeats {
		if BridgeLive(hb, nowMs) {
			n++
		}
	}
	return n, nil
}

// CountOnlineBridges counts bridges (every tenant) live at nowMs.
func (m *MySQLBridgeJobs) CountOnlineBridges(ctx context.Context, nowMs int64) (int, error) {
	// cross-tenant: a metrics snapshot of every tenant's bridges.
	var n int
	err := m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_heartbeats h WHERE `+liveBridgePred, bridgeLiveSince(nowMs)).Scan(&n)
	return n, err
}
