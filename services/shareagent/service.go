// Package shareagenthttp exposes the bnbloc-shareagent HTTP service: the
// shared-agent access controller endpoints, the credential-free access
// snapshot, and the turn proxy to the owner daemon. The owner credential never
// enters a bnbloc response or log.
package shareagenthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/shareagent"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// OwnerResolver returns the base URL of an owner daemon's proxy endpoint for a
// room+owner. Presence/device registries implement it.
type OwnerResolver interface {
	OwnerEndpoint(roomID, ownerUserID string) (string, bool)
}

// Membership authorizes a caller for a room (fail closed) through
// membership.RoomScope (requireMember), like every backend: the role, and
// whether the membership is a platform agent host's, come with it
// (membership.Member).
type Membership = membership.Directory

// Service holds the shareagent handler dependencies.
type Service struct {
	Controller *shareagent.Controller
	Grants     store.GrantStore
	Presence   shareagent.PresenceChecker
	Owners     OwnerResolver
	Membership Membership
	HTTP       *http.Client
	Now        func() int64
	// Tenants is the tenant registry, used by the org-admin console for member
	// management, quota get/set, and tenant settings. Its persisted per-tenant
	// run/cost ceilings are the ONLY source of org-level quota (resolved per
	// request from the ctx tenant); nil means no org ceilings.
	Tenants store.TenantStore
	// QuotaRequests holds borrowers' requests for more runs (F3). nil answers
	// the quota-request routes with 501.
	QuotaRequests store.QuotaRequestStore
	// Notifications tells an agent owner about a new borrow quota request
	// (F5; best-effort, never blocks the request). nil sends nothing.
	Notifications BorrowNotifier
	// RefreshTokens is the gateway's refresh-family store (shared MySQL):
	// removing a member, suspending a tenant, or an admin's "sign out member"
	// revokes families there so the gateway refuses their next refresh. nil
	// skips revocation (tests).
	RefreshTokens store.RefreshTokenStore
	// PushSubscriptions holds members' browser push subscriptions (N4):
	// removing a member from a tenant deletes theirs for that tenant. nil
	// skips the cleanup (tests).
	PushSubscriptions store.WebPushSubscriptionStore
	// ExtAudit receives member/tenant administration and session-revocation
	// events (R13; nil = not recorded). AuditLog is every audit trail's read
	// side, merged by the audit log routes (empty = 501).
	ExtAudit store.ExtensionAuditStore
	AuditLog []store.AuditLogReader
	// Metrics counts audited borrow decisions (nil = off).
	Metrics *obs.Metrics
	// Settings holds the platform-agent policy (platform setting
	// platform_agents); Bridges is the bridge queue that says whether a
	// platform agent's host is online and how many jobs wait for it;
	// AgentHosts adds/removes the host's agent-host room membership. Nil
	// Settings (or Tenants) answers the platform-agent routes 501 and refuses
	// every platform grant.
	Settings   store.PlatformSettingsStore
	Bridges    BridgeAgents
	AgentHosts AgentHosts
	// RoomName resolves a room's name in the ctx tenant ("" when unknown;
	// nil = names left empty): the platform-agent usage dashboard.
	RoomName func(ctx context.Context, roomID string) string
}

// requireMember verifies the authenticated caller's room membership with the
// ability need (membership.RoomScope, in the request's tenant), failing
// closed. It writes the error response and returns ok=false when the caller
// is missing or refused.
func (s *Service) requireMember(w http.ResponseWriter, r *http.Request, roomID, caller string, need membership.Need) (membership.Member, bool) {
	if caller == "" {
		httpx.Error(w, "missing authenticated subject", http.StatusUnauthorized)
		return membership.Member{}, false
	}
	m, err := membership.RoomScope(r.Context(), s.Membership, roomID, caller, need)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusForbidden)
		return membership.Member{}, false
	}
	return m, true
}

