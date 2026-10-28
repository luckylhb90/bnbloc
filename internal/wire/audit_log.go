package wire

// Audit log DTOs (R13). shareagent serves them at GET /v1/tenant/audit-log
// (an org admin's own tenant) and GET /v1/platform/audit-log (super-admin,
// every tenant or ?tenant=). The JSON tags are the public contract the web
// client reads.

// TruncatedHeader names the response header an audit CSV export sets to
// the row cap when it stopped there (the gateway exposes it to
// cross-origin clients).
const TruncatedHeader = "X-Bnbloc-Truncated"

// Audit log entry kinds written by the platform itself. Component kinds
// (core, plugin, notifier, auth-provider, share-target, service, webhook) are the extension kinds
// recorded by the component lifecycle and their deliveries.
const (
	AuditKindBorrow     = "borrow"      // a borrow decision (shared-agent audit)
	AuditKindSession    = "session"     // a sign-in (refresh family) revoked
	AuditKindMember     = "member"      // org membership / role change
	AuditKindTenant     = "tenant"      // tenant created / status changed
	AuditKindAPIToken   = "api-token"   // API token issued / revoked / exchanged
	AuditKindAdminAlert = "admin-alert" // an admin alert queued
	// AuditKindPlatformAgent: the platform-agent policy saved (platform
	// level) or a platform agent added to / removed from a room (the room's
	// tenant).
	AuditKindPlatformAgent = "platform-agent"
	// AuditKindRetention: a data retention policy saved (platform default or
	// a tenant override) or a retention pass deleting a tenant's expired
	// data (docs/prd/2026-10-10-enterprise-scale.md §3).
	AuditKindRetention = "retention"
	// AuditKindContent: a content component used for a run (an expert a
	// chat asked an agent to wear).
	AuditKindContent = "content"
	// AuditKindApp: an interactive app's data read out (name = the app):
	// an owner's export, or an agent's read through its job's grant.
	AuditKindApp = "app"
	// AuditKindRoomSetup: a room's setup an agent changed for the person
	// who asked it, through the room tool room_setup (name = the room; the
	// detail says what: the description, the welcome, a channel, an expert
	// or a pack, a schedule; never its text).
	AuditKindRoomSetup = "room-setup"
	// AuditKindAssist: an AI assist ran (name = task@version; the model,
	// tokens and latency, never the text) or the assist model was set.
	AuditKindAssist = "assist"
	// AuditKindSource: a room's consented source (name = the reading
	// room): an admin's grant or withdrawal, a schedule's read through
	// read_records (the source, the range and the count, never a record).
	AuditKindSource = "source"
	// AuditKindEvent: an event a member sent to a room (name = the room;
	// the detail names its type and subject and how many tasks it set,
	// never its data).
	AuditKindEvent = "event"
	// AuditKindNetwork: the platform's server egress saved (the mode and
	// the hosts).
	AuditKindNetwork = "network"
	// AuditKindSection: a members-only section's membership changed (name
	// = the room; the detail names the section and the member; the actor
	// who let them in or out). A room admin's join is one (C12).
	AuditKindSection = "section"
)

// AuditLogEntry is one normalized audit record. ID is unique across sources
// ("<source>:<row id>"). Name is the record's subject (component name, member
// id, token id, the borrowed agent's owner, ...). TenantID is empty for a
// platform-level record (only the platform view shows those).
type AuditLogEntry struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Action   string `json:"action"`
	Actor    string `json:"actor,omitempty"`
	TenantID string `json:"tenantId,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Detail   string `json:"detail,omitempty"`
	RoomID   string `json:"roomId,omitempty"`
	AtMs     int64  `json:"atMs"`
}

// AuditLogReply is one page, newest first. NextCursor is opaque; absent at
// the end of the log.
type AuditLogReply struct {
	Entries    []AuditLogEntry `json:"entries"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// Audit outcomes: the one vocabulary of an audit record's outcome
// (internal/audit.Record writers; the web's auditOutcomeTone reads it).
const (
	AuditOutcomeOK        = "ok"        // an administration action done
	AuditOutcomeAllow     = "allow"     // a decision admitted (install, exchange, reply, share, use)
	AuditOutcomeDeny      = "deny"      // a decision refused
	AuditOutcomeDelivered = "delivered" // a delivery accepted
	AuditOutcomeFailed    = "failed"    // a delivery or a dispatch failed
	AuditOutcomeQueued    = "queued"    // accepted for later work (an admin alert, an expert run)
	AuditOutcomeRunning   = "running"   // work under way (content resolved for a running job)
	AuditOutcomeRevoked   = "revoked"   // a sign-in ended
)
