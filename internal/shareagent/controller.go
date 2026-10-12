// Package shareagent holds the cross-user agent borrowing rules shared by
// the services: the stable deny reasons of a refused borrow, the platform
// grant reading (platform.go) and the decision metrics. The authority itself
// is the reserve-then-settle path (services/shareagent reserve.go over
// store.RunReservationStore); owner credentials never enter this package.
package shareagent

import (
	"errors"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Deny reason codes (stable, mirrored in the PRD wire contract).
const (
	// DenyOwnerNotAuthorized: the owner holds no live room authorization for
	// the agent (none, revoked, or expired).
	DenyOwnerNotAuthorized = "owner_not_authorized"
	// DenyOrgQuotaExhausted: the tenant's org-level run/cost ceiling refused
	// the reservation.
	DenyOrgQuotaExhausted = "org_quota_exhausted"
	// DenyGrantInvalid: an unclassified refusal of the grant.
	DenyGrantInvalid   = "grant_invalid"
	DenyQuotaExhausted = "quota_exhausted"
	DenyCostExhausted  = "cost_exhausted"
	// DenyPlatformAgentUnavailable: a platform grant whose agent is no longer
	// published for the room's organization.
	DenyPlatformAgentUnavailable = "platform_agent_unavailable"
	// DenySandboxRequired: a platform grant reserved for a drive its host's
	// bridge would not run in the agent sandbox (platform agents always run
	// sandboxed).
	DenySandboxRequired = "sandbox_required"
)

// ReserveDenyReason maps an atomic ReserveRun ceiling refusal to its deny
// reason, so the member sees the real reason (quota / cost).
func ReserveDenyReason(err error) string {
	switch {
	case errors.Is(err, store.ErrRunQuotaExhausted):
		return DenyQuotaExhausted
	case errors.Is(err, store.ErrCostAllowanceExhausted):
		return DenyCostExhausted
	default:
		return DenyGrantInvalid
	}
}
