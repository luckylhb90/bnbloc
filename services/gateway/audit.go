package gateway

import (
	"context"
	"log/slog"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// audit appends one row to the audit trail (R13): the one way a gateway
// handler records an action. The row lands in tenantID — the organization
// whose admins see it, never empty (a platform-level action is recorded in
// the tenant the super admin acts in) — at the current time. Best-effort:
// the action already happened, so a failed write is logged, not returned.
func (s *Service) audit(ctx context.Context, tenantID string, rec store.ExtAuditRecord) {
	if s.ExtAudit == nil {
		return
	}
	rec.TenantID = tenantID
	rec.AtMs = s.nowTime().UnixMilli()
	if err := s.ExtAudit.AppendExtAudit(ctx, rec); err != nil {
		slog.Warn("bnbloc-gateway: audit write failed", "event", "bnbloc.gateway.audit_failed",
			"kind", rec.Kind, "name", rec.Name, "action", rec.Action, "error", err)
	}
}
