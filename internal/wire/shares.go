package wire

// Public artifact share links (docs/prd/2026-10-09-artifact-shares-and-site-previews.md).

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
	// Active is the link's own state (not revoked, not expired); the
	// organization switch is reported by GET /v1/my/tenant-policy.
	Active bool `json:"active"`
}

// ShareResolution is the room service's answer to the gateway's internal
// POST /internal/v1/shares/resolve for an active link.
type ShareResolution struct {
	TenantID string        `json:"tenantId"`
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
