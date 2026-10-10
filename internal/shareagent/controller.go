// Package shareagent implements the cross-user Agent borrowing authority: the
// SharedAgentAccessController and Auditor contracts. Owner credentials never
// enter this package; it authorizes and (in the service layer) proxies turns.
// Every lifecycle action re-reads current authority state and fails closed.
package shareagent

import (
	"context"
	"errors"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Action is a shared-agent lifecycle action.
type Action string

// Lifecycle actions.
const (
	ActionStart    Action = "start"
	ActionResume   Action = "resume"
	ActionSettings Action = "settings"
	ActionTurn     Action = "turn"
	ActionRelease  Action = "release"
)

// Deny reason codes (stable, mirrored in the PRD wire contract).
const (
	DenyOwnerOffline     = "owner_offline"
	DenyQuotaExhausted   = "quota_exhausted"
	DenyCostExhausted    = "cost_exhausted"
	DenyConcurrencyFull  = "concurrency_full"
	DenyModelOutOfRange  = "model_out_of_range"
	DenyCapabilityDenied = "capability_denied"
	DenyGrantInvalid     = "grant_invalid"
	DenyBindingRevoked   = "binding_revoked"
	// DenyPlatformAgentUnavailable: a platform grant whose agent is no longer
	// published for the room's organization.
	DenyPlatformAgentUnavailable = "platform_agent_unavailable"
	// DenySandboxRequired: a platform grant reserved for a drive its host's
	// bridge would not run in the agent sandbox (platform agents always run
	// sandboxed).
	DenySandboxRequired = "sandbox_required"
)

// PresenceChecker reports whether an owner is currently online. It is a live
// authority read, never a cached snapshot value.
type PresenceChecker interface {
	IsOwnerOnline(roomID, ownerUserID string) bool
}

// Request is one authorization request for a shared-agent action.
type Request struct {
	RoomID         string
	TargetID       string // trusted shared-agent:* identity
	GrantID        string
	ActorUserID    string
	Action         Action
	ModelPlanID    string
	Model          string
	TurnCapability string  // consult|review|delegate|upgrade (turn only)
	Cost           float64 // estimated cost for this turn
}

// Decision is the outcome of an authorization request.
type Decision struct {
	Allowed     bool
	DenyReason  string
	LeaseID     string
	OwnerUserID string
}

// Controller enforces shared-agent authorization. Missing store, presence, or
// audit dependencies cause every request to fail closed.
type Controller struct {
	Grants   store.GrantStore
	Presence PresenceChecker
	Audit    store.GrantStore // audit sink (same store; separated for clarity)
	// NewLeaseID generates a unique lease id; injectable for tests.
	NewLeaseID func() string
	Now        func() int64
	// Metrics counts audited decisions (nil = off).
	Metrics *obs.Metrics
}

// Authorize evaluates a request, records exactly one audit decision, and (for
// start/resume/turn) acquires a concurrency lease. It fails closed on any
// missing dependency or unmet condition.
func (c *Controller) Authorize(ctx context.Context, req Request) Decision {
	// Fail closed if the controller is not fully wired.
	if c.Grants == nil || c.Presence == nil || c.Audit == nil || c.NewLeaseID == nil || c.Now == nil {
		return c.deny(ctx, req, DenyGrantInvalid, "")
	}
	// Shared targets are classified from the trusted identity, not snapshot data.
	if !strings.HasPrefix(strings.TrimSpace(req.TargetID), "shared-agent:") {
		return c.deny(ctx, req, DenyGrantInvalid, "")
	}
	grant, err := c.Grants.GetGrant(ctx, req.GrantID)
	if err != nil || grant.RoomID != req.RoomID || grant.TargetID != req.TargetID || grant.GranteeUserID != req.ActorUserID {
		return c.deny(ctx, req, DenyGrantInvalid, "")
	}
	if grant.RevokedAtMs != nil {
		return c.deny(ctx, req, DenyBindingRevoked, grant.OwnerUserID)
	}
	// TTL: an expired grant is invalid; borrowing must be re-approved.
	if grant.ExpiresAtMs != nil && c.Now() >= *grant.ExpiresAtMs {
		return c.deny(ctx, req, DenyGrantInvalid, grant.OwnerUserID)
	}

	// Release audits and returns; the concurrency lease is dropped by the
	// service layer via Controller.Release using the lease id from the earlier
	// allow. Release performs no quota/presence checks.
	if req.Action == ActionRelease {
		c.audit(ctx, req, "allow", "", grant.OwnerUserID)
		return Decision{Allowed: true, OwnerUserID: grant.OwnerUserID}
	}

	// Live owner presence is re-read every action.
	if !c.Presence.IsOwnerOnline(req.RoomID, grant.OwnerUserID) {
		return c.deny(ctx, req, DenyOwnerOffline, grant.OwnerUserID)
	}
	// Model range (turn/start/resume/settings all carry a selected model).
	if req.Model != "" && !modelInRange(grant.AllowedModels, req.ModelPlanID, req.Model) {
		return c.deny(ctx, req, DenyModelOutOfRange, grant.OwnerUserID)
	}
	// Turn capability permission.
	if req.Action == ActionTurn && !capabilityAllowed(grant.Capabilities, req.TurnCapability) {
		return c.deny(ctx, req, DenyCapabilityDenied, grant.OwnerUserID)
	}
	// Run quota.
	if grant.RunQuota != nil && grant.RunsUsed >= *grant.RunQuota {
		return c.deny(ctx, req, DenyQuotaExhausted, grant.OwnerUserID)
	}
	// Cost allowance.
	if grant.CostAllowance != nil && grant.CostUsed+req.Cost > *grant.CostAllowance {
		return c.deny(ctx, req, DenyCostExhausted, grant.OwnerUserID)
	}

	// Acquire a concurrency lease atomically.
	leaseID := c.NewLeaseID()
	if err := c.Grants.AcquireLease(ctx, req.GrantID, leaseID, c.Now()); err != nil {
		return c.deny(ctx, req, DenyConcurrencyFull, grant.OwnerUserID)
	}
	// A turn consumes quota/cost once admitted.
	if req.Action == ActionTurn {
		if _, err := c.Grants.ConsumeRun(ctx, req.GrantID, req.Cost); err != nil {
			_ = c.Grants.ReleaseLease(ctx, leaseID, c.Now())
			return c.deny(ctx, req, ConsumeDenyReason(err), grant.OwnerUserID)
		}
	}
	c.audit(ctx, req, "allow", "", grant.OwnerUserID)
	return Decision{Allowed: true, LeaseID: leaseID, OwnerUserID: grant.OwnerUserID}
}

// ConsumeDenyReason maps an atomic ConsumeRun / ReserveRun refusal to its deny
// reason. The pre-checks above read a snapshot; a concurrent turn can still
// exhaust the ceiling before this turn consumes, and the member must see the
// real reason (quota / cost), not "grant invalid".
func ConsumeDenyReason(err error) string {
	switch {
	case errors.Is(err, store.ErrRunQuotaExhausted):
		return DenyQuotaExhausted
	case errors.Is(err, store.ErrCostAllowanceExhausted):
		return DenyCostExhausted
	default:
		return DenyGrantInvalid
	}
}

// Release drops a concurrency lease acquired by an earlier allow.
func (c *Controller) Release(ctx context.Context, leaseID string) {
	if c.Grants != nil && c.Now != nil {
		_ = c.Grants.ReleaseLease(ctx, leaseID, c.Now())
	}
}

func (c *Controller) deny(ctx context.Context, req Request, reason, owner string) Decision {
	c.audit(ctx, req, "deny", reason, owner)
	return Decision{Allowed: false, DenyReason: reason, OwnerUserID: owner}
}

func (c *Controller) audit(ctx context.Context, req Request, decision, reason, owner string) {
	if req.Action != ActionRelease {
		RecordDecision(c.Metrics, decision, reason)
	}
	if c.Audit == nil || c.Now == nil {
		return
	}
	c.Audit.AppendAudit(ctx, store.AuditRecord{
		RoomID:      req.RoomID,
		GrantID:     req.GrantID,
		ActorUserID: req.ActorUserID,
		OwnerUserID: owner,
		Action:      string(req.Action),
		Decision:    decision,
		DenyReason:  reason,
		Model:       req.Model,
		AtMs:        c.Now(),
	})
}

func modelInRange(allowed []store.AllowedModel, planID, model string) bool {
	// An empty range is "no model restriction": the owner authorized the agent
	// without narrowing its models (there is no hardcoded default list).
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a.Model != model {
			continue
		}
		// A plan-less entry matches every otherwise-compatible plan.
		if a.ModelPlanID == "" || a.ModelPlanID == planID {
			return true
		}
	}
	return false
}

func capabilityAllowed(caps store.Capabilities, capability string) bool {
	switch capability {
	case "consult":
		return caps.Consult
	case "review":
		return caps.Review
	case "delegate":
		return caps.Delegate
	case "upgrade":
		return caps.Upgrade
	case "": // an unqualified turn requires at least consult
		return caps.Consult
	default:
		return false
	}
}
