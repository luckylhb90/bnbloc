package store

import (
	"context"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

func TestScopedLockName(t *testing.T) {
	if got := scopedLockName("bnbloc_retention", "bnbloc"); got != "bnbloc_retention@bnbloc" {
		t.Fatalf("scoped = %q", got)
	}
	if got := scopedLockName("bnbloc_retention", ""); got != "bnbloc_retention" {
		t.Fatalf("no database = %q", got)
	}
	a := scopedLockName(migrationLockName, strings.Repeat("a", 64))
	b := scopedLockName(migrationLockName, strings.Repeat("a", 63)+"b")
	if len(a) > maxLockName || len(b) > maxLockName || a == b {
		t.Fatalf("long database names must stay distinct within %d characters: %q, %q", maxLockName, a, b)
	}
}

// An advisory lock guards one database: a retention pass (or a migration
// run) on one database never waits on, or skips for, another database on the
// same server, while a second taker on the same database is refused.
func TestAdvisoryLocksScopedByDatabase(t *testing.T) {
	ctx := context.Background()
	open := func() *MySQLRetention {
		db, err := OpenMySQL(testmysql.Fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return NewMySQLRetention(NewDB(db, nil, 0, 0))
	}
	a, b := open(), open()
	releaseA, ok, err := a.TryRetentionLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock on database a = %v, %v", ok, err)
	}
	defer releaseA()
	if _, ok, err := a.TryRetentionLock(ctx); err != nil || ok {
		t.Fatalf("a second lock on database a = %v, %v; want refused", ok, err)
	}
	releaseB, ok, err := b.TryRetentionLock(ctx)
	if err != nil || !ok {
		t.Fatalf("lock on database b while a holds its own = %v, %v; want taken", ok, err)
	}
	releaseB()
}
