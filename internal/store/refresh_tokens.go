package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"sync"
)

// RefreshFamily is one sign-in's chain of refresh tokens. Every refresh token
// names its family and a token id (jti); the family records which jti is
// current, so a rotated-away token presented again is detected as reuse and
// the whole family is revoked. The family also carries the verified identity
// a refresh replays (subject, email, the verifier's tenant hint), so a
// refresh re-runs tenant resolution exactly like the original sign-in.
type RefreshFamily struct {
	FamilyID   string
	Subject    string
	Email      string
	TenantHint string
	// TenantID and Role are the scope the family's last session was issued
	// in; admin revocation (member removal, tenant suspension) selects on it.
	TenantID string
	Role     string
	// Client labels the sign-in surface ("web", "desktop", "cli", "bridge")
	// and UserAgent its request User-Agent, for the member's session list.
	Client    string
	UserAgent string
	// ParentFamilyID is the family a forked sign-in (the desktop's agent
	// bridge) was forked from; empty for a direct sign-in.
	ParentFamilyID string
	CurrentJTI     string
	PreviousJTI    string
	RotatedAtMs    int64
	CreatedAtMs    int64
	LastUsedAtMs   int64
	ExpiresAtMs    int64
	// RevokedAtMs is 0 while the family is active.
	RevokedAtMs int64
}

// RefreshUse presents a refresh token of a family. NewJTI rotates the family
// to a new current token; empty only validates (and records use).
type RefreshUse struct {
	FamilyID     string
	PresentedJTI string
	NewJTI       string
	NowMs        int64
	// GraceMs is how long after a rotation the immediately-previous jti is
	// still accepted as a normal use (two tabs refreshing the same token at
	// once): it yields the CURRENT jti, it never rotates again.
	GraceMs int64
	// ExpiresAtMs slides the family's expiry on a successful use.
	ExpiresAtMs int64
	// TenantID / Role record the scope the new session lands in.
	TenantID string
	Role     string
}

// RefreshRevokeFilter selects families for bulk revocation. At least one of
// Subject or TenantID is required (an empty filter never revokes everything).
// ExceptRole skips families whose recorded role matches (a platform
// super-admin keeps access to a suspended tenant).
type RefreshRevokeFilter struct {
	Subject    string
	TenantID   string
	ExceptRole string
}

var (
	// ErrRefreshInvalid: the family is unknown, expired, or revoked.
	ErrRefreshInvalid = errors.New("refresh token family invalid")
	// ErrRefreshReuse: a rotated-away token was presented; the family has
	// been revoked by this call.
	ErrRefreshReuse = errors.New("refresh token reuse detected; family revoked")
	// ErrRefreshFamilyExists rejects a duplicate family id.
	ErrRefreshFamilyExists = errors.New("refresh token family exists")
	// errEmptyRevokeFilter refuses a bulk revocation with no selector.
	errEmptyRevokeFilter = errors.New("refresh revoke filter needs a subject or tenant")
)

// RefreshTokenStore tracks refresh-token families server-side so refresh
// tokens are rotatable, revocable, and reuse-detecting. Implementations must
// make UseRefreshFamily atomic per family. Families are global (a sign-in of
// a subject, keyed by family id), not tenant-scoped: TenantID records the
// scope of the last session. A family is live strictly before ExpiresAtMs
// (liveAt). A revocation needs its time: nowMs <= 0 is
// ErrRevokeTimeRequired (0 reads as "not revoked").
type RefreshTokenStore interface {
	// CreateRefreshFamily records a new family (first pruning the families
	// expired at f.CreatedAtMs); a taken id is ErrRefreshFamilyExists.
	CreateRefreshFamily(ctx context.Context, f RefreshFamily) error
	// GetRefreshFamily returns a family regardless of state.
	GetRefreshFamily(ctx context.Context, familyID string) (RefreshFamily, bool, error)
	// UseRefreshFamily validates (and with NewJTI, rotates) a presented
	// token. It returns the updated family; its CurrentJTI is the jti the
	// caller embeds in the token it issues. ErrRefreshInvalid / ErrRefreshReuse
	// on failure.
	UseRefreshFamily(ctx context.Context, u RefreshUse) (RefreshFamily, error)
	// ListRefreshFamilies returns subject's active (unrevoked, unexpired)
	// families, most recently used first.
	ListRefreshFamilies(ctx context.Context, subject string, nowMs int64) ([]RefreshFamily, error)
	// RevokeRefreshFamily revokes one of subject's families; revoked=false
	// when no active family matched (idempotent).
	RevokeRefreshFamily(ctx context.Context, subject, familyID string, nowMs int64) (revoked bool, err error)
	// RevokeRefreshFamilies revokes every active family matching the filter
	// and returns how many were revoked.
	RevokeRefreshFamilies(ctx context.Context, filter RefreshRevokeFilter, nowMs int64) (int, error)
	// SwitchRefreshFamily moves an active family of the subject to another
	// tenant (a member's tenant switch): its tenant hint, which every later
	// refresh and fork resolves from, and its current scope. It never
	// rotates the token. ErrRefreshInvalid when no active family matched.
	SwitchRefreshFamily(ctx context.Context, sw RefreshSwitch) (RefreshFamily, error)
}

