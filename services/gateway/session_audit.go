package gateway

import (
	"context"
	"log/slog"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// auditSessionRevoke records a revoked sign-in (refresh family) in the audit
// trail (R13): a member's own sign-out or session revocation, and a refresh
// token reuse that revoked its family. The row carries the family's tenant so
// that tenant's admins see it. Best-effort: the revocation already happened.
func (s *Service) auditSessionRevoke(ctx context.Context, fam store.RefreshFamily, action, actor, outcome string) {
	if s.ExtAudit == nil {
		return
	}
	detail := "client=" + fam.Client
	if fam.ParentFamilyID != "" {
		detail += " parent=" + fam.ParentFamilyID
	}
	if err := s.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: wire.AuditKindSession, Name: fam.FamilyID, Action: action, Actor: actor,
		TenantID: fam.TenantID, Outcome: outcome, Detail: "subject=" + fam.Subject + " " + detail,
		AtMs: s.nowTime().UnixMilli(),
	}); err != nil {
		slog.Warn("bnbloc gateway: session audit failed", "event", "bnbloc.gateway.session_audit_failed", "action", action, "error", err)
	}
}

// auditSessionRevokeByID looks the family up first (its tenant and client
// are not in the caller's hands).
func (s *Service) auditSessionRevokeByID(ctx context.Context, familyID, action, actor string) {
	if s.ExtAudit == nil {
		return
	}
	fam, ok, err := s.refreshTokens().GetRefreshFamily(ctx, familyID)
	if err != nil || !ok {
		fam = store.RefreshFamily{FamilyID: familyID, Subject: actor}
	}
	s.auditSessionRevoke(ctx, fam, action, actor, "revoked")
}
