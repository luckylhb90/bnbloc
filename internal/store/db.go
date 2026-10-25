package store

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// DB is the store's database handle. It replaces a bare *sql.DB in every store
// and has the methods the stores call, so only the field types change.
//
// Writes, transactions, connections (advisory locks) and the schema always go to
// the primary. A read goes to the read replica only when its context was marked
// with WithReplicaReads, which a read-only handler does explicitly and whose data
// may lag by up to the replica's bound. Every other read stays on the primary,
// so read-your-writes cannot break anywhere that did not opt in. See
// docs/prd/2026-10-24-read-replica-routing.md.
type DB struct {
	primary *sql.DB
	replica *sql.DB // nil = no replica configured
	maxLag  time.Duration
	check   time.Duration

	state atomic.Pointer[replicaState] // nil until the first check

	replicaReads  atomic.Int64 // opted-in reads served by the replica
	fallbackReads atomic.Int64 // opted-in reads the primary served because the replica was not usable

	stop     chan struct{}
	stopOnce sync.Once
}

// replicaState is the result of the last health check.
type replicaState struct {
	usable  bool
	reason  string // "replica" when usable, otherwise why it is not
	lag     time.Duration
	checked time.Time
}

// ReplicaStats is what an operator reads to see the replica's role right now.
type ReplicaStats struct {
	Configured    bool
	Usable        bool
	Reason        string
	Lag           time.Duration
	LastCheck     time.Time
	ReplicaReads  int64
	FallbackReads int64
}

// Defaults for the replica settings (environment: BNBLOC_MYSQL_REPLICA_*).
const (
	defaultReplicaMaxLag = 5 * time.Second
	defaultReplicaCheck  = 2 * time.Second
	defaultReplicaConns  = 20
)

// NewDB wraps the primary pool and, if non-nil, the replica pool. With a replica
// it starts the health checker; call Close to stop it.
func NewDB(primary, replica *sql.DB, maxLag, check time.Duration) *DB {
	if maxLag <= 0 {
		maxLag = defaultReplicaMaxLag
	}
	if check <= 0 {
		check = defaultReplicaCheck
	}
	d := &DB{primary: primary, replica: replica, maxLag: maxLag, check: check, stop: make(chan struct{})}
	if replica != nil {
		go d.watch()
	}
	return d
}

// WithReplicaReads marks ctx as a read-only, lag-tolerant read: a read on that
// context may be served by the replica when the replica is usable. Writes and
// transactions on the same context still go to the primary.
func WithReplicaReads(ctx context.Context) context.Context {
	return context.WithValue(ctx, replicaReadsKey{}, true)
}

type replicaReadsKey struct{}

func replicaReadsOK(ctx context.Context) bool {
	v, _ := ctx.Value(replicaReadsKey{}).(bool)
	return v
}

// route decides where one opted-in read goes and why. It is pure: the health
// state and the clock are inputs, so the rule is tested without a database.
func route(optIn, hasReplica bool, st *replicaState, now time.Time, staleAfter time.Duration) (replica bool, reason string) {
	switch {
	case !optIn:
		return false, "not-opted-in"
	case !hasReplica:
		return false, "no-replica"
	case st == nil:
		return false, "replica-unchecked"
	case now.Sub(st.checked) > staleAfter:
		return false, "replica-check-stale"
	case !st.usable:
		return false, st.reason
	}
	return true, "replica"
}

// evaluateReplica reads one SHOW REPLICA STATUS row (keys as MySQL names them;
// the older SLAVE names are accepted too) and says whether the replica may serve
// reads. Lag is in whole seconds, as MySQL reports it.
func evaluateReplica(status map[string]string, maxLag time.Duration) (usable bool, reason string, lag time.Duration) {
	io := pick(status, "Replica_IO_Running", "Slave_IO_Running")
	sqlThread := pick(status, "Replica_SQL_Running", "Slave_SQL_Running")
	if io == "" && sqlThread == "" {
		return false, "not a replica", 0
	}
	if io != "Yes" {
		return false, "io thread not running", 0
	}
	if sqlThread != "Yes" {
		return false, "sql thread not running", 0
	}
	secs := pick(status, "Seconds_Behind_Source", "Seconds_Behind_Master")
	if secs == "" || secs == "NULL" {
		return false, "lag unknown", 0
	}
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil || n < 0 {
		return false, "lag unreadable", 0
	}
	lag = time.Duration(n) * time.Second
	if lag > maxLag {
		return false, "replication lag over bound", lag
	}
	return true, "replica", lag
}

