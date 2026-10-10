package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/go-sql-driver/mysql"
)

// SSOHandoff is a pending SSO sign-in parked for a client that did not start
// in the browser completing it: a desktop one-time code (bnbloc://sso deep
// link) or a CLI grant claimed by nonce. It stores the verified identity, not
// tokens: the gateway mints the session when the hand-off is redeemed, so no
// bearer or refresh token ever rests in the table.
//
// KeyHash is the hex sha256 of the lookup secret (the desktop code or the CLI
// nonce, domain-separated by the caller); the raw secret is never stored.
// NonceHash binds a desktop code to the nonce the desktop generated.
type SSOHandoff struct {
	KeyHash     string
	NonceHash   string
	Subject     string
	Email       string
	TenantHint  string
	ExpiresAtMs int64
}

// ErrSSOHandoffPending refuses a second hand-off under a key that is still
// live: a second sign-in can never swap the grant a claimant is waiting for.
var ErrSSOHandoffPending = errors.New("sso hand-off already pending")

// SSOHandoffStore persists pending SSO hand-offs so any gateway replica can
// redeem a hand-off another replica parked. Implementations must make Take
// single-use under concurrency (exactly one caller wins a key).
type SSOHandoffStore interface {
	// PutSSOHandoff parks h, first pruning expired hand-offs. A live entry
	// under the same key fails with ErrSSOHandoffPending.
	PutSSOHandoff(ctx context.Context, h SSOHandoff, nowMs int64) error
	// TakeSSOHandoff atomically consumes the live hand-off under keyHash.
	// found=false when it is absent, expired, or already consumed.
	TakeSSOHandoff(ctx context.Context, keyHash string, nowMs int64) (SSOHandoff, bool, error)
}

// maxPendingSSOHandoffs bounds the in-memory table (a flood of SSO completions
// cannot grow a dev gateway without limit). The MySQL table is bounded by the
// short TTL and the opportunistic prune.
const maxPendingSSOHandoffs = 10000

// errTooManySSOHandoffs refuses a hand-off when the in-memory table is full.
var errTooManySSOHandoffs = errors.New("too many pending sso hand-offs")

// MemorySSOHandoff is the in-memory SSOHandoffStore (dev / tests; one process).
type MemorySSOHandoff struct {
	mu   sync.Mutex
	rows map[string]SSOHandoff
}

// NewMemorySSOHandoff constructs an in-memory SSO hand-off store.
func NewMemorySSOHandoff() *MemorySSOHandoff {
	return &MemorySSOHandoff{rows: map[string]SSOHandoff{}}
}

var _ SSOHandoffStore = (*MemorySSOHandoff)(nil)

func (s *MemorySSOHandoff) PutSSOHandoff(_ context.Context, h SSOHandoff, nowMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.rows {
		if v.ExpiresAtMs < nowMs {
			delete(s.rows, k)
		}
	}
	if _, taken := s.rows[h.KeyHash]; taken {
		return ErrSSOHandoffPending
	}
	if len(s.rows) >= maxPendingSSOHandoffs {
		return errTooManySSOHandoffs
	}
	s.rows[h.KeyHash] = h
	return nil
}

func (s *MemorySSOHandoff) TakeSSOHandoff(_ context.Context, keyHash string, nowMs int64) (SSOHandoff, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.rows[keyHash]
	delete(s.rows, keyHash)
	if !ok || h.ExpiresAtMs < nowMs {
		return SSOHandoff{}, false, nil
	}
	return h, true, nil
}

// MySQLSSOHandoff implements SSOHandoffStore over database/sql.
type MySQLSSOHandoff struct{ DB *sql.DB }

// NewMySQLSSOHandoff constructs a MySQL-backed SSO hand-off store.
func NewMySQLSSOHandoff(db *sql.DB) *MySQLSSOHandoff { return &MySQLSSOHandoff{DB: db} }

var _ SSOHandoffStore = (*MySQLSSOHandoff)(nil)

func (s *MySQLSSOHandoff) PutSSOHandoff(ctx context.Context, h SSOHandoff, nowMs int64) error {
	// Prune first: it also clears an expired row under this key, so the insert
	// below only collides with a LIVE (or consumed-but-unexpired) hand-off.
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sso_handoff_codes WHERE expires_at_ms < ?`, nowMs); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sso_handoff_codes
  (key_hash, nonce_hash, subject, email, tenant_hint, expires_at_ms)
  VALUES (?, ?, ?, ?, ?, ?)`,
		h.KeyHash, h.NonceHash, h.Subject, h.Email, h.TenantHint, h.ExpiresAtMs)
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlErrDupEntry {
		return ErrSSOHandoffPending
	}
	return err
}

func (s *MySQLSSOHandoff) TakeSSOHandoff(ctx context.Context, keyHash string, nowMs int64) (SSOHandoff, bool, error) {
	// The conditional UPDATE is the single-use gate: of N replicas racing on
	// one key exactly one sees RowsAffected == 1. The winner then reads and
	// deletes its row; a crash in between leaves a consumed row that the
	// expiry prune removes.
	res, err := s.DB.ExecContext(ctx, `UPDATE sso_handoff_codes SET consumed_at_ms = ?
  WHERE key_hash = ? AND consumed_at_ms IS NULL AND expires_at_ms >= ?`, nowMs, keyHash, nowMs)
	if err != nil {
		return SSOHandoff{}, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return SSOHandoff{}, false, err
	}
	h := SSOHandoff{KeyHash: keyHash}
	err = s.DB.QueryRowContext(ctx, `SELECT nonce_hash, subject, email, tenant_hint, expires_at_ms
  FROM sso_handoff_codes WHERE key_hash = ?`, keyHash).Scan(&h.NonceHash, &h.Subject, &h.Email, &h.TenantHint, &h.ExpiresAtMs)
	if err != nil {
		return SSOHandoff{}, false, err
	}
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM sso_handoff_codes WHERE key_hash = ?`, keyHash); err != nil {
		return SSOHandoff{}, false, err
	}
	return h, true, nil
}
