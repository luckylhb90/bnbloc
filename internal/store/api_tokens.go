package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
)

// APIToken is a super-admin-issued, long-lived credential for an unattended
// caller (the nightly smoke, a deploy pipeline). It is never a session: the
// gateway exchanges it (POST /v1/auth/api-token) for a short-lived session of
// Subject, re-running tenant resolution and the role lookup at every exchange
// exactly like a refresh. Only the SHA-256 of the secret part is stored.
type APIToken struct {
	TokenID    string
	SecretHash string // hex SHA-256 of the secret part
	Subject    string
	// Email and TenantHint are replayed as the identity at every exchange
	// (a super-admin configured by email matches on Email).
	Email      string
	TenantHint string
	Label      string
	CreatedBy  string
	// CreatedAtMs / ExpiresAtMs bound the token; LastUsedAtMs is the last
	// successful exchange (0 = never).
	CreatedAtMs  int64
	ExpiresAtMs  int64
	LastUsedAtMs int64
	// RevokedAtMs is 0 while the token is not revoked.
	RevokedAtMs int64
	RevokedBy   string
}

// Active reports whether the token may be exchanged at nowMs (live strictly
// before its expiry: liveAt).
func (t APIToken) Active(nowMs int64) bool {
	return t.RevokedAtMs == 0 && liveAt(t.ExpiresAtMs, nowMs)
}

// maxListedAPITokens bounds one listing (newest first).
const maxListedAPITokens = 500

// ErrAPITokenExists rejects a duplicate token id.
var ErrAPITokenExists = errors.New("api token exists")

// ErrRevokeTimeRequired refuses a revocation without its time: a revoked
// time of 0 reads as "not revoked" (API tokens, refresh families), so a
// revoke at 0 would report success and change nothing.
var ErrRevokeTimeRequired = errors.New("store: a revocation needs its time (nowMs > 0)")

// APITokenStore persists API tokens platform-wide (not tenant-scoped: the
// issuer is a platform super-admin). Implementations must be safe for
// concurrent use.
type APITokenStore interface {
	CreateAPIToken(ctx context.Context, t APIToken) error
	// GetAPIToken returns a token regardless of state.
	GetAPIToken(ctx context.Context, tokenID string) (APIToken, bool, error)
	// ListAPITokens returns every token (revoked and expired included),
	// newest first, at most maxListedAPITokens.
	ListAPITokens(ctx context.Context) ([]APIToken, error)
	// RevokeAPIToken revokes a not-yet-revoked token; revoked=false when no
	// such token exists or it was already revoked (idempotent).
	// ErrRevokeTimeRequired when nowMs <= 0.
	RevokeAPIToken(ctx context.Context, tokenID, by string, nowMs int64) (revoked bool, err error)
	// TouchAPIToken records a successful exchange.
	TouchAPIToken(ctx context.Context, tokenID string, nowMs int64) error
}

// MemoryAPITokens is the in-memory APITokenStore (dev / tests).
type MemoryAPITokens struct {
	mu   sync.Mutex
	rows map[string]APIToken
}

// NewMemoryAPITokens constructs an empty in-memory API token store.
func NewMemoryAPITokens() *MemoryAPITokens { return &MemoryAPITokens{rows: map[string]APIToken{}} }

var _ APITokenStore = (*MemoryAPITokens)(nil)

func (s *MemoryAPITokens) CreateAPIToken(_ context.Context, t APIToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.rows[t.TokenID]; dup {
		return ErrAPITokenExists
	}
	s.rows[t.TokenID] = t
	return nil
}

func (s *MemoryAPITokens) GetAPIToken(_ context.Context, tokenID string) (APIToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rows[tokenID]
	return t, ok, nil
}

func (s *MemoryAPITokens) ListAPITokens(_ context.Context) ([]APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]APIToken, 0, len(s.rows))
	for _, t := range s.rows {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].TokenID < out[j].TokenID
	})
	if len(out) > maxListedAPITokens {
		out = out[:maxListedAPITokens]
	}
	return out, nil
}

func (s *MemoryAPITokens) RevokeAPIToken(_ context.Context, tokenID, by string, nowMs int64) (bool, error) {
	if nowMs <= 0 {
		return false, ErrRevokeTimeRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.rows[tokenID]
	if !ok || t.RevokedAtMs != 0 {
		return false, nil
	}
	t.RevokedAtMs, t.RevokedBy = nowMs, by
	s.rows[tokenID] = t
	return true, nil
}

func (s *MemoryAPITokens) TouchAPIToken(_ context.Context, tokenID string, nowMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.rows[tokenID]; ok {
		t.LastUsedAtMs = nowMs
		s.rows[tokenID] = t
	}
	return nil
}

// MySQLAPITokens is the MySQL-backed APITokenStore (table api_tokens).
type MySQLAPITokens struct{ DB *sql.DB }

// NewMySQLAPITokens builds a MySQLAPITokens over a shared pool.
func NewMySQLAPITokens(db *sql.DB) *MySQLAPITokens { return &MySQLAPITokens{DB: db} }

var _ APITokenStore = (*MySQLAPITokens)(nil)

const apiTokenColumns = `token_id, secret_hash, subject, email, tenant_hint, label, created_by,
  created_at_ms, expires_at_ms, last_used_at_ms, COALESCE(revoked_at_ms, 0), revoked_by`

func scanAPIToken(sc rowScanner) (APIToken, error) {
	var t APIToken
	err := sc.Scan(&t.TokenID, &t.SecretHash, &t.Subject, &t.Email, &t.TenantHint, &t.Label, &t.CreatedBy,
		&t.CreatedAtMs, &t.ExpiresAtMs, &t.LastUsedAtMs, &t.RevokedAtMs, &t.RevokedBy)
	return t, err
}

func (m *MySQLAPITokens) CreateAPIToken(ctx context.Context, t APIToken) error {
	_, err := m.DB.ExecContext(ctx, `INSERT INTO api_tokens
  (token_id, secret_hash, subject, email, tenant_hint, label, created_by, created_at_ms, expires_at_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TokenID, t.SecretHash, t.Subject, t.Email, t.TenantHint, t.Label, t.CreatedBy, t.CreatedAtMs, t.ExpiresAtMs)
	return dupAs(err, ErrAPITokenExists)
}

func (m *MySQLAPITokens) GetAPIToken(ctx context.Context, tokenID string) (APIToken, bool, error) {
	t, err := scanAPIToken(m.DB.QueryRowContext(ctx, `SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_id = ?`, tokenID))
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, false, nil
	}
	if err != nil {
		return APIToken{}, false, err
	}
	return t, true, nil
}

func (m *MySQLAPITokens) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	return queryAll(ctx, m.DB, `SELECT `+apiTokenColumns+` FROM api_tokens
  ORDER BY created_at_ms DESC, token_id LIMIT ?`, scanAPIToken, maxListedAPITokens)
}

func (m *MySQLAPITokens) RevokeAPIToken(ctx context.Context, tokenID, by string, nowMs int64) (bool, error) {
	if nowMs <= 0 {
		return false, ErrRevokeTimeRequired
	}
	res, err := m.DB.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at_ms = ?, revoked_by = ? WHERE token_id = ? AND revoked_at_ms IS NULL`,
		nowMs, by, tokenID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (m *MySQLAPITokens) TouchAPIToken(ctx context.Context, tokenID string, nowMs int64) error {
	_, err := m.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at_ms = ? WHERE token_id = ?`, nowMs, tokenID)
	return err
}
