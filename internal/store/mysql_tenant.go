package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// MySQLTenant is the MySQL-backed TenantStore. Like MemoryTenant it operates
// across tenants by design (it is the registry that establishes tenancy) and is
// only reachable from the trusted gateway/admin path.
type MySQLTenant struct {
	DB *sql.DB
}

// NewMySQLTenant constructs a MySQL-backed tenant store.
func NewMySQLTenant(db *sql.DB) *MySQLTenant { return &MySQLTenant{DB: db} }

// tenantCols is the column list a Tenant scans from, in Scan order.
const tenantCols = `tenant_id, name, status, sso_issuer, run_quota, cost_allowance, require_sandbox_for_borrow, public_shares_disabled, created_at_ms,
  retention_messages_days, retention_artifact_versions_days, retention_audit_days, retention_agent_jobs_days`

// scanTenant reads one row of tenantCols.
func scanTenant(sc rowScanner) (Tenant, error) {
	var t Tenant
	var status string
	var ret [4]sql.NullInt64
	if err := sc.Scan(&t.TenantID, &t.Name, &status, &t.SSOIssuer, &t.RunQuota, &t.CostAllowance, &t.RequireSandboxForBorrow, &t.PublicSharesDisabled, &t.CreatedAtMs,
		&ret[0], &ret[1], &ret[2], &ret[3]); err != nil {
		return Tenant{}, err
	}
	t.Status = TenantStatus(status)
	for i, k := range wire.RetentionKinds {
		if ret[i].Valid {
			v := int(ret[i].Int64)
			t.Retention = t.Retention.With(k, &v)
		}
	}
	return t, nil
}

func (m *MySQLTenant) PutTenant(ctx context.Context, t Tenant) error {
	if t.Status == "" {
		t.Status = TenantActive
	}
	_, err := m.DB.ExecContext(ctx, `
INSERT INTO tenants (tenant_id, name, status, sso_issuer, created_at_ms)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE name = VALUES(name), status = VALUES(status), sso_issuer = VALUES(sso_issuer)`,
		t.TenantID, t.Name, string(t.Status), t.SSOIssuer, t.CreatedAtMs)
	return err
}
func (m *MySQLTenant) GetTenant(ctx context.Context, tenantID string) (Tenant, error) {
	t, err := scanTenant(m.DB.QueryRowContext(ctx, `
SELECT `+tenantCols+` FROM tenants WHERE tenant_id = ?`, tenantID))
	if errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrTenantNotFound
	}
	if err != nil {
		return Tenant{}, err
	}
	return t, nil
}

// ListTenants returns every tenant, for the platform (super-admin) console.
func (m *MySQLTenant) ListTenants(ctx context.Context) ([]Tenant, error) {
	return queryAll(ctx, m.DB, `
SELECT `+tenantCols+` FROM tenants ORDER BY created_at_ms ASC`, scanTenant)
}

// ListMembers returns all members of a tenant.
func (m *MySQLTenant) ListMembers(ctx context.Context, tenantID string) ([]TenantMember, error) {
	return queryAll(ctx, m.DB, `
SELECT `+tenantMemberCols+` FROM tenant_members WHERE tenant_id = ? ORDER BY joined_at_ms ASC`, scanTenantMember, tenantID)
}

// ListMembershipsForUser returns every tenant membership of a user.
func (m *MySQLTenant) ListMembershipsForUser(ctx context.Context, userID string) ([]TenantMember, error) {
	return queryAll(ctx, m.DB, `
SELECT `+tenantMemberCols+` FROM tenant_members WHERE user_id = ? ORDER BY joined_at_ms ASC`, scanTenantMember, userID)
}

// tenantMemberCols is the column list scanTenantMember reads, in its order.
const tenantMemberCols = `tenant_id, user_id, role, joined_at_ms, source`

// scanTenantMember reads one row of tenantMemberCols.
func scanTenantMember(r rowScanner) (TenantMember, error) {
	var mem TenantMember
	var role, source string
	if err := r.Scan(&mem.TenantID, &mem.UserID, &role, &mem.JoinedAtMs, &source); err != nil {
		return TenantMember{}, err
	}
	mem.Role = OrgRole(role)
	mem.Source = MemberSource(source)
	return mem, nil
}

