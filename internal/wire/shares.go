package wire

// Share links of an artifact (docs/prd/2026-10-09-artifact-shares-and-site-previews.md,
// access: docs/prd/2026-10-13-share-link-sign-in.md).

// Who may open a share link (ShareView.Access): a signed-in member of the
// room's organization (the default), or anyone holding the link, without
// signing in (the creator's opt-in, refused while the organization
// disables anonymous links).
//
//wire:union ShareAccess
const (
	ShareAccessOrg    = "org"
	ShareAccessAnyone = "anyone"
)

// ValidShareAccess reports a known access mode.
func ValidShareAccess(a string) bool { return a == ShareAccessOrg || a == ShareAccessAnyone }

// ShareCreateRequest is POST /v1/rooms/{roomId}/artifacts/{artifactId}/shares:
// Version 0 (or absent) follows the latest version; ExpiresInHours
// 1..2160 (default 168); Access defaults to ShareAccessOrg.
type ShareCreateRequest struct {
	Version        int    `json:"version,omitempty"`
	ExpiresInHours int    `json:"expiresInHours,omitempty"`
	Access         string `json:"access,omitempty" tstype:"ShareAccess"`
}

// ShareAccessUpdate is PATCH …/shares/{shareId}: the link's new access.
type ShareAccessUpdate struct {
	Access string `json:"access" tstype:"ShareAccess"`
}

// ShareTicketRequest is POST /v1/share-tickets: the link's token, in the
// body so it never reaches an access log.
type ShareTicketRequest struct {
	Token string `json:"token"`
}

// ShareTicket answers a ticket mint: a signed credential of the signed-in
// member for one link, used in place of the link's token in
// /v1/public/shares/{…} (metadata, content, site, ~app).
type ShareTicket struct {
	Ticket      string `json:"ticket"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// The codes of a share route's refusal (httpx.ErrorBody code) the share
// page acts on.
//
//wire:union ShareRefusalCode
const (
	// ShareSignInRequired: an org link opened without signing in (401).
	ShareSignInRequired = "share_sign_in_required"
	// ShareOrgOnly: the signed-in caller is not a member of the link's
	// organization (403).
	ShareOrgOnly = "share_org_only"
)

// ShareView is one share link as the room service lists it.
type ShareView struct {
	ShareID        string `json:"shareId"`
	ArtifactID     string `json:"artifactId"`
	Version        int    `json:"version"` // 0 = follows the latest version
	CreatedBy      string `json:"createdBy"`
	CreatedAtMs    int64  `json:"createdAtMs"`
	ExpiresAtMs    int64  `json:"expiresAtMs"`
	RevokedAtMs    int64  `json:"revokedAtMs,omitempty"`
	RevokedBy      string `json:"revokedBy,omitempty"`
	ViewCount      int64  `json:"viewCount"`
	LastViewedAtMs int64  `json:"lastViewedAtMs,omitempty"`
	// Access is who may open the link (ShareAccessOrg, ShareAccessAnyone).
	Access string `json:"access" tstype:"ShareAccess"`
	// Active is the link's own state (not revoked, not expired); the
	// organization switch is reported by GET /v1/my/tenant-policy.
	Active bool `json:"active"`
}

// ShareResolveRequest is the gateway's internal POST
// /internal/v1/shares/resolve: a link's token (its secret is verified), or
// the share id of a share ticket the gateway signed.
type ShareResolveRequest struct {
	Token   string `json:"token,omitempty"`
	ShareID string `json:"shareId,omitempty"`
}

// ShareViewCount is the gateway's internal POST /internal/v1/shares/view:
// one view of a link its caller was allowed to open.
type ShareViewCount struct {
	ShareID string `json:"shareId"`
}

// ShareResolution is the room service's answer to the gateway's internal
// POST /internal/v1/shares/resolve for a live link.
type ShareResolution struct {
	TenantID string `json:"tenantId"`
	// OrgName is the organization's display name (its id when it has
	// none), for the refusal of a caller outside it.
	OrgName  string        `json:"orgName"`
	RoomID   string        `json:"roomId"`
	Share    ShareView     `json:"share"`
	Artifact ShareArtifact `json:"artifact"`
	Version  ShareVersion  `json:"version"`
	SharedBy ShareUser     `json:"sharedBy"`
}

// ShareArtifact is the shared artifact's identity.
type ShareArtifact struct {
	ArtifactID string `json:"artifactId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	FileName   string `json:"fileName"`
}

// ShareVersion is the resolved version (the latest one, resolved at call
// time, for a link that follows the latest).
type ShareVersion struct {
	Version     int    `json:"version"`
	Storage     string `json:"storage"`
	MimeType    string `json:"mimeType"`
	SizeBytes   int64  `json:"sizeBytes"`
	ContentHash string `json:"contentHash"`
	BlobKey     string `json:"blobKey,omitempty"`
	Content     string `json:"content,omitempty"` // inline versions only
	Summary     string `json:"summary,omitempty"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// ShareUser names who shared the link.
type ShareUser struct {
	UserID string `json:"userId"`
	// DisplayName is what an outsider sees as "shared by": the user's
	// display name with the default applied (store.DisplayName — the local
	// part of the id when none is set; ids are usually e-mail addresses and
	// the full address is not published).
	DisplayName string `json:"displayName"`
}
