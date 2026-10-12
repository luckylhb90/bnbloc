package wire

// Audit log DTOs (R13). shareagent serves them at GET /v1/tenant/audit-log
// (an org admin's own tenant) and GET /v1/platform/audit-log (super-admin,
// every tenant or ?tenant=). The JSON tags are the public contract the web
// client reads.

// Audit log entry kinds written by the platform itself. Component kinds
// (core, plugin, notifier, auth-provider, webhook) are the extension kinds
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