// Handler builds the HTTP mux for the shareagent service.
func (s *Service) Handler() http.Handler {
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if s.Now == nil {
		s.Now = func() int64 { return time.Now().UnixMilli() }
	}
	mux := http.NewServeMux()
	for _, action := range []string{"start", "resume", "settings", "turn", "release"} {
		mux.HandleFunc("POST /v1/rooms/{roomId}/shared-agents/{targetId}/"+action, s.handleAction(action))
	}
	mux.HandleFunc("GET /v1/rooms/{roomId}/shared-agents/{targetId}/access", s.handleAccess)
	// Owner pre-authorization: an owner authorizes room-wide borrowing of their
	// agent; the driver (bridge) and borrower client consult it. The legacy
	// self-grant create path is removed: a grantee must never authorize itself.
	mux.HandleFunc("PUT /v1/rooms/{roomId}/shared-agents/{targetId}/authorization", s.handleAuthorize)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/shared-agents/{targetId}/authorization", s.handleDeauthorize)
	mux.HandleFunc("GET /v1/rooms/{roomId}/shared-agents/{targetId}/authorization", s.handleAuthorization)
	// Exact borrow quota (R8): the owner's bridge reserves a borrowed run
	// before driving it and settles the reservation with the run's usage
	// after; the owner's usage view is below.
	mux.HandleFunc("POST /v1/rooms/{roomId}/shared-agents/{targetId}/authorization/reserve", s.handleReserveBorrow)
	mux.HandleFunc("POST /v1/rooms/{roomId}/shared-agents/{targetId}/authorization/record", s.handleRecordBorrow)
	// Org-wide audit trail: the tenant's shared-agent audit records across ALL
	// rooms, for an org_admin's audit view. RBAC gating for this path is added
	// at the gateway; this endpoint returns the ctx tenant's records.
	// Org-admin console (RBAC manage_org enforced at the gateway for /v1/tenant/*):
	// member list + role change, add/remove member, quota get/set, and tenant
	// settings get/update.
	mux.HandleFunc("GET /v1/tenant/members", s.handleTenantMembers)
	// Platform console (RBAC manage_platform enforced at the gateway for
	// /v1/platform/*): cross-tenant list of all tenants for a super-admin.
	mux.HandleFunc("GET /v1/platform/tenants", s.handlePlatformTenants)
	mux.HandleFunc("POST /v1/platform/tenants", s.handlePlatformCreateTenant)
	mux.HandleFunc("PUT /v1/platform/tenants/{tenantId}/status", s.handlePlatformSetTenantStatus)
	mux.HandleFunc("POST /v1/tenant/members", s.handleTenantAddMember)
	mux.HandleFunc("DELETE /v1/tenant/members/{userId}", s.handleTenantRemoveMember)
	mux.HandleFunc("DELETE /v1/tenant/members/{userId}/sessions", s.handleTenantRevokeMemberSessions)
	mux.HandleFunc("PUT /v1/tenant/members/{userId}/role", s.handleTenantSetMemberRole)
	mux.HandleFunc("PUT /v1/tenant/quota", s.handleTenantSetQuota)
	mux.HandleFunc("GET /v1/tenant/settings", s.handleTenantSettings)
	// The tenant policy switches, read-only for members (the owner's bridge
	// enforces the sandbox switch per borrowed drive; the web hides sharing
	// while public links are disabled).
	mux.HandleFunc("GET /v1/my/tenant-policy", s.handleMyTenantPolicy)
	mux.HandleFunc("PUT /v1/tenant/settings", s.handleTenantUpdateSettings)
	mux.HandleFunc("GET /v1/tenant/audit", s.handleTenantAudit)
	// Audit log page (R13): every audit trail merged, the ctx tenant only;
	// the platform route spans tenants (manage_platform at the gateway).
	mux.HandleFunc("GET /v1/tenant/audit-log", s.handleTenantAuditLog)
	mux.HandleFunc("GET /v1/platform/audit-log", s.handlePlatformAuditLog)
	// Self-service join requests. User surface (/v1/tenants/*, plural) is open to
	// any authenticated user — a non-member browsing/applying is the point, so
	// the gateway gates these at PermRead only. Admin review surface
	// (/v1/tenant/join-requests/*, singular) is manage_org, scoped to the ctx
	// tenant like the other admin endpoints.
	mux.HandleFunc("GET /v1/tenants/directory", s.handleTenantDirectory)
	mux.HandleFunc("GET /v1/tenants/join-requests", s.handleMyJoinRequests)
	mux.HandleFunc("POST /v1/tenants/{tenantId}/join-request", s.handleCreateJoinRequest)
	mux.HandleFunc("GET /v1/tenant/join-requests", s.handleListJoinRequests)
	mux.HandleFunc("POST /v1/tenant/join-requests/{userId}/approve", s.handleApproveJoinRequest)
	mux.HandleFunc("POST /v1/tenant/join-requests/{userId}/reject", s.handleRejectJoinRequest)
	mux.HandleFunc("GET /v1/rooms/{roomId}/shared-agents-usage", s.handleOwnerAuthorizations)
	// Org-level usage: aggregate borrow accounting across the tenant's grants,
	// with any configured run/cost ceilings so a UI can show remaining.
	mux.HandleFunc("GET /v1/tenant/usage", s.handleTenantUsage)
	// Room shared-agent discovery: list ACTIVE authorizations in a room so
	// browser users with no local core can find borrowable agents. The path
	// segment carries no {targetId}, so it does not collide with the per-target
	// routes above.
	mux.HandleFunc("GET /v1/rooms/{roomId}/shared-agents", s.handleListSharedAgents)
	// Borrow billing (F3): caller-scoped usage report (JSON/CSV), the
	// tenant-wide report (manage_org at the gateway), and quota requests.
	mux.HandleFunc("GET /v1/borrow/usage-report", s.handleUsageReport)
	mux.HandleFunc("GET /v1/tenant/borrow/usage-report", s.handleTenantUsageReport)
	mux.HandleFunc("POST /v1/borrow/quota-requests", s.handleCreateQuotaRequest)
	mux.HandleFunc("GET /v1/borrow/quota-requests", s.handleListQuotaRequests)
	mux.HandleFunc("POST /v1/borrow/quota-requests/{requestId}/approve", s.handleApproveQuotaRequest)
	mux.HandleFunc("POST /v1/borrow/quota-requests/{requestId}/decline", s.handleDeclineQuotaRequest)
	// Platform agents: the super-admin policy (manage_platform at the
	// gateway), the published list for the caller's tenant, and a room's
	// added platform agents.
	mux.HandleFunc("GET /v1/platform/platform-agents", s.handleGetPlatformAgentsPolicy)
	mux.HandleFunc("PUT /v1/platform/platform-agents", s.handlePutPlatformAgentsPolicy)
	mux.HandleFunc("GET /v1/platform/platform-agents/usage", s.handlePlatformAgentsUsage)
	// Data retention: the platform default (manage_platform) and the ctx
	// tenant's override (manage_org), read by realtime's retention worker.
	mux.HandleFunc("GET /v1/platform/retention", s.handleGetPlatformRetention)
	mux.HandleFunc("PUT /v1/platform/retention", s.handlePutPlatformRetention)
	mux.HandleFunc("GET /v1/tenant/retention", s.handleGetTenantRetention)
	mux.HandleFunc("PUT /v1/tenant/retention", s.handlePutTenantRetention)
	mux.HandleFunc("GET /v1/platform-agents", s.handleListPlatformAgents)
	mux.HandleFunc("GET /v1/rooms/{roomId}/platform-agents", s.handleListRoomPlatformAgents)
	mux.HandleFunc("POST /v1/rooms/{roomId}/platform-agents", s.handleAddRoomPlatformAgent)
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/platform-agents/{provider}", s.handleRemoveRoomPlatformAgent)
	return svcauth.EnsureTenant(mux)
}

