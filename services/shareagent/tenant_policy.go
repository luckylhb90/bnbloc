package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// tenantPolicyView is GET /v1/my/tenant-policy: the organization's name and
// the org-admin policy switches a member's surfaces follow — the owner's
// bridge (borrowed drives must run sandboxed) and the web (the share
// dialog offers no link anyone can open while anonymous links are
// disabled; publishing to the organization says whether an admin
// approves it).
type tenantPolicyView struct {
	TenantID                string `json:"tenantId"`
	Name                    string `json:"name"`
	RequireSandboxForBorrow bool   `json:"requireSandboxForBorrow"`
	AnonymousLinksDisabled  bool   `json:"anonymousLinksDisabled"`
	PublishApproval         string `json:"publishApproval"`
}

func viewTenantPolicy(t store.Tenant) tenantPolicyView {
	return tenantPolicyView{TenantID: t.TenantID, Name: t.Name, RequireSandboxForBorrow: t.RequireSandboxForBorrow, AnonymousLinksDisabled: t.AnonymousLinksDisabled, PublishApproval: t.PublishApproval}
}

// handleMyTenantPolicy answers the policy of ?tenant= (a tenant the caller is
// a member of; a bridge drives rooms of every tenant its owner belongs to, so
// a job's tenant need not be the token's) or of the request's tenant.
// Read-only: only org admins change it (PUT /v1/tenant/settings).
func (s *Service) handleMyTenantPolicy(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		httpx.Unavailable(w, "tenant registry")
		return
	}
	caller := svcauth.Subject(r)
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return
	}
	tid := tenant.FromContext(r.Context())
	if q := strings.TrimSpace(r.URL.Query().Get("tenant")); q != "" && q != tid {
		_, member, err := s.Tenants.GetMember(r.Context(), q, caller)
		if err != nil {
			obs.ServerError(w, "read membership failed", err)
			httpx.Error(w, "read membership failed", http.StatusInternalServerError)
			return
		}
		if !member {
			httpx.Error(w, "not a member of that tenant", http.StatusForbidden)
			return
		}
		tid = q
	}
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		httpx.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewTenantPolicy(t))
}
