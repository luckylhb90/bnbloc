package store

import (
	"context"
	"fmt"
	"strings"
)

// schemaTableDDL returns the CREATE TABLE statement for table from the
// embedded schema.sql, so a migration that introduces a table runs the exact
// DDL a fresh database gets (one definition, no hand-copied twin to drift).
func schemaTableDDL(table string) (string, error) {
	head := "CREATE TABLE IF NOT EXISTS " + table + " ("
	start := strings.Index(schemaSQL, head)
	if start < 0 {
		return "", fmt.Errorf("schema.sql has no table %s", table)
	}
	end := strings.Index(schemaSQL[start:], ";")
	if end < 0 {
		return "", fmt.Errorf("schema.sql table %s is unterminated", table)
	}
	return schemaSQL[start : start+end], nil
}

// retiredTables are tables a later migration drops (and schema.sql no
// longer holds). A drop lands only in a release after the one that stopped
// using the table or column (TestMigrationsAreDeploySafe). The locked migration that created one still runs on a
// fresh database (the ledger replays every migration in order): creating a
// retired table is skipped there, and the dropping migration removes it
// from databases that have it.
var retiredTables = map[string]string{
	"prompts":             "2026_10_prompt_packs",
	"shared_agent_leases": "2026_10_drop_legacy_borrow_and_submit",
}

// retiredColumns are columns ("table.column") a later migration drops (and
// schema.sql no longer holds), with the dropping migration's id. A locked
// migration that adds, collates or indexes one still runs on a fresh
// database: the migration helpers skip a retired column there (and an index
// over one), and the dropping migration removes it from databases that
// have it.
var retiredColumns = map[string]string{
	"sync_states.pending_count":          "2026_10_drop_unused_columns",
	"sync_states.failed_count":           "2026_10_drop_unused_columns",
	"sync_states.last_error":             "2026_10_drop_unused_columns",
	"agent_messages.channel_id":          "2026_10_drop_unused_columns",
	"agent_sessions.provider_session_id": "2026_10_drop_unused_columns",
	"agent_sessions.owner_user_id":       "2026_10_drop_unused_columns",
	"agent_sessions.state_json":          "2026_10_drop_unused_columns",
	"agent_sessions.updated_at_ms":       "2026_10_drop_unused_columns",
	"agent_messages.client_submit_id":    "2026_10_drop_legacy_borrow_and_submit",
	"tenants.public_shares_disabled":     "2026_10_share_link_access",
}

// retiredColumn reports whether table.column is in retiredColumns, or its
// table in retiredTables: a locked migration never alters either on a
// fresh database.
func retiredColumn(table, column string) bool {
	if _, ok := retiredTables[table]; ok {
		return true
	}
	_, ok := retiredColumns[table+"."+column]
	return ok
}

// createSchemaTable runs schemaTableDDL(table) (idempotent: IF NOT EXISTS);
// a retired table is not created.
func createSchemaTable(ctx context.Context, db sqlConn, table string) error {
	if _, retired := retiredTables[table]; retired {
		return nil
	}
	ddl, err := schemaTableDDL(table)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, ddl)
	return err
}