// SetQuota persists a tenant's org-level ceilings.
func (m *MySQLTenant) SetQuota(ctx context.Context, tenantID string, runQuota int, costAllowance float64) error {
	res, err := m.DB.ExecContext(ctx, `
UPDATE tenants SET run_quota = ?, cost_allowance = ? WHERE tenant_id = ?`, runQuota, costAllowance, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTenantNotFound
	}
	return nil
}

// SetTenantPolicy persists the set policy switches in one statement (an
// unset field keeps its column) and returns the stored tenant.
func (m *MySQLTenant) SetTenantPolicy(ctx context.Context, tenantID string, u TenantPolicyUpdate) (Tenant, error) {
	if _, err := m.DB.ExecContext(ctx, `
UPDATE tenants SET
  require_sandbox_for_borrow = COALESCE(?, require_sandbox_for_borrow),
  public_shares_disabled = COALESCE(?, public_shares_disabled)
WHERE tenant_id = ?`, nullableBool(u.RequireSandboxForBorrow), nullableBool(u.PublicSharesDisabled), tenantID); err != nil {
		return Tenant{}, err
	}
	// The stored row is the result (ErrTenantNotFound when there is none).
	return m.GetTenant(ctx, tenantID)
}

// SetTenantRetention replaces the four retention override columns in one
// statement (a null kind uses the platform default).
func (m *MySQLTenant) SetTenantRetention(ctx context.Context, tenantID string, p wire.RetentionPolicy) (Tenant, error) {
	if _, err := m.DB.ExecContext(ctx, `
UPDATE tenants SET
  retention_messages_days = ?,
  retention_artifact_versions_days = ?,
  retention_audit_days = ?,
  retention_agent_jobs_days = ?
WHERE tenant_id = ?`, nullableInt(p.MessagesDays), nullableInt(p.ArtifactVersionsDays), nullableInt(p.AuditDays), nullableInt(p.AgentJobsDays), tenantID); err != nil {
		return Tenant{}, err
	}
	return m.GetTenant(ctx, tenantID)
}

// nullableInt maps an unset value to SQL NULL.
func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullableBool maps an unset switch to SQL NULL.
func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func (m *MySQLTenant) TenantForIssuer(ctx context.Context, issuer string) (string, bool, error) {
	if issuer == "" {
		return "", false, nil
	}
	var id string
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id FROM tenants WHERE sso_issuer = ? LIMIT 1`, issuer).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, id != "", nil
}

func (m *MySQLTenant) PutMember(ctx context.Context, mem TenantMember) error {
	if mem.Role == "" {
		mem.Role = OrgRoleMember
	}
	_, err := m.DB.ExecContext(ctx, `
INSERT INTO tenant_members (tenant_id, user_id, role, joined_at_ms, source)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE role = VALUES(role), source = VALUES(source)`,
		mem.TenantID, mem.UserID, string(mem.Role), mem.JoinedAtMs, string(normalizeMemberSource(mem.Source)))
	return err
}

// EnsureMember is insert-only (INSERT IGNORE) and reads the stored row back,
// so an existing membership keeps its role whatever the caller passed.
func (m *MySQLTenant) EnsureMember(ctx context.Context, mem TenantMember) (TenantMember, bool, error) {
	if mem.Role == "" {
		mem.Role = OrgRoleMember
	}
	res, err := m.DB.ExecContext(ctx, `
INSERT IGNORE INTO tenant_members (tenant_id, user_id, role, joined_at_ms, source)
VALUES (?, ?, ?, ?, ?)`,
		mem.TenantID, mem.UserID, string(mem.Role), mem.JoinedAtMs, string(normalizeMemberSource(mem.Source)))
	if err != nil {
		return TenantMember{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return TenantMember{}, false, err
	}
	stored, ok, err := m.GetMember(ctx, mem.TenantID, mem.UserID)
	if err != nil {
		return TenantMember{}, false, err
	}
	if !ok {
		return TenantMember{}, false, errMemberVanished
	}
	return stored, n == 1, nil
}

// errMemberVanished: the membership EnsureMember just inserted or found was
// removed before it could be read back.
var errMemberVanished = errors.New("store: tenant membership removed concurrently")

func (m *MySQLTenant) GetMember(ctx context.Context, tenantID, userID string) (TenantMember, bool, error) {
	mem, err := scanTenantMember(m.DB.QueryRowContext(ctx, `
SELECT `+tenantMemberCols+` FROM tenant_members WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return TenantMember{}, false, nil
	}
	if err != nil {
		return TenantMember{}, false, err
	}
	return mem, true, nil
}

