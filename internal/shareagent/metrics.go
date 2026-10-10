package shareagent

import "github.com/luckylhb90/bnbloc/internal/obs"

// Borrow decision metric (docs/monitoring.md): every audited borrow decision
// — the lease path (Controller.Authorize) and the bridge's accountability
// record — by decision and reason.
const metricBorrowDecisions = "borrow_decisions_total"

// DenyOrgQuotaExhausted is the accountability record's org-level ceiling
// refusal (the tenant's aggregate borrow runs or cost reached).
const DenyOrgQuotaExhausted = "org_quota_exhausted"

// decisionReasons is the bounded reason label set; "none" for an allow.
var decisionReasons = map[string]bool{
	DenyOwnerOffline: true, DenyQuotaExhausted: true, DenyCostExhausted: true, DenyConcurrencyFull: true,
	DenyModelOutOfRange: true, DenyCapabilityDenied: true, DenyGrantInvalid: true, DenyBindingRevoked: true,
	DenyOrgQuotaExhausted: true, DenyPlatformAgentUnavailable: true, DenySandboxRequired: true,
}

// RegisterMetrics describes the borrow decision series and registers the
// allow series at zero.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(metricBorrowDecisions, "Audited borrow decisions by decision (allow, deny) and deny reason.")
	m.CounterAdd(metricBorrowDecisions, 0, obs.L("decision", "allow"), obs.L("reason", "none"))
}

// RecordDecision counts one audited borrow decision ("allow" or "deny").
func RecordDecision(m *obs.Metrics, decision, reason string) {
	if decision != "allow" && decision != "deny" {
		return
	}
	switch {
	case reason == "":
		reason = "none"
	case !decisionReasons[reason]:
		reason = "other"
	}
	m.CounterInc(metricBorrowDecisions, obs.L("decision", decision), obs.L("reason", reason))
}
