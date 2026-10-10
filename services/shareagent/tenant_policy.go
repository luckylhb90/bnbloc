package shareagenthttp

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// tenantPolicyView is GET /v1/my/tenant-policy: the org-admin policy
// switches a member's surfaces follow — the owner's bridge (borrowed drives
// must run sandboxed) and the web (the share dialog is hidden while public
// links are disabled).
type tenantPolicyView struct {
	TenantID                string `json:"tenantId"`
	RequireSandboxForBorrow bool   `json:"requireSandboxForBorrow"`
	PublicSharesDisabled    bool   `json:"publicSharesDisabled"`
}

func viewTenantPolicy(t store.Tenant) tenantPolicyView {
	return tenantPolicyView{TenantID: t.TenantID, RequireSandboxForBorrow: t.RequireSandboxForBorrow, PublicSharesDisabled: t.PublicSharesDisabled}
}

// handleMyTenantPolicy answers the policy of ?tenant= (a tenant the caller is
// a member of; a bridge drives rooms of every tenant its owner belongs to, so
// a job's tenant need not be the token's) or of the request's tenant.
// Read-only: only org admins change it (PUT /v1/tenant/settings).
func (s *Service) handleMyTenantPolicy(w http.ResponseWriter, r *http.Request) {
	if s.Tenants == nil {
		obs.ServerError(w, "tenant registry not configured", nil)
		writeErr(w, http.StatusNotImplemented, "tenant registry not configured")
		return
	}
	caller := strings.TrimSpace(r.Header.Get("X-Bnbloc-User"))
	if caller == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return
	}
	tid := tenant.FromContext(r.Context())
	if q := strings.TrimSpace(r.URL.Query().Get("tenant")); q != "" && q != tid {
		if _, ok := s.Tenants.GetMember(r.Context(), q, caller); !ok {
			writeErr(w, http.StatusForbidden, "not a member of that tenant")
			return
		}
		tid = q
	}
	t, err := s.Tenants.GetTenant(r.Context(), tid)
	if err != nil {
		writeErr(w, http.StatusNotFound, "tenant not found")
		return
	}
	writeJSON(w, http.StatusOK, viewTenantPolicy(t))
}
