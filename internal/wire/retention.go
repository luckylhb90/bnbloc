package wire

import "fmt"

// Data retention (docs/prd/2026-10-10-enterprise-scale.md §3). A platform
// default (super-admin) and a per-tenant override (org admin) say, per kind
// of data, how many days it is kept. Unset everywhere means keep forever.

// RetentionKind is one kind of data a retention policy covers.
type RetentionKind string

// Retention kinds.
const (
	// RetentionMessages: room messages (chat and agent replies, with their
	// message tasks), the activity copy of agent replies, and chat
	// attachments, by age.
	RetentionMessages RetentionKind = "messages"
	// RetentionArtifactVersions: artifact versions other than the latest
	// (with the share links pinned to them). The latest version is never
	// deleted.
	RetentionArtifactVersions RetentionKind = "artifactVersions"
	// RetentionAudit: the audit trails (borrow decisions, component and
	// administration events).
	RetentionAudit RetentionKind = "audit"
	// RetentionAgentJobs: agent jobs that finished (replied, failed, timed
	// out or cancelled). Queued and running jobs are never deleted.
	RetentionAgentJobs RetentionKind = "agentJobs"
)

// RetentionKinds lists every kind, in display order.
var RetentionKinds = []RetentionKind{RetentionMessages, RetentionArtifactVersions, RetentionAudit, RetentionAgentJobs}

// Retention bounds. The minimum is one month because monthly usage budgets
// and the borrow usage reports read the current month's agent replies and
// audit rows; a shorter period would undercount them. They are properties
// of the product (the budget period), not of a deployment.
const (
	RetentionMinDays = 31
	RetentionMaxDays = 36500
)

// RetentionLimits tells a client the accepted range of a days value.
type RetentionLimits struct {
	MinDays int `json:"minDays"`
	MaxDays int `json:"maxDays"`
}

// CurrentRetentionLimits is the range every policy write is validated
// against.
func CurrentRetentionLimits() RetentionLimits {
	return RetentionLimits{MinDays: RetentionMinDays, MaxDays: RetentionMaxDays}
}

// RetentionPolicy holds the days of each kind. What an unset (null) value
// means depends on the layer:
//
//   - the platform default: null keeps forever (0 is accepted on write and
//     stored as null);
//   - a tenant override: null uses the platform default, 0 keeps forever
//     (overriding a platform default), N deletes after N days.
//
// The effective policy (EffectiveRetention) holds null for keep forever.
type RetentionPolicy struct {
	MessagesDays         *int `json:"messagesDays"`
	ArtifactVersionsDays *int `json:"artifactVersionsDays"`
	AuditDays            *int `json:"auditDays"`
	AgentJobsDays        *int `json:"agentJobsDays"`
}

// Days returns the value of kind (nil when unset or unknown).
func (p RetentionPolicy) Days(kind RetentionKind) *int {
	switch kind {
	case RetentionMessages:
		return p.MessagesDays
	case RetentionArtifactVersions:
		return p.ArtifactVersionsDays
	case RetentionAudit:
		return p.AuditDays
	case RetentionAgentJobs:
		return p.AgentJobsDays
	}
	return nil
}

// With returns p with kind set to days.
func (p RetentionPolicy) With(kind RetentionKind, days *int) RetentionPolicy {
	switch kind {
	case RetentionMessages:
		p.MessagesDays = days
	case RetentionArtifactVersions:
		p.ArtifactVersionsDays = days
	case RetentionAudit:
		p.AuditDays = days
	case RetentionAgentJobs:
		p.AgentJobsDays = days
	}
	return p
}

// Validate checks every set value: 0 (keep forever) or a day count within
// [RetentionMinDays, RetentionMaxDays].
func (p RetentionPolicy) Validate() error {
	for _, k := range RetentionKinds {
		d := p.Days(k)
		if d == nil || *d == 0 {
			continue
		}
		if *d < 0 {
			return fmt.Errorf("%s: days must not be negative", k)
		}
		if *d < RetentionMinDays || *d > RetentionMaxDays {
			return fmt.Errorf("%s: days must be between %d and %d (or 0 to keep forever)", k, RetentionMinDays, RetentionMaxDays)
		}
	}
	return nil
}

// AsDefault normalizes a platform default: 0 (keep forever) is stored as
// null, the platform layer's keep-forever value.
func (p RetentionPolicy) AsDefault() RetentionPolicy {
	out := RetentionPolicy{}
	for _, k := range RetentionKinds {
		if d := p.Days(k); d != nil && *d > 0 {
			v := *d
			out = out.With(k, &v)
		}
	}
	return out
}

// EffectiveRetention resolves the days that apply to a tenant: its override
// where set (0 = keep forever), else the platform default. The result holds
// null for keep forever and a positive day count otherwise.
func EffectiveRetention(platform, override RetentionPolicy) RetentionPolicy {
	out := RetentionPolicy{}
	for _, k := range RetentionKinds {
		d := override.Days(k)
		if d == nil {
			d = platform.Days(k)
		}
		if d != nil && *d > 0 {
			v := *d
			out = out.With(k, &v)
		}
	}
	return out
}

// RetentionStatus is what the retention worker last did (platform-wide).
type RetentionStatus struct {
	// LastRunAtMs is when the last row-deletion pass finished (0 = never).
	LastRunAtMs int64 `json:"lastRunAtMs"`
	// LastBlobGCAtMs is when the last blob collection finished (0 = never).
	LastBlobGCAtMs int64 `json:"lastBlobGcAtMs"`
	// LastDeleted counts what the last pass deleted, by item (room
	// messages, attachments, blobs, ...).
	LastDeleted map[string]int64 `json:"lastDeleted"`
}

// PlatformRetentionView is GET/PUT /v1/platform/retention: the platform
// default (null = keep forever), the accepted range and the worker status.
type PlatformRetentionView struct {
	Default RetentionPolicy `json:"default"`
	Limits  RetentionLimits `json:"limits"`
	Status  RetentionStatus `json:"status"`
}

// TenantRetentionView is GET/PUT /v1/tenant/retention: the tenant's override
// (null = use the platform default, 0 = keep forever), the platform default
// it falls back to, the effective result and the accepted range.
type TenantRetentionView struct {
	TenantID  string          `json:"tenantId"`
	Override  RetentionPolicy `json:"override"`
	Default   RetentionPolicy `json:"default"`
	Effective RetentionPolicy `json:"effective"`
	Limits    RetentionLimits `json:"limits"`
}
