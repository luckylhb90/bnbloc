package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Installs on both backends: a put is idempotent (the first install time
// stays), a delete of a missing row is a no-op, a listing is by kind then
// name ([] when none), counts are distinct subjects per name of one kind,
// and subjects are exact (case-variant ids are distinct).
func TestContractComponentInstallStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.ComponentInstalls
		ctx := context.Background()
		alice, bob := uniq("alice-"), uniq("bob-")
		kind := uniq("kind-")
		for _, in := range []ComponentInstall{
			{Subject: alice, Kind: kind, Name: "orbit", InstalledAtMs: 102},
			{Subject: alice, Kind: kind, Name: "acme", InstalledAtMs: 100},
			{Subject: bob, Kind: kind, Name: "acme", InstalledAtMs: 101},
			{Subject: alice, Kind: kind + "-plugin", Name: "auditor", InstalledAtMs: 103},
		} {
			if err := s.PutComponentInstall(ctx, in.Subject, in.Kind, in.Name, in.InstalledAtMs); err != nil {
				t.Fatalf("put %+v: %v", in, err)
			}
		}
		if err := s.PutComponentInstall(ctx, alice, kind, "acme", 999); err != nil {
			t.Fatalf("repeat put: %v", err)
		}
		got := must(s.ListComponentInstalls(ctx, alice))(t)
		if len(got) != 3 || got[0].Kind != kind || got[0].Name != "acme" || got[0].InstalledAtMs != 100 || got[1].Name != "orbit" || got[2].Kind != kind+"-plugin" {
			t.Fatalf("alice's installs = %+v; want kind, name order and the first install time", got)
		}
		counts := must(s.CountComponentInstalls(ctx, kind))(t)
		if len(counts) != 2 || counts["acme"] != 2 || counts["orbit"] != 1 {
			t.Fatalf("counts = %+v; want distinct subjects per name of the kind", counts)
		}
		if none := must(s.ListComponentInstalls(ctx, strings.ToUpper(alice)))(t); none == nil || len(none) != 0 {
			t.Fatalf("a case-variant subject's installs = %#v, want []", none)
		}
		for i := 0; i < 2; i++ {
			if err := s.DeleteComponentInstall(ctx, alice, kind, "acme"); err != nil {
				t.Fatalf("delete #%d: %v", i, err)
			}
		}
		if got := must(s.ListComponentInstalls(ctx, alice))(t); len(got) != 2 || got[0].Name != "orbit" {
			t.Fatalf("after delete = %+v", got)
		}
	})
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
