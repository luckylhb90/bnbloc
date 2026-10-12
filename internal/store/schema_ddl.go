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
// longer holds). The locked migration that created one still runs on a
// fresh database (the ledger replays every migration in order): creating a
// retired table is skipped there, and the dropping migration removes it
// from databases that have it.
var retiredTables = map[string]string{
	"prompts": "2026_10_prompt_packs",
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
