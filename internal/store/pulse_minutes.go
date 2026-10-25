package store

import (
	"context"
	"fmt"
	"strings"
)

// PulseMinuteDelta is one minute's increment to one metric and dimension of a
// tenant's board rollup (docs/prd/2026-10-24-executive-pulse-backend.md, section 2).
type PulseMinuteDelta struct {
	Tenant   string
	MinuteMs int64
	Metric   string
	Dim      string
	Value    float64
}

// PulseMinutes is the organization board's rollup store. It holds counts and
// sums only: no member id and no per-person row.
type PulseMinutes interface {
	// AddPulseMinutes adds the deltas in one transaction: all of them land, or
	// none does.
	AddPulseMinutes(ctx context.Context, rows []PulseMinuteDelta, nowMs int64) error
	// PulseTotals sums each metric over [fromMs, toMs) for one tenant.
	PulseTotals(ctx context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]float64, error)
	// PulseBreakdown sums each metric by dimension over [fromMs, toMs): metric ->
	// dimension -> value (for example replies by core).
	PulseBreakdown(ctx context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]map[string]float64, error)
	// PulseFreshness is when the tenant's rollup was last written (unix ms), 0 if never.
	PulseFreshness(ctx context.Context, tenant string) (int64, error)
	// PrunePulseMinutes deletes rows older than beforeMs, a bounded batch at a time.
	PrunePulseMinutes(ctx context.Context, beforeMs int64) (int64, error)
}

// pulseWriteChunk bounds one INSERT statement (rows x 6 placeholders).
const pulseWriteChunk = 500

// pulsePruneChunk bounds one DELETE statement.
const pulsePruneChunk = 5000

// MySQLPulseMinutes is the MySQL rollup store.
type MySQLPulseMinutes struct{ DB *DB }

// NewMySQLPulseMinutes returns the MySQL rollup store over db.
func NewMySQLPulseMinutes(db *DB) *MySQLPulseMinutes { return &MySQLPulseMinutes{DB: db} }

func (m *MySQLPulseMinutes) AddPulseMinutes(ctx context.Context, rows []PulseMinuteDelta, nowMs int64) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for start := 0; start < len(rows); start += pulseWriteChunk {
		chunk := rows[start:min(start+pulseWriteChunk, len(rows))]
		var b strings.Builder
		args := make([]any, 0, len(chunk)*6)
		b.WriteString("INSERT INTO pulse_minutes (tenant_id, minute_ms, metric, dim, value, updated_at_ms) VALUES ")
		for i, r := range chunk {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("(?,?,?,?,?,?)")
			args = append(args, r.Tenant, r.MinuteMs, r.Metric, r.Dim, r.Value, nowMs)
		}
		b.WriteString(" ON DUPLICATE KEY UPDATE value = value + VALUES(value), updated_at_ms = VALUES(updated_at_ms)")
		if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("pulse minutes: %w", err)
		}
	}
	return tx.Commit()
}

func (m *MySQLPulseMinutes) PulseTotals(ctx context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(metrics) == 0 {
		return out, nil
	}
	args := []any{tenant, fromMs, toMs}
	marks := make([]string, len(metrics))
	for i, mt := range metrics {
		marks[i] = "?"
		args = append(args, mt)
	}
	q := `SELECT metric, SUM(value) FROM pulse_minutes
WHERE tenant_id = ? AND minute_ms >= ? AND minute_ms < ? AND metric IN (` + strings.Join(marks, ",") + `)
GROUP BY metric`
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var metric string
		var sum float64
		if err := rows.Scan(&metric, &sum); err != nil {
			return nil, err
		}
		out[metric] = sum
	}
	return out, rows.Err()
}

func (m *MySQLPulseMinutes) PulseBreakdown(ctx context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]map[string]float64, error) {
	out := map[string]map[string]float64{}
	if len(metrics) == 0 {
		return out, nil
	}
	args := []any{tenant, fromMs, toMs}
	marks := make([]string, len(metrics))
	for i, mt := range metrics {
		marks[i] = "?"
		args = append(args, mt)
	}
	q := `SELECT metric, dim, SUM(value) FROM pulse_minutes
WHERE tenant_id = ? AND minute_ms >= ? AND minute_ms < ? AND metric IN (` + strings.Join(marks, ",") + `)
GROUP BY metric, dim`
	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var metric, dim string
		var sum float64
		if err := rows.Scan(&metric, &dim, &sum); err != nil {
			return nil, err
		}
		if out[metric] == nil {
			out[metric] = map[string]float64{}
		}
		out[metric][dim] = sum
	}
	return out, rows.Err()
}

func (m *MySQLPulseMinutes) PulseFreshness(ctx context.Context, tenant string) (int64, error) {
	var at int64
	err := m.DB.QueryRowContext(ctx, "SELECT COALESCE(MAX(updated_at_ms), 0) FROM pulse_minutes WHERE tenant_id = ?", tenant).Scan(&at)
	return at, err
}

func (m *MySQLPulseMinutes) PrunePulseMinutes(ctx context.Context, beforeMs int64) (int64, error) {
	// cross-tenant: retention is by age alike for every tenant; the bound is time, not tenant.
	var total int64
	for {
		res, err := m.DB.ExecContext(ctx, "DELETE FROM pulse_minutes WHERE minute_ms < ? LIMIT ?", beforeMs, pulsePruneChunk)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < pulsePruneChunk || ctx.Err() != nil {
			return total, nil
		}
	}
}
