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

// createSchemaTable runs schemaTableDDL(table) (idempotent: IF NOT EXISTS).
func createSchemaTable(ctx context.Context, db sqlConn, table string) error {
	ddl, err := schemaTableDDL(table)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, ddl)
	return err
}