type actionBody struct {
	GrantID        string  `json:"grantId"`
	ModelPlanID    string  `json:"modelPlanId"`
	Model          string  `json:"model"`
	TurnCapability string  `json:"turnCapability"`
	Cost           float64 `json:"cost"`
	LeaseID        string  `json:"leaseId"`
	PayloadRef     any     `json:"payloadRef"`
}

func (s *Service) handleAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID, err := roomctx.Normalize(r.PathValue("roomId"))
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		actor := svcauth.Subject(r)
		if _, ok := s.requireMember(w, r, roomID, actor, membership.AnyMember); !ok {
			return
		}
		var body actionBody
		if !httpx.DecodeJSON(w, r, httpx.DefaultBodyLimit, &body) {
			return
		}
		req := shareagent.Request{
			RoomID:         roomID,
			TargetID:       r.PathValue("targetId"),
			GrantID:        body.GrantID,
			ActorUserID:    actor,
			Action:         shareagent.Action(action),
			ModelPlanID:    body.ModelPlanID,
			Model:          body.Model,
			TurnCapability: body.TurnCapability,
			Cost:           body.Cost,
		}
		// Release drops the prior lease and audits.
		if req.Action == shareagent.ActionRelease {
			decision := s.Controller.Authorize(r.Context(), req)
			if body.LeaseID != "" {
				s.Controller.Release(r.Context(), body.LeaseID)
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"accepted": decision.Allowed})
			return
		}

		decision := s.Controller.Authorize(r.Context(), req)
		if !decision.Allowed {
			// Fail closed with the stable deny reason.
			httpx.WriteJSON(w, http.StatusForbidden, map[string]string{"denyReason": decision.DenyReason})
			return
		}
		// For a turn, proxy to the owner daemon. The request carries model/
		// capability only; no credential is ever forwarded from or returned by
		// bnbloc.
		if req.Action == shareagent.ActionTurn {
			result, proxyErr := s.proxyTurn(r.Context(), roomID, decision.OwnerUserID, body)
			if proxyErr != nil {
				s.Controller.Release(r.Context(), decision.LeaseID)
				obs.ServerError(w, "grant action failed", proxyErr)
				httpx.Error(w, "owner turn proxy failed", http.StatusBadGateway)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"accepted": true, "leaseId": decision.LeaseID, "result": result})
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"accepted": true, "leaseId": decision.LeaseID})
	}
}

// proxyTurn forwards the turn to the owner daemon and returns its opaque
// result. It forwards only model/capability/payloadRef and never a credential.
func (s *Service) proxyTurn(ctx context.Context, roomID, ownerUserID string, body actionBody) (any, error) {
	endpoint, ok := s.Owners.OwnerEndpoint(roomID, ownerUserID)
	if !ok {
		return nil, errOwnerUnavailable
	}
	forward := map[string]any{
		"roomId":         roomID,
		"modelPlanId":    body.ModelPlanID,
		"model":          body.Model,
		"turnCapability": body.TurnCapability,
		"payloadRef":     body.PayloadRef,
	}
	buf, _ := json.Marshal(forward)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errOwnerUnavailable
	}
	var out any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

var errOwnerUnavailable = &proxyError{"owner endpoint unavailable"}

type proxyError struct{ msg string }

func (e *proxyError) Error() string { return e.msg }
