package wire

// An API token is the credential of an unattended caller, or of a test
// account (a svc:<name> identity a super admin issues, see
// docs/prd/2026-10-22-multi-account.md). These are the shapes of its routes
// (POST/GET /v1/platform/api-tokens, services/gateway).

// ServiceSubjectPrefix is the reserved namespace of service identities (an API
// token's subject, a test account): svc:<name>, lowercase letters, digits and
// . _ - (the gateway enforces the rest).
const ServiceSubjectPrefix = "svc:"

// APITokenCreateRequest is POST /v1/platform/api-tokens.
type APITokenCreateRequest struct {
	// Subject defaults to the caller; otherwise a svc:<name> service subject.
	Subject string `json:"subject,omitempty"`
	Label   string `json:"label"`
	TTLDays int    `json:"ttlDays,omitempty"`
	// TenantID is the tenant hint the exchanged session resolves in (default
	// the caller's session tenant).
	TenantID string `json:"tenantId,omitempty"`
}

// APITokenView is one token as listed (never the secret or its hash).
type APITokenView struct {
	ID           string `json:"id"`
	Subject      string `json:"subject"`
	Label        string `json:"label"`
	Tenant       string `json:"tenant,omitempty"`
	CreatedBy    string `json:"createdBy"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
	LastUsedAtMs int64  `json:"lastUsedAtMs,omitempty"`
	RevokedAtMs  int64  `json:"revokedAtMs,omitempty"`
	RevokedBy    string `json:"revokedBy,omitempty"`
	Active       bool   `json:"active"`
}

// APITokensReply is GET /v1/platform/api-tokens.
type APITokensReply struct {
	Tokens []APITokenView `json:"tokens"`
}

// APITokenGrant is POST /v1/auth/api-token: a session only (no refresh
// token), so its holder keeps the API token to renew it.
type APITokenGrant struct {
	Token       string   `json:"sessionToken"`
	Subject     string   `json:"subject"`
	Tenant      string   `json:"tenant"`
	Role        string   `json:"role"`
	Rooms       []string `json:"rooms"`
	ExpiresAtMs int64    `json:"expiresAtMs"`
}

// APITokenCreated answers a create: the token's view and the token itself,
// shown once.
type APITokenCreated struct {
	APITokenView
	Token string `json:"token"`
}
