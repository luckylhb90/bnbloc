package gateway

import (
	"context"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// audit appends one row to the audit trail (R13) through the shared writer
// (internal/audit.Record: best effort, failures logged): the one way a
// gateway handler records an action. The row lands in tenantID — the
// organization whose admins see it, never empty (a platform-level action is
// recorded in the tenant the super admin acts in) — at the gateway's
// current time.
func (s *Service) audit(ctx context.Context, tenantID string, rec store.ExtAuditRecord) {
	rec.TenantID = tenantID
	rec.AtMs = s.nowTime().UnixMilli()
	audit.Record(ctx, s.ExtAudit, rec)
}
