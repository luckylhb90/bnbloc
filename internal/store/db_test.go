package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// route and evaluateReplica are the whole routing rule; these tests pin it
// without a database.

func TestRouteDecision(t *testing.T) {
	now := time.Now()
	usable := func(checked time.Time) *replicaState {
		return &replicaState{usable: true, reason: "replica", checked: checked}
	}
	cases := []struct {
		name       string
		optIn      bool
		hasReplica bool
		st         *replicaState
		wantRep    bool
		wantReason string
	}{
		{"not opted in", false, true, usable(now), false, "not-opted-in"},
		{"no replica configured", true, false, usable(now), false, "no-replica"},
		{"never checked", true, true, nil, false, "replica-unchecked"},
		{"check gone stale", true, true, usable(now.Add(-time.Minute)), false, "replica-check-stale"},
		{"replica unusable", true, true, &replicaState{usable: false, reason: "sql thread not running", checked: now}, false, "sql thread not running"},
		{"usable replica serves opted-in read", true, true, usable(now), true, "replica"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotRep, gotReason := route(c.optIn, c.hasReplica, c.st, now, 3*time.Second)
			if gotRep != c.wantRep || gotReason != c.wantReason {
				t.Fatalf("route = (%v, %q), want (%v, %q)", gotRep, gotReason, c.wantRep, c.wantReason)
			}
		})
	}
}

func TestEvaluateReplica(t *testing.T) {
	healthy := func(lag string) map[string]string {
		return map[string]string{"Replica_IO_Running": "Yes", "Replica_SQL_Running": "Yes", "Seconds_Behind_Source": lag}
	}
	cases := []struct {
		name       string
		status     map[string]string
		wantUsable bool
		wantReason string
		wantLag    time.Duration
	}{
		{"no status row: not a replica", map[string]string{}, false, "not a replica", 0},
		{"io thread stopped", map[string]string{"Replica_IO_Running": "Connecting", "Replica_SQL_Running": "Yes"}, false, "io thread not running", 0},
		{"sql thread stopped", map[string]string{"Replica_IO_Running": "Yes", "Replica_SQL_Running": "No"}, false, "sql thread not running", 0},
		{"lag unknown (NULL)", healthy("NULL"), false, "lag unknown", 0},
		{"lag unreadable", healthy("soon"), false, "lag unreadable", 0},
		{"lag within bound", healthy("3"), true, "replica", 3 * time.Second},
		{"lag over bound", healthy("6"), false, "replication lag over bound", 6 * time.Second},
		{"older SLAVE names accepted", map[string]string{"Slave_IO_Running": "Yes", "Slave_SQL_Running": "Yes", "Seconds_Behind_Master": "1"}, true, "replica", time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			usable, reason, lag := evaluateReplica(c.status, 5*time.Second)
			if usable != c.wantUsable || reason != c.wantReason || lag != c.wantLag {
				t.Fatalf("evaluateReplica = (%v, %q, %s), want (%v, %q, %s)", usable, reason, lag, c.wantUsable, c.wantReason, c.wantLag)
			}
		})
	}
}

// Writes and transactions never leave the primary, even on an opted-in context.
// The handles are lazy (sql.Open does not dial), so the decision is observable
// by pointer identity without a server.
func TestReaderRoutesOnlyOptedInReadsToUsableReplica(t *testing.T) {
	primary, _ := sql.Open("mysql", "u:p@tcp(127.0.0.1:1)/p")
	replica, _ := sql.Open("mysql", "u:p@tcp(127.0.0.1:2)/r")
	t.Cleanup(func() { _ = primary.Close(); _ = replica.Close() })
	d := &DB{primary: primary, replica: replica, maxLag: 5 * time.Second, check: time.Second, stop: make(chan struct{})}
	d.state.Store(&replicaState{usable: true, reason: "replica", checked: time.Now()})

	opted := WithReplicaReads(context.Background())
	if d.reader(context.Background()) != primary {
		t.Fatal("a read without opt-in must use the primary")
	}
	if d.reader(opted) != replica {
		t.Fatal("an opted-in read with a usable replica must use the replica")
	}
	if got := d.Stats(); got.ReplicaReads != 1 || got.FallbackReads != 0 {
		t.Fatalf("stats after one replica read = %+v", got)
	}

	d.state.Store(&replicaState{usable: false, reason: "replication lag over bound", checked: time.Now()})
	if d.reader(opted) != primary {
		t.Fatal("an opted-in read with an unusable replica must fall back to the primary")
	}
	if got := d.Stats(); got.FallbackReads != 1 || got.Reason != "replication lag over bound" {
		t.Fatalf("stats after fallback = %+v", got)
	}
}

func TestStatsWithoutReplicaReportsNotConfigured(t *testing.T) {
	primary, _ := sql.Open("mysql", "u:p@tcp(127.0.0.1:1)/p")
	t.Cleanup(func() { _ = primary.Close() })
	d := NewDB(primary, nil, 0, 0)
	if d.reader(WithReplicaReads(context.Background())) != primary {
		t.Fatal("no replica: every read is the primary")
	}
	if got := d.Stats(); got.Configured || got.FallbackReads != 0 || got.ReplicaReads != 0 {
		t.Fatalf("stats = %+v", got)
	}
}

// The default read path (no opt-in, or no replica) must not allocate on the
// router's behalf: it is the path every existing read takes.
func TestReaderDefaultPathAllocatesNothing(t *testing.T) {
	primary, _ := sql.Open("mysql", "u:p@tcp(127.0.0.1:1)/p")
	t.Cleanup(func() { _ = primary.Close() })
	d := NewDB(primary, nil, 0, 0)
	ctx := context.Background()
	if n := testing.AllocsPerRun(1000, func() { _ = d.reader(ctx) }); n != 0 {
		t.Fatalf("default read path allocates %v per call, want 0", n)
	}
}