func pick(status map[string]string, names ...string) string {
	for _, n := range names {
		if v, ok := status[n]; ok {
			return v
		}
	}
	return ""
}

// reader returns the handle for one read.
func (d *DB) reader(ctx context.Context) *sql.DB {
	optIn := replicaReadsOK(ctx)
	useReplica, _ := route(optIn, d.replica != nil, d.state.Load(), time.Now(), 3*d.check)
	if useReplica {
		d.replicaReads.Add(1)
		return d.replica
	}
	if optIn && d.replica != nil {
		d.fallbackReads.Add(1)
	}
	return d.primary
}

// QueryContext routes the read (see WithReplicaReads).
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.reader(ctx).QueryContext(ctx, query, args...)
}

// QueryRowContext routes the read (see WithReplicaReads).
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.reader(ctx).QueryRowContext(ctx, query, args...)
}

// QueryRow always reads the primary: without a context it cannot opt in.
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.primary.QueryRow(query, args...)
}

// ExecContext always writes to the primary.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.primary.ExecContext(ctx, query, args...)
}

// Exec always writes to the primary.
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	return d.primary.Exec(query, args...)
}

// BeginTx always starts on the primary.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return d.primary.BeginTx(ctx, opts)
}

// Conn always takes a primary connection (advisory locks must stay on one session).
func (d *DB) Conn(ctx context.Context) (*sql.Conn, error) {
	return d.primary.Conn(ctx)
}

// Primary is the primary pool, for the few callers that need the raw handle
// (schema, migrations).
func (d *DB) Primary() *sql.DB { return d.primary }

// Stats reports the replica's state and the read counters.
func (d *DB) Stats() ReplicaStats {
	s := ReplicaStats{
		Configured:    d.replica != nil,
		ReplicaReads:  d.replicaReads.Load(),
		FallbackReads: d.fallbackReads.Load(),
	}
	if st := d.state.Load(); st != nil {
		s.Usable, s.Reason, s.Lag, s.LastCheck = st.usable, st.reason, st.lag, st.checked
	}
	return s
}

// Close stops the health checker and closes both pools.
func (d *DB) Close() error {
	d.stopOnce.Do(func() { close(d.stop) })
	var first error
	if d.replica != nil {
		if err := d.replica.Close(); err != nil {
			first = err
		}
	}
	if err := d.primary.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// watch checks the replica on a timer until Close.
func (d *DB) watch() {
	d.probe()
	t := time.NewTicker(d.check)
	defer t.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-t.C:
			d.probe()
		}
	}
}

// probe runs one health check and logs only real changes (usable or the reason),
// never every tick: the lag number changes every second and is logged on a flip.
func (d *DB) probe() {
	ctx, cancel := context.WithTimeout(context.Background(), d.check)
	defer cancel()
	st := &replicaState{checked: time.Now()}
	status, err := replicaStatus(ctx, d.replica)
	if err != nil {
		st.reason = "check failed"
		log.Printf("bnbloc: store: replica check failed: %v", err)
	} else {
		st.usable, st.reason, st.lag = evaluateReplica(status, d.maxLag)
	}
	prev := d.state.Load()
	d.state.Store(st)
	if prev == nil || prev.usable != st.usable || prev.reason != st.reason {
		if st.usable {
			log.Printf("bnbloc: store: replica usable for opted-in reads (lag %s, bound %s)", st.lag, d.maxLag)
		} else {
			log.Printf("bnbloc: store: replica not used for opted-in reads: %s (primary serves them)", st.reason)
		}
	}
}

// replicaStatus returns the first row of the replica's status, with NULL shown as
// "NULL". It tries the current statement, then the one for MySQL before 8.0.22.
func replicaStatus(ctx context.Context, db *sql.DB) (map[string]string, error) {
	var lastErr error
	for _, q := range []string{"SHOW REPLICA STATUS", "SHOW SLAVE STATUS"} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			lastErr = err
			continue
		}
		return firstRow(rows)
	}
	return nil, fmt.Errorf("replica status: %w", lastErr)
}

func firstRow(rows *sql.Rows) (map[string]string, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if !rows.Next() {
		return out, rows.Err()
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, c := range cols {
		if vals[i].Valid {
			out[c] = vals[i].String
		} else {
			out[c] = "NULL"
		}
	}
	return out, rows.Err()
}
