package store

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/luckylhb90/bnbloc/internal/testmysql"
)

// Routing against a real primary and a real replica. Gated on
// BNBLOC_TEST_MYSQL_REPLICA_ADDR (host:port of a replica of the server named by
// BNBLOC_TEST_MYSQL_DSN, same credentials). The replica's apply delay is set
// with SOURCE_DELAY, so a stale read and a fallback can be observed directly.
// docs/replication.md has the recipe for the local pair.

const (
	testReplicaBound = 4 * time.Second
	testReplicaCheck = 300 * time.Millisecond
	testShortDelay   = 2 // seconds: within the bound
	testLongDelay    = 7 // seconds: over the bound
)

type replicaFixture struct {
	primary      *DB
	replicaAdmin *sql.DB // root on the replica, to set the apply delay
	replicaDSN   string
	primaryPool  *sql.DB
}

func newReplicaFixture(t *testing.T) *replicaFixture {
	t.Helper()
	addr := os.Getenv("BNBLOC_TEST_MYSQL_REPLICA_ADDR")
	if addr == "" {
		t.Skip("BNBLOC_TEST_MYSQL_REPLICA_ADDR not set")
	}
	primaryDSN := testmysql.Fresh(t)
	cfg, err := mysql.ParseDSN(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	repCfg := *cfg
	repCfg.Addr = addr
	replicaDSN := repCfg.FormatDSN()

	primary, err := OpenMySQL(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	replicaPool, err := openReplica(replicaDSN)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", replicaDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = setApplyDelay(admin, 0)
		_ = admin.Close()
	})
	d := NewDB(primary, replicaPool, testReplicaBound, testReplicaCheck)
	t.Cleanup(func() { _ = d.Close() })
	return &replicaFixture{primary: d, replicaAdmin: admin, replicaDSN: replicaDSN, primaryPool: primary}
}

// setApplyDelay makes the replica apply events n seconds late (0 = none).
func setApplyDelay(admin *sql.DB, n int) error {
	for _, q := range []string{
		"STOP REPLICA SQL_THREAD",
		"CHANGE REPLICATION SOURCE TO SOURCE_DELAY = " + strconv.Itoa(n),
		"START REPLICA SQL_THREAD",
	} {
		if _, err := admin.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func countRows(t *testing.T, ctx context.Context, q queryRower) int {
	t.Helper()
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM repl_probe").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// queryRower is the read surface both *DB and *sql.DB have.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func TestReplicaServesBoundedStaleOptInReads(t *testing.T) {
	f := newReplicaFixture(t)
	ctx := context.Background()
	opted := WithReplicaReads(ctx)
	if _, err := f.primary.ExecContext(ctx, "CREATE TABLE repl_probe (id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "table on replica", 20*time.Second, func() bool {
		var n int
		return f.replicaAdmin.QueryRow("SELECT COUNT(*) FROM repl_probe").Scan(&n) == nil
	})
	if err := setApplyDelay(f.replicaAdmin, testShortDelay); err != nil {
		t.Fatal(err)
	}
	// Once a row written under the delay is visible on the replica, the replica
	// reports its lag and the health check marks it usable.
	f.primary.ExecContext(ctx, "INSERT INTO repl_probe VALUES (1)")
	waitFor(t, "replica usable within the bound", 30*time.Second, func() bool {
		return f.primary.Stats().Usable && countRows(t, opted, f.primary) == 1
	})
	before := countRows(t, ctx, f.primary)

	f.primary.ExecContext(ctx, "INSERT INTO repl_probe VALUES (2)")
	if got := countRows(t, ctx, f.primary); got != before+1 {
		t.Fatalf("default read must see the primary's new row: got %d", got)
	}
	if got := countRows(t, opted, f.primary); got != before {
		t.Fatalf("opted-in read inside the bound must be served by the replica (stale by the delay): got %d, want %d", got, before)
	}
	waitFor(t, "replica catches up", 20*time.Second, func() bool {
		return countRows(t, opted, f.primary) == before+1
	})
	if s := f.primary.Stats(); s.ReplicaReads == 0 {
		t.Fatalf("expected replica reads, stats = %+v", s)
	}
}

func TestReplicaOverBoundFallsBackToPrimary(t *testing.T) {
	f := newReplicaFixture(t)
	ctx := context.Background()
	opted := WithReplicaReads(ctx)
	if _, err := f.primary.ExecContext(ctx, "CREATE TABLE repl_probe (id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "table on replica", 20*time.Second, func() bool {
		var n int
		return f.replicaAdmin.QueryRow("SELECT COUNT(*) FROM repl_probe").Scan(&n) == nil
	})
	if err := setApplyDelay(f.replicaAdmin, testLongDelay); err != nil {
		t.Fatal(err)
	}
	f.primary.ExecContext(ctx, "INSERT INTO repl_probe VALUES (1)")
	waitFor(t, "replica over the bound", 30*time.Second, func() bool {
		return f.primary.Stats().Reason == "replication lag over bound"
	})
	fallbacks := f.primary.Stats().FallbackReads
	f.primary.ExecContext(ctx, "INSERT INTO repl_probe VALUES (2)")
	if got, want := countRows(t, opted, f.primary), countRows(t, ctx, f.primary); got != want {
		t.Fatalf("over the bound an opted-in read must see the primary: got %d, want %d", got, want)
	}
	if s := f.primary.Stats(); s.FallbackReads != fallbacks+1 || s.Usable {
		t.Fatalf("fallback not counted or replica still usable: %+v", s)
	}
	if err := setApplyDelay(f.replicaAdmin, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replica usable again", 30*time.Second, func() bool {
		return f.primary.Stats().Usable
	})
}

func TestWritesAndTransactionsStayOnPrimaryEvenWhenOptedIn(t *testing.T) {
	f := newReplicaFixture(t)
	ctx := context.Background()
	opted := WithReplicaReads(ctx)
	if _, err := f.primary.ExecContext(opted, "CREATE TABLE repl_probe (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("an opted-in context must not change where DDL goes: %v", err)
	}
	if _, err := f.primary.ExecContext(opted, "INSERT INTO repl_probe VALUES (1)"); err != nil {
		t.Fatalf("an opted-in write must reach the primary (the replica is read-only): %v", err)
	}
	tx, err := f.primary.BeginTx(opted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(opted, "INSERT INTO repl_probe VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, ctx, f.primary); got != 2 {
		t.Fatalf("primary rows = %d, want 2", got)
	}
}

func TestUnreachableReplicaFallsBackAndReports(t *testing.T) {
	primaryDSN := testmysql.Fresh(t)
	cfg, err := mysql.ParseDSN(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := OpenMySQL(primaryDSN)
	if err != nil {
		t.Fatal(err)
	}
	deadCfg := *cfg
	deadCfg.Addr = "127.0.0.1:1" // nothing listens here
	dead, err := openReplica(deadCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDB(primary, dead, testReplicaBound, testReplicaCheck)
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, "CREATE TABLE repl_probe (id INT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "check to report the dead replica", 20*time.Second, func() bool {
		return d.Stats().Reason == "check failed"
	})
	if got := countRows(t, WithReplicaReads(ctx), d); got != 0 {
		t.Fatalf("opted-in read with a dead replica must return the primary's data: got %d", got)
	}
	if s := d.Stats(); s.FallbackReads == 0 || s.Usable {
		t.Fatalf("stats = %+v", s)
	}
}
