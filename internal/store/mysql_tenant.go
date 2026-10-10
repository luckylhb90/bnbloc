package store

import (
	"context"
	"database/sql"
	"errors"
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
const tenantCols = `tenant_id, name, status, sso_issuer, run_quota, cost_allowance, require_sandbox_for_borrow, public_shares_disabled, created_at_ms`

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
	var t Tenant
	var status string
	err := m.DB.QueryRowContext(ctx, `
SELECT `+tenantCols+` FROM tenants WHERE tenant_id = ?`, tenantID).
		Scan(&t.TenantID, &t.Name, &status, &t.SSOIssuer, &t.RunQuota, &t.CostAllowance, &t.RequireSandboxForBorrow, &t.PublicSharesDisabled, &t.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return Tenant{}, ErrTenantNotFound
	}
	if err != nil {
		return Tenant{}, err
	}
	t.Status = TenantStatus(status)
	return t, nil
}

// ListTenants returns every tenant, for the platform (super-admin) console.
func (m *MySQLTenant) ListTenants(ctx context.Context) []Tenant {
	rows, err := m.DB.QueryContext(ctx, `
SELECT `+tenantCols+` FROM tenants ORDER BY created_at_ms ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		var status string
		if err := rows.Scan(&t.TenantID, &t.Name, &status, &t.SSOIssuer, &t.RunQuota, &t.CostAllowance, &t.RequireSandboxForBorrow, &t.PublicSharesDisabled, &t.CreatedAtMs); err != nil {
			return out
		}
		t.Status = TenantStatus(status)
		out = append(out, t)
	}
	return out
}

// ListMembers returns all members of a tenant.
func (m *MySQLTenant) ListMembers(ctx context.Context, tenantID string) []TenantMember {
	rows, err := m.DB.QueryContext(ctx, `
SELECT tenant_id, user_id, role, joined_at_ms, source FROM tenant_members WHERE tenant_id = ? ORDER BY joined_at_ms ASC`, tenantID)
	if err != nil {
		return nil
	}
	return scanTenantMembers(rows)
}

// ListMembershipsForUser returns every tenant membership of a user.
func (m *MySQLTenant) ListMembershipsForUser(ctx context.Context, userID string) []TenantMember {
	rows, err := m.DB.QueryContext(ctx, `
SELECT tenant_id, user_id, role, joined_at_ms, source FROM tenant_members WHERE user_id = ? ORDER BY joined_at_ms ASC`, userID)
	if err != nil {
		return nil
	}
	return scanTenantMembers(rows)
}

// scanTenantMembers reads tenant_id, user_id, role, joined_at_ms, source rows.
func scanTenantMembers(rows *sql.Rows) []TenantMember {
	defer rows.Close()
	var out []TenantMember
	for rows.Next() {
		var mem TenantMember
		var role, source string
		if err := rows.Scan(&mem.TenantID, &mem.UserID, &role, &mem.JoinedAtMs, &source); err != nil {
			return out
		}
		mem.Role = OrgRole(role)
		mem.Source = MemberSource(source)
		out = append(out, mem)
	}
	return out
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
	// RowsAffected is 0 for an unchanged value too: the read tells a missing
	// tenant (ErrTenantNotFound) apart from a no-op update.
	return m.GetTenant(ctx, tenantID)
}

// nullableBool maps an unset switch to SQL NULL.
func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func (m *MySQLTenant) TenantForIssuer(ctx context.Context, issuer string) (string, bool) {
	if issuer == "" {
		return "", false
	}
	var id string
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id FROM tenants WHERE sso_issuer = ? LIMIT 1`, issuer).Scan(&id)
	if err != nil || id == "" {
		return "", false
	}
	return id, true
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

func (m *MySQLTenant) GetMember(ctx context.Context, tenantID, userID string) (TenantMember, bool) {
	var mem TenantMember
	var role, source string
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id, user_id, role, joined_at_ms, source FROM tenant_members WHERE tenant_id = ? AND user_id = ?`,
		tenantID, userID).Scan(&mem.TenantID, &mem.UserID, &role, &mem.JoinedAtMs, &source)
	if err != nil {
		return TenantMember{}, false
	}
	mem.Role = OrgRole(role)
	mem.Source = MemberSource(source)
	return mem, true
}

func (m *MySQLTenant) MemberCount(ctx context.Context, tenantID string) int {
	var n int
	if err := m.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM tenant_members WHERE tenant_id = ?`, tenantID).Scan(&n); err != nil {
		return 0
	}
	return n
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

func (m *MySQLTenant) GetJoinRequest(ctx context.Context, tenantID, userID string) (JoinRequest, bool) {
	var jr JoinRequest
	var status string
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id, user_id, message, status, created_at_ms, decided_at_ms, decided_by
FROM tenant_join_requests WHERE tenant_id = ? AND user_id = ?`, tenantID, userID).
		Scan(&jr.TenantID, &jr.UserID, &jr.Message, &status, &jr.CreatedAtMs, &jr.DecidedAtMs, &jr.DecidedBy)
	if err != nil {
		return JoinRequest{}, false
	}
	jr.Status = JoinRequestStatus(status)
	return jr, true
}

func (m *MySQLTenant) ListJoinRequests(ctx context.Context, tenantID string, status JoinRequestStatus) []JoinRequest {
	q := `SELECT tenant_id, user_id, message, status, created_at_ms, decided_at_ms, decided_by
FROM tenant_join_requests WHERE tenant_id = ?`
	args := []any{tenantID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, string(status))
	}
	q += ` ORDER BY created_at_ms ASC`
	return m.scanJoinRequests(ctx, q, args...)
}

func (m *MySQLTenant) ListJoinRequestsForUser(ctx context.Context, userID string) []JoinRequest {
	return m.scanJoinRequests(ctx, `
SELECT tenant_id, user_id, message, status, created_at_ms, decided_at_ms, decided_by
FROM tenant_join_requests WHERE user_id = ? ORDER BY created_at_ms DESC`, userID)
}

func (m *MySQLTenant) scanJoinRequests(ctx context.Context, query string, args ...any) []JoinRequest {
	rows, err := m.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []JoinRequest
	for rows.Next() {
		var jr JoinRequest
		var status string
		if err := rows.Scan(&jr.TenantID, &jr.UserID, &jr.Message, &status, &jr.CreatedAtMs, &jr.DecidedAtMs, &jr.DecidedBy); err != nil {
			return out
		}
		jr.Status = JoinRequestStatus(status)
		out = append(out, jr)
	}
	return out
}

func (m *MySQLTenant) PrimaryTenantForUser(ctx context.Context, userID string) (string, bool) {
	var tid string
	// A real org membership outranks the "default" catch-all (auto-provisioned
	// on first login); within the same class the most-recently-joined wins.
	err := m.DB.QueryRowContext(ctx, `
SELECT tenant_id FROM tenant_members WHERE user_id = ?
ORDER BY (tenant_id = 'default') ASC, joined_at_ms DESC LIMIT 1`, userID).Scan(&tid)
	if err != nil {
		return "", false
	}
	return tid, true
}