func (m *MySQLTenant) MemberCount(ctx context.Context, tenantID string) (int, error) {
	var n int
	if err := m.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tenant_members WHERE tenant_id = ?`, tenantID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (m *MySQLTenant) RemoveMember(ctx context.Context, tenantID, userID string) error {
	_, err := m.DB.ExecContext(ctx, `
DELETE FROM tenant_members WHERE tenant_id = ? AND user_id = ?`, tenantID, userID)
	return err
}

func (m *MySQLTenant) PutJoinRequest(ctx context.Context, jr JoinRequest) error {
	_, err := m.DB.ExecContext(ctx, `
INSERT INTO tenant_join_requests (tenant_id, user_id, message, status, created_at_ms, decided_at_ms, decided_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE message=VALUES(message), status=VALUES(status),
  created_at_ms=VALUES(created_at_ms), decided_at_ms=VALUES(decided_at_ms), decided_by=VALUES(decided_by)`,
		jr.TenantID, jr.UserID, jr.Message, string(jr.Status), jr.CreatedAtMs, jr.DecidedAtMs, jr.DecidedBy)
	return err
}

// joinRequestCols is the column list scanJoinRequest reads, in its order.
const joinRequestCols = `tenant_id, user_id, message, status, created_at_ms, decided_at_ms, decided_by`

// scanJoinRequest reads one row of joinRequestCols.
func scanJoinRequest(r rowScanner) (JoinRequest, error) {
	var jr JoinRequest
	var status string
	if err := r.Scan(&jr.TenantID, &jr.UserID, &jr.Message, &status, &jr.CreatedAtMs, &jr.DecidedAtMs, &jr.DecidedBy); err != nil {
		return JoinRequest{}, err
	}
	jr.Status = JoinRequestStatus(status)
	return jr, nil
}

func (m *MySQLTenant) GetJoinRequest(ctx context.Context, tenantID, userID string) (JoinRequest, bool, error) {
	jr, err := scanJoinRequest(m.DB.QueryRowContext(ctx, `
SELECT `+joinRequestCols+`
FROM tenant_join_requests WHERE tenant_id = ? AND user_id = ?`, tenantID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return JoinRequest{}, false, nil
	}
	if err != nil {
		return JoinRequest{}, false, err
	}
	return jr, true, nil
}

func (m *MySQLTenant) ListJoinRequests(ctx context.Context, tenantID string, status JoinRequestStatus) ([]JoinRequest, error) {
	q := `SELECT ` + joinRequestCols + `
FROM tenant_join_requests WHERE tenant_id = ?`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY created_at_ms ASC`
	return queryAll(ctx, m.DB, q, scanJoinRequest, args...)
}

func (m *MySQLTenant) ListJoinRequestsForUser(ctx context.Context, userID string) ([]JoinRequest, error) {
	return queryAll(ctx, m.DB, `
SELECT `+joinRequestCols+`
FROM tenant_join_requests WHERE user_id = ? ORDER BY created_at_ms DESC`, scanJoinRequest, userID)
}

func (m *MySQLTenant) PrimaryTenantForUser(ctx context.Context, userID string) (string, bool, error) {
	var tid string
	// A real org membership outranks the "default" catch-all (auto-provisioned
	// on first login); within the same class the most-recently-joined wins.
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id FROM tenant_members WHERE user_id = ?
ORDER BY (tenant_id = 'default') ASC, joined_at_ms DESC LIMIT 1`, userID).Scan(&tid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return tid, true, nil
}
