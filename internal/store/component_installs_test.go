package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryComponentInstallPutIsIdempotent_whenSubjectInstallsSameComponentTwice(t *testing.T) {
	// Given
	s := NewMemoryComponentInstall()
	ctx := context.Background()

	// When
	if err := s.PutComponentInstall(ctx, "alice", "core", "acme", 100); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := s.PutComponentInstall(ctx, "alice", "core", "acme", 200); err != nil {
		t.Fatalf("second put: %v", err)
	}

	// Then
	installs, err := s.ListComponentInstalls(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installs) != 1 {
		t.Fatalf("installs = %+v; want exactly one row", installs)
	}
	if installs[0].InstalledAtMs != 100 {
		t.Fatalf("installed_at_ms = %d; want original timestamp for idempotent install", installs[0].InstalledAtMs)
	}
}

func TestMemoryComponentInstallDeleteIsIdempotent_whenInstallIsAbsent(t *testing.T) {
	// Given
	s := NewMemoryComponentInstall()
	ctx := context.Background()
	if err := s.PutComponentInstall(ctx, "alice", "core", "acme", 100); err != nil {
		t.Fatalf("put: %v", err)
	}

	// When
	if err := s.DeleteComponentInstall(ctx, "alice", "core", "acme"); err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	if err := s.DeleteComponentInstall(ctx, "alice", "core", "acme"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}

	// Then
	installs, err := s.ListComponentInstalls(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(installs) != 0 {
		t.Fatalf("installs = %+v; want empty after delete", installs)
	}
}

func TestMemoryComponentInstallCountDistinctSubjectsByKind(t *testing.T) {
	// Given
	s := NewMemoryComponentInstall()
	ctx := context.Background()
	for _, install := range []ComponentInstall{
		{Subject: "alice", Kind: "core", Name: "acme", InstalledAtMs: 100},
		{Subject: "bob", Kind: "core", Name: "acme", InstalledAtMs: 101},
		{Subject: "alice", Kind: "core", Name: "orbit", InstalledAtMs: 102},
		{Subject: "alice", Kind: "plugin", Name: "auditor", InstalledAtMs: 103},
	} {
		if err := s.PutComponentInstall(ctx, install.Subject, install.Kind, install.Name, install.InstalledAtMs); err != nil {
			t.Fatalf("put %+v: %v", install, err)
		}
	}
	if err := s.PutComponentInstall(ctx, "alice", "core", "acme", 999); err != nil {
		t.Fatalf("repeat put: %v", err)
	}

	// When
	counts, err := s.CountComponentInstalls(ctx, "core")

	// Then
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["acme"] != 2 || counts["orbit"] != 1 || counts["auditor"] != 0 {
		t.Fatalf("counts = %+v; want distinct core subjects only", counts)
	}
}

func TestComponentInstallsSchemaAndMigrationDeclareTable(t *testing.T) {
	// Given
	const tableDDL = "CREATE TABLE IF NOT EXISTS component_installs"

	// When
	internalSchemaHasTable := strings.Contains(schemaSQL, tableDDL)
	deploySchemaHasTable := strings.Contains(readDeploySchemaForTest(t), tableDDL)
	migrationFound := false
	for _, m := range migrations {
		if m.id == "2026_09_component_installs" {
			migrationFound = true
		}
	}

	// Then
	if !internalSchemaHasTable {
		t.Fatal("internal schema must create component_installs for fresh databases")
	}
	if !deploySchemaHasTable {
		t.Fatal("deploy schema must create component_installs for fresh databases")
	}
	if !migrationFound {
		t.Fatal("migration ledger must include 2026_09_component_installs")
	}
}

func readDeploySchemaForTest(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "schema.sql"))
	if err != nil {
		t.Fatalf("read deploy schema: %v", err)
	}
	return string(raw)
}
