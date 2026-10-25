package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// MySQLUsageBudgets is the MySQL UsageBudgetStore (table usage_budgets).
type MySQLUsageBudgets struct{ DB *DB }

// NewMySQLUsageBudgets returns a MySQL-backed budget store.
func NewMySQLUsageBudgets(db *DB) *MySQLUsageBudgets { return &MySQLUsageBudgets{DB: db} }

var _ UsageBudgetStore = (*MySQLUsageBudgets)(nil)

const budgetColumns = `user_id, tokens, cost, warn_at_percent, hard_stop, updated_by, updated_at_ms`

func scanBudget(scan func(...any) error) (UsageBudget, error) {
	var b UsageBudget
	var tokens sql.NullInt64
	var cost sql.NullFloat64
	if err := scan(&b.UserID, &tokens, &cost, &b.WarnAtPercent, &b.HardStop, &b.UpdatedBy, &b.UpdatedAtMs); err != nil {
		return UsageBudget{}, err
	}
	if tokens.Valid {
		v := tokens.Int64
		b.Tokens = &v
	}
	if cost.Valid {
		v := cost.Float64
		b.Cost = &v
	}
	return b, nil
}

// ListUsageBudgets implements UsageBudgetStore.
func (s *MySQLUsageBudgets) ListUsageBudgets(ctx context.Context) ([]UsageBudget, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, s.DB, `SELECT `+budgetColumns+` FROM usage_budgets WHERE tenant_id = ? ORDER BY user_id`,
		func(r rowScanner) (UsageBudget, error) { return scanBudget(r.Scan) }, tnt)
}

// GetUsageBudget implements UsageBudgetStore.
func (s *MySQLUsageBudgets) GetUsageBudget(ctx context.Context, userID string) (UsageBudget, bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return UsageBudget{}, false, err
	}
	b, err := scanBudget(s.DB.QueryRowContext(ctx,
		`SELECT `+budgetColumns+` FROM usage_budgets WHERE tenant_id = ? AND user_id = ?`, tnt, userID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageBudget{}, false, nil
	}
	if err != nil {
		return UsageBudget{}, false, err
	}
	return b, true, nil
}

// ReplaceUsageBudgets implements UsageBudgetStore in one transaction. The
// upsert resets the warning state only when the limits changed (assignments
// run left to right, so the warned_* expressions read the old limits).
func (s *MySQLUsageBudgets) ReplaceUsageBudgets(ctx context.Context, budgets []UsageBudget) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	keep := make([]any, 0, len(budgets)+1)
	keep = append(keep, tnt)
	for _, b := range budgets {
		keep = append(keep, b.UserID)
	}
	del := `DELETE FROM usage_budgets WHERE tenant_id = ?`
	if len(budgets) > 0 {
		del += ` AND user_id NOT IN (?` + strings.Repeat(",?", len(budgets)-1) + `)`
	}
	if _, err := tx.ExecContext(ctx, del, keep...); err != nil {
		return err
	}
	for _, b := range budgets {
		b = b.normalized()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO usage_budgets
			 (tenant_id, user_id, tokens, cost, warn_at_percent, hard_stop, updated_by, updated_at_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON DUPLICATE KEY UPDATE
			  warned_level = IF(tokens <=> VALUES(tokens) AND cost <=> VALUES(cost) AND warn_at_percent = VALUES(warn_at_percent), warned_level, 0),
			  warned_period = IF(warned_level = 0, '', warned_period),
			  tokens = VALUES(tokens), cost = VALUES(cost), warn_at_percent = VALUES(warn_at_percent),
			  hard_stop = VALUES(hard_stop), updated_by = VALUES(updated_by), updated_at_ms = VALUES(updated_at_ms)`,
			tnt, b.UserID, nullPtr(b.Tokens), nullPtr(b.Cost), b.WarnAtPercent, b.HardStop, b.UpdatedBy, b.UpdatedAtMs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimBudgetWarning implements UsageBudgetStore with one conditional UPDATE,
// so concurrent evaluators (activity replicas) send each warning once.
func (s *MySQLUsageBudgets) ClaimBudgetWarning(ctx context.Context, userID, period string, level int) (bool, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	res, err := s.DB.ExecContext(ctx,
		`UPDATE usage_budgets SET warned_period = ?, warned_level = ?
		 WHERE tenant_id = ? AND user_id = ? AND (warned_period <> ? OR warned_level < ?)`,
		period, level, tnt, userID, period, level)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