// RefreshSwitch moves a family to the tenant a member switched to.
type RefreshSwitch struct {
	FamilyID string
	Subject  string
	// TenantHint is the switched-to tenant: refreshes and forks land there.
	TenantHint string
	// TenantID and Role are the switched session's scope (admin revocation
	// selects on them).
	TenantID string
	Role     string
	NowMs    int64
}

// decideRefreshSwitch is the one switch rule both backends apply to a
// loaded family.
func decideRefreshSwitch(f RefreshFamily, sw RefreshSwitch) (RefreshFamily, error) {
	if f.Subject != sw.Subject || !f.active(sw.NowMs) || strings.TrimSpace(sw.TenantHint) == "" {
		return f, ErrRefreshInvalid
	}
	f.TenantHint, f.TenantID, f.Role, f.LastUsedAtMs = sw.TenantHint, sw.TenantID, sw.Role, sw.NowMs
	return f, nil
}

// decideRefreshUse is the one rotation/reuse rule both backends apply to a
// loaded family. It returns the updated family, or an error; reuse=true means
// the caller must persist f with RevokedAtMs set (the returned family).
func decideRefreshUse(f RefreshFamily, u RefreshUse) (out RefreshFamily, reuse bool, err error) {
	if !f.active(u.NowMs) || u.PresentedJTI == "" {
		return f, false, ErrRefreshInvalid
	}
	switch {
	case u.PresentedJTI == f.CurrentJTI:
		if u.NewJTI != "" {
			f.PreviousJTI = f.CurrentJTI
			f.CurrentJTI = u.NewJTI
			f.RotatedAtMs = u.NowMs
		}
	case u.PresentedJTI == f.PreviousJTI && u.NowMs-f.RotatedAtMs <= u.GraceMs:
		// A concurrent refresh of the token just rotated away: answer with
		// the current jti so both callers end up holding a live token.
	default:
		f.RevokedAtMs = u.NowMs
		return f, true, ErrRefreshReuse
	}
	f.LastUsedAtMs = u.NowMs
	f.ExpiresAtMs = max(f.ExpiresAtMs, u.ExpiresAtMs) // a use only slides the expiry later
	if u.TenantID != "" {
		f.TenantID = u.TenantID
	}
	f.Role = u.Role
	return f, false, nil
}

func (f RefreshFamily) active(nowMs int64) bool {
	return f.RevokedAtMs == 0 && liveAt(f.ExpiresAtMs, nowMs)
}

func (r RefreshRevokeFilter) matches(f RefreshFamily) bool {
	if r.Subject != "" && f.Subject != r.Subject {
		return false
	}
	if r.TenantID != "" && f.TenantID != r.TenantID {
		return false
	}
	return r.ExceptRole == "" || f.Role != r.ExceptRole
}

func (r RefreshRevokeFilter) empty() bool {
	return strings.TrimSpace(r.Subject) == "" && strings.TrimSpace(r.TenantID) == ""
}

// MemoryRefreshToken is the in-memory RefreshTokenStore (dev / tests).
type MemoryRefreshToken struct {
	mu   sync.Mutex
	rows map[string]RefreshFamily
}

// NewMemoryRefreshToken constructs an in-memory refresh token store.
func NewMemoryRefreshToken() *MemoryRefreshToken {
	return &MemoryRefreshToken{rows: map[string]RefreshFamily{}}
}

var _ RefreshTokenStore = (*MemoryRefreshToken)(nil)

func (s *MemoryRefreshToken) CreateRefreshFamily(_ context.Context, f RefreshFamily) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.rows {
		if !liveAt(v.ExpiresAtMs, f.CreatedAtMs) {
			delete(s.rows, k)
		}
	}
	if _, dup := s.rows[f.FamilyID]; dup {
		return ErrRefreshFamilyExists
	}
	s.rows[f.FamilyID] = f
	return nil
}

func (s *MemoryRefreshToken) GetRefreshFamily(_ context.Context, familyID string) (RefreshFamily, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.rows[familyID]
	return f, ok, nil
}

