package store

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// The store's small shared rules. Each has one owner here; both backends
// call it, so the memory and MySQL answers cannot drift
// (TestStoreRulesSingleSource forbids re-spelling them elsewhere).

// clampLimit is the one page-size rule: a non-positive n means def, and n
// never exceeds max. Both backends clamp a request with the same call.
func clampLimit(n, def, max int) int {
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// liveAt is the one expiry rule: a value expiring at expiresAtMs is live
// strictly before it (at nowMs == expiresAtMs it has expired). SQL spells
// the same rule `expires_at_ms > ?` with nowMs.
func liveAt(expiresAtMs, nowMs int64) bool { return nowMs < expiresAtMs }

// mysqlErrDupEntry is MySQL's ER_DUP_ENTRY (duplicate key on insert).
const mysqlErrDupEntry = 1062

// mysqlErrDeadlock is MySQL's ER_LOCK_DEADLOCK: the transaction was rolled
// back and may be run again.
const mysqlErrDeadlock = 1213

// isDupEntry reports a duplicate-key error. It and isDeadlock are the only
// readers of a driver error number: a write that can collide maps it to the
// store's exported sentinel with dupAs, which the memory store returns as
// well.
func isDupEntry(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDupEntry
}

// isDeadlock reports a transaction MySQL rolled back as a deadlock victim.
func isDeadlock(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDeadlock
}

// isDupKey reports a duplicate-key error on the named unique key.
func isDupKey(err error, key string) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == mysqlErrDupEntry && strings.Contains(me.Message, key)
}

// dupAs returns sentinel when err is a duplicate-key error, else err.
func dupAs(err, sentinel error) error {
	if isDupEntry(err) {
		return sentinel
	}
	return err
}

// nullIfZero writes the zero value of a column as SQL NULL.
func nullIfZero[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// nullPtr writes an unset (nil) value as SQL NULL.
func nullPtr[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// SQL predicates spelled once.
const (
	// sqlNotDeleted keeps soft-deleted rows out of a read.
	sqlNotDeleted = "deleted_at_ms IS NULL"
	// sqlHumanMember keeps agent-host memberships out of a room_members
	// read (Membership.Human on the Go side).
	sqlHumanMember = "agent_host = 0"
)
