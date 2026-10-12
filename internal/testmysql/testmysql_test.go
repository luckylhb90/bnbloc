package testmysql

import (
	"strings"
	"testing"
)

func TestTestDatabaseNames(t *testing.T) {
	p := prefix("bnbloc")
	if p != "bnbloc_t" {
		t.Fatalf("prefix = %q", p)
	}
	if got, ok := createdAt("bnbloc_t1760000000_0a1b2c3d", p); !ok || got != 1760000000 {
		t.Fatalf("createdAt = %d, %v", got, ok)
	}
	// Only names this package makes are ever swept.
	for _, name := range []string{"bnbloc", "bnbloc_t", "bnbloc_tx_0a1b2c3d", "bnbloc_t1760000000_zz", "bnbloc_t1760000000_0a1b2c3d4", "other_t1760000000_0a1b2c3d"} {
		if _, ok := createdAt(name, p); ok {
			t.Errorf("createdAt(%q) accepted a name testmysql never creates", name)
		}
	}
	long := strings.Repeat("b", 80)
	if n := len(prefix(long)) + 10 + 1 + 8; n > maxDBName {
		t.Fatalf("a long base makes %d-character names, over MySQL's %d", n, maxDBName)
	}
}