func (s *MemoryRefreshToken) UseRefreshFamily(_ context.Context, u RefreshUse) (RefreshFamily, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.rows[u.FamilyID]
	if !ok {
		return RefreshFamily{}, ErrRefreshInvalid
	}
	out, reuse, err := decideRefreshUse(f, u)
	if err == nil || reuse {
		s.rows[u.FamilyID] = out
	}
	return out, err
}

func (s *MemoryRefreshToken) SwitchRefreshFamily(_ context.Context, sw RefreshSwitch) (RefreshFamily, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.rows[sw.FamilyID]
	if !ok {
		return RefreshFamily{}, ErrRefreshInvalid
	}
	out, err := decideRefreshSwitch(f, sw)
	if err != nil {
		return RefreshFamily{}, err
	}
	s.rows[sw.FamilyID] = out
	return out, nil
}

func (s *MemoryRefreshToken) ListRefreshFamilies(_ context.Context, subject string, nowMs int64) ([]RefreshFamily, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []RefreshFamily{}
	for _, f := range s.rows {
		if f.Subject == subject && f.active(nowMs) {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LastUsedAtMs != out[j].LastUsedAtMs {
			return out[i].LastUsedAtMs > out[j].LastUsedAtMs
		}
		return out[i].FamilyID < out[j].FamilyID
	})
	return out, nil
}

func (s *MemoryRefreshToken) RevokeRefreshFamily(_ context.Context, subject, familyID string, nowMs int64) (bool, error) {
	if nowMs <= 0 {
		return false, ErrRevokeTimeRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.rows[familyID]
	if !ok || f.Subject != subject || f.RevokedAtMs != 0 {
		return false, nil
	}
	f.RevokedAtMs = nowMs
	s.rows[familyID] = f
	return true, nil
}

func (s *MemoryRefreshToken) RevokeRefreshFamilies(_ context.Context, filter RefreshRevokeFilter, nowMs int64) (int, error) {
	if filter.empty() {
		return 0, errEmptyRevokeFilter
	}
	if nowMs <= 0 {
		return 0, ErrRevokeTimeRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, f := range s.rows {
		if f.RevokedAtMs == 0 && filter.matches(f) {
			f.RevokedAtMs = nowMs
			s.rows[id] = f
			n++
		}
	}
	return n, nil
}

// MySQLRefreshToken implements RefreshTokenStore over database/sql.
type MySQLRefreshToken struct{ DB *sql.DB }

// NewMySQLRefreshToken constructs a MySQL-backed refresh token store.
func NewMySQLRefreshToken(db *sql.DB) *MySQLRefreshToken { return &MySQLRefreshToken{DB: db} }

var _ RefreshTokenStore = (*MySQLRefreshToken)(nil)

const refreshFamilyColumns = `family_id, subject, email, tenant_hint, tenant_id, role, client, user_agent,
  current_jti, previous_jti, rotated_at_ms, created_at_ms, last_used_at_ms, expires_at_ms, COALESCE(revoked_at_ms, 0),
  parent_family_id`

func scanRefreshFamily(row rowScanner) (RefreshFamily, error) {
	var f RefreshFamily
	err := row.Scan(&f.FamilyID, &f.Subject, &f.Email, &f.TenantHint, &f.TenantID, &f.Role, &f.Client, &f.UserAgent,
		&f.CurrentJTI, &f.PreviousJTI, &f.RotatedAtMs, &f.CreatedAtMs, &f.LastUsedAtMs, &f.ExpiresAtMs, &f.RevokedAtMs,
		&f.ParentFamilyID)
	return f, err
}

func (s *MySQLRefreshToken) CreateRefreshFamily(ctx context.Context, f RefreshFamily) error {
	if _, err := s.DB.ExecContext(ctx, `DELETE FROM refresh_token_families WHERE expires_at_ms <= ?`, f.CreatedAtMs); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO refresh_token_families
  (family_id, subject, email, tenant_hint, tenant_id, role, client, user_agent,
   current_jti, previous_jti, rotated_at_ms, created_at_ms, last_used_at_ms, expires_at_ms, parent_family_id)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.FamilyID, f.Subject, f.Email, f.TenantHint, f.TenantID, f.Role, f.Client, f.UserAgent,
		f.CurrentJTI, f.PreviousJTI, f.RotatedAtMs, f.CreatedAtMs, f.LastUsedAtMs, f.ExpiresAtMs, f.ParentFamilyID)
	return dupAs(err, ErrRefreshFamilyExists)
}

func (s *MySQLRefreshToken) GetRefreshFamily(ctx context.Context, familyID string) (RefreshFamily, bool, error) {
	// cross-tenant: a family is a subject's global sign-in, keyed by its id.
	f, err := scanRefreshFamily(s.DB.QueryRowContext(ctx,
		`SELECT `+refreshFamilyColumns+` FROM refresh_token_families WHERE family_id = ?`, familyID))
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshFamily{}, false, nil
	}
	if err != nil {
		return RefreshFamily{}, false, err
	}
	return f, true, nil
}

