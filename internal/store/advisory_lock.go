package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
)

// MySQL advisory locks (GET_LOCK) are server-wide: a name is shared by every
// database on the server. A lock guards work on ONE database (its migration
// ledger, its retention pass), so its name carries the database: two
// deployments, or two test databases, on one server never wait on or skip for
// each other. advisoryLock is the only place a lock name is built and taken.

// maxLockName is MySQL's limit on a GET_LOCK name.
const maxLockName = 64

// scopedLockName is name qualified by the database it guards, kept within
// MySQL's length limit (a long database name is replaced by its digest).
func scopedLockName(name, database string) string {
	if database == "" {
		return name
	}
	full := name + "@" + database
	if len(full) <= maxLockName {
		return full
	}
	sum := sha256.Sum256([]byte(database))
	digest := hex.EncodeToString(sum[:])
	if keep := maxLockName - len(name) - 1; keep < len(digest) {
		digest = digest[:keep]
	}
	return name + "@" + digest
}

// advisoryLock takes the database-scoped lock name on conn, waiting up to
// waitSeconds (0: do not wait). A lock is owned by the session that took it,
// so the caller keeps conn pinned until release, which frees the lock (it
// does not close conn). ok is false when another session holds it.
func advisoryLock(ctx context.Context, conn *sql.Conn, name string, waitSeconds int) (release func(), ok bool, err error) {
	key, err := lockKey(ctx, conn, name)
	if err != nil {
		return nil, false, err
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", key, waitSeconds).Scan(&got); err != nil {
		return nil, false, fmt.Errorf("take lock %s: %w", key, err)
	}
	if !got.Valid || got.Int64 != 1 {
		return nil, false, nil
	}
	return func() { _, _ = conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK(?)", key) }, true, nil
}

// lockKey is the server-wide name of the lock name on q's database.
func lockKey(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string) (string, error) {
	var database sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		return "", fmt.Errorf("lock %s: read database: %w", name, err)
	}
	return scopedLockName(name, database.String), nil
}
