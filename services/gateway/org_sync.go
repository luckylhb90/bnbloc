package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/authprovider"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/orgsync"
)

// Organization sync (R11): super-admin mapping rules turn the directory
// attributes an auth provider asserts (groups, department, email domain)
// into tenant memberships, org roles and default rooms. The rules apply at
// every SSO sign-in (handleAuthProviderACS); a directory feed deprovisions
// departed users and re-applies changed rules through the routes below.

// orgSyncSignInActor attributes sign-in-time changes in the audit trail.
const orgSyncSignInActor = "org-sync:sign-in"

// maxOrgSyncBodyBytes bounds a rule set / preview / deprovision body.
const maxOrgSyncBodyBytes = 256 * 1024

// maxDeprovisionUsers bounds one deprovision call.
const maxDeprovisionUsers = 1000

func (s *Service) registerOrgSync(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/platform/org-sync/rules", s.handleOrgSyncRules)
	mux.HandleFunc("PUT /v1/platform/org-sync/rules", s.handleOrgSyncPutRules)
	mux.HandleFunc("POST /v1/platform/org-sync/preview", s.handleOrgSyncPreview)
	mux.HandleFunc("POST /v1/platform/org-sync/deprovision", s.handleOrgSyncDeprovision)
	mux.HandleFunc("POST /v1/platform/org-sync/resync", s.handleOrgSyncResync)
}

// applyOrgSync converges the signing-in subject's memberships and returns
// the landing tenant ("" when no rule matched or sync is not configured).
func (s *Service) applyOrgSync(ctx context.Context, identity auth.Identity, a authprovider.Assertion) (string, error) {
	if s.OrgSync == nil {
		return "", nil
	}
	res, err := s.OrgSync.Apply(ctx, orgsync.Attributes{
		UserID: identity.Subject, Email: identity.Email, DisplayName: a.DisplayName,
		Department: a.Department, Groups: a.Groups,
	}, orgSyncSignInActor, false)
	if err != nil {
		return "", err
	}
	if len(res.Changes) > 0 {
		slog.Info("bnbloc gateway: org sync applied at sign-in",
			"event", "bnbloc.gateway.org_sync_applied", "subject", identity.Subject, "changes", len(res.Changes))
	}
	return res.Tenant, nil
}

// orgSyncAdmin authenticates a super-admin and checks sync is configured.
func (s *Service) orgSyncAdmin(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	claims, ok := s.requireSuperAdminClaims(w, r)
	if !ok {
		return auth.Claims{}, false
	}
	if s.OrgSync == nil {
		obs.ServerError(w, "organization sync not configured (no tenant store)", nil)
		http.Error(w, "organization sync not configured (no tenant store)", http.StatusNotImplemented)
		return auth.Claims{}, false
	}
	return claims, true
}

func decodeOrgSyncBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxOrgSyncBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return false
	}
	return true
}

// orgSyncFailed answers a 5xx and logs its cause.
func orgSyncFailed(w http.ResponseWriter, op string, err error) {
	obs.ServerError(w, "org sync "+op, err)
	http.Error(w, "organization sync "+op+" failed", http.StatusInternalServerError)
}

func (s *Service) handleOrgSyncRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.orgSyncAdmin(w, r); !ok {
		return
	}
	set, err := s.OrgSync.Rules(r.Context())
	if err != nil {
		orgSyncFailed(w, "read rules", err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Service) handleOrgSyncPutRules(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.orgSyncAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		Rules []orgsync.Rule `json:"rules"`
	}
	if !decodeOrgSyncBody(w, r, &body) {
		return
	}
	set, err := s.OrgSync.SaveRules(r.Context(), body.Rules, claims.Subject)
	var invalid *orgsync.ValidationError
	if errors.As(err, &invalid) {
		http.Error(w, invalid.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		orgSyncFailed(w, "save rules", err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

type orgSyncPreviewBody struct {
	// UserID defaults to Email (the subject an SSO sign-in without a
	// separate subject asserts).
	UserID     string   `json:"userId,omitempty"`
	Email      string   `json:"email"`
	Groups     []string `json:"groups,omitempty"`
	Department string   `json:"department,omitempty"`
}

// handleOrgSyncPreview is the dry run: what a sign-in with these attributes
// would change, against the saved rules and the user's current memberships.
func (s *Service) handleOrgSyncPreview(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.orgSyncAdmin(w, r)
	if !ok {
		return
	}
	var body orgSyncPreviewBody
	if !decodeOrgSyncBody(w, r, &body) {
		return
	}
	userID := strings.TrimSpace(body.UserID)
	if userID == "" {
		userID = strings.TrimSpace(body.Email)
	}
	if userID == "" {
		http.Error(w, "email or userId required", http.StatusBadRequest)
		return
	}
	res, err := s.OrgSync.Apply(r.Context(), orgsync.Attributes{
		UserID: userID, Email: strings.TrimSpace(body.Email), Department: body.Department, Groups: body.Groups,
	}, claims.Subject, true)
	if err != nil {
		orgSyncFailed(w, "preview", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleOrgSyncDeprovision is the directory feed's (or an operator's) way to
// end a departed user's access: a sign-in protocol never learns that a user stopped
// signing in.
func (s *Service) handleOrgSyncDeprovision(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.orgSyncAdmin(w, r)
	if !ok {
		return
	}
	var body struct {
		UserIDs []string `json:"userIds"`
	}
	if !decodeOrgSyncBody(w, r, &body) {
		return
	}
	if len(body.UserIDs) == 0 || len(body.UserIDs) > maxDeprovisionUsers {
		http.Error(w, "userIds must list 1 to 1000 users", http.StatusBadRequest)
		return
	}
	results, err := s.OrgSync.Deprovision(r.Context(), body.UserIDs, claims.Subject)
	if err != nil {
		orgSyncFailed(w, "deprovision", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleOrgSyncResync re-applies the saved rules to every subject a sign-in
// recorded (after a rule change, or on a schedule).
func (s *Service) handleOrgSyncResync(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.orgSyncAdmin(w, r)
	if !ok {
		return
	}
	res, err := s.OrgSync.Resync(r.Context(), claims.Subject)
	if err != nil {
		orgSyncFailed(w, "resync", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