func (s *MySQLRefreshToken) UseRefreshFamily(ctx context.Context, u RefreshUse) (RefreshFamily, error) {
	// The row lock serializes concurrent uses of one family across replicas,
	// so two refreshes of the same token can never both rotate it.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RefreshFamily{}, err
	}
	defer func() { _ = tx.Rollback() }()
	f, err := scanRefreshFamily(tx.QueryRowContext(ctx,
		`SELECT `+refreshFamilyColumns+` FROM refresh_token_families WHERE family_id = ? FOR UPDATE`, u.FamilyID))
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshFamily{}, ErrRefreshInvalid
	}
	if err != nil {
		return RefreshFamily{}, err
	}
	out, reuse, useErr := decideRefreshUse(f, u)
	if useErr != nil && !reuse {
		return out, useErr
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_token_families SET
  current_jti = ?, previous_jti = ?, rotated_at_ms = ?, last_used_at_ms = ?, expires_at_ms = ?,
  tenant_id = ?, role = ?, revoked_at_ms = ?
  WHERE family_id = ?`,
		out.CurrentJTI, out.PreviousJTI, out.RotatedAtMs, out.LastUsedAtMs, out.ExpiresAtMs,
		out.TenantID, out.Role, nullIfZero(out.RevokedAtMs), out.FamilyID); err != nil {
		return RefreshFamily{}, err
	}
	if err := tx.Commit(); err != nil {
		return RefreshFamily{}, err
	}
	return out, useErr
}

func (s *MySQLRefreshToken) SwitchRefreshFamily(ctx context.Context, sw RefreshSwitch) (RefreshFamily, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RefreshFamily{}, err
	}
	defer func() { _ = tx.Rollback() }()
	f, err := scanRefreshFamily(tx.QueryRowContext(ctx,
		`SELECT `+refreshFamilyColumns+` FROM refresh_token_families WHERE family_id = ? FOR UPDATE`, sw.FamilyID))
	if errors.Is(err, sql.ErrNoRows) {
		return RefreshFamily{}, ErrRefreshInvalid
	}
	if err != nil {
		return RefreshFamily{}, err
	}
	out, err := decideRefreshSwitch(f, sw)
	if err != nil {
		return RefreshFamily{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_token_families SET
  tenant_hint = ?, tenant_id = ?, role = ?, last_used_at_ms = ?
  WHERE family_id = ?`, out.TenantHint, out.TenantID, out.Role, out.LastUsedAtMs, out.FamilyID); err != nil {
		return RefreshFamily{}, err
	}
	if err := tx.Commit(); err != nil {
		return RefreshFamily{}, err
	}
	return out, nil
}

func (s *MySQLRefreshToken) ListRefreshFamilies(ctx context.Context, subject string, nowMs int64) ([]RefreshFamily, error) {
	// cross-tenant: a subject's sign-ins span every tenant they belong to.
	return queryAll(ctx, s.DB, `SELECT `+refreshFamilyColumns+` FROM refresh_token_families
  WHERE subject = ? AND revoked_at_ms IS NULL AND expires_at_ms > ?
  ORDER BY last_used_at_ms DESC, family_id`, scanRefreshFamily, subject, nowMs)
}

func (s *MySQLRefreshToken) RevokeRefreshFamily(ctx context.Context, subject, familyID string, nowMs int64) (bool, error) {
	// cross-tenant: a family is a subject's global sign-in, keyed by its id.
	if nowMs <= 0 {
		return false, ErrRevokeTimeRequired
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE refresh_token_families SET revoked_at_ms = ?
  WHERE family_id = ? AND subject = ? AND revoked_at_ms IS NULL`, nowMs, familyID, subject)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *MySQLRefreshToken) RevokeRefreshFamilies(ctx context.Context, filter RefreshRevokeFilter, nowMs int64) (int, error) {
	if filter.empty() {
		return 0, errEmptyRevokeFilter
	}
	if nowMs <= 0 {
		return 0, ErrRevokeTimeRequired
	}
	q := `UPDATE refresh_token_families SET revoked_at_ms = ? WHERE revoked_at_ms IS NULL`
	args := []any{nowMs}
	if filter.Subject != "" {
		q += ` AND subject = ?`
		args = append(args, filter.Subject)
	}
	if filter.TenantID != "" {
		q += ` AND tenant_id = ?`
		args = append(args, filter.TenantID)
	}
	if filter.ExceptRole != "" {
		q += ` AND role <> ?`
		args = append(args, filter.ExceptRole)
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
