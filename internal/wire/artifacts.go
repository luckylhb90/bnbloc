package wire

// Artifact is a room deliverable (spec, report, code, image, site) as room
// members see it. The tenant stays server-side. store.Artifact.View builds
// it.
type Artifact struct {
	ArtifactID string `json:"artifactId"`
	RoomID     string `json:"roomId"`
	ChannelID  string `json:"channelId"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Language   string `json:"language,omitempty"`
	// MimeType / FileName are the current version's media type and display
	// name (the extension source).
	MimeType       string `json:"mimeType,omitempty"`
	FileName       string `json:"fileName,omitempty"`
	CurrentVersion int    `json:"currentVersion"`
	CreatedBy      string `json:"createdBy"`
	CreatedAtMs    int64  `json:"createdAtMs"`
	UpdatedAtMs    int64  `json:"updatedAtMs"`
	// IsApp: the artifact is an interactive app, a site whose data space
	// exists or whose current version loads the app SDK (store.Artifact.IsApp,
	// the one rule; a client never re-derives it).
	IsApp bool `json:"isApp"`
}

// ArtifactThumbnail is a stored preview image of a version (the bytes are
// served by the thumbnail route).
type ArtifactThumbnail struct {
	MimeType string `json:"mimeType"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// ArtifactVersion is one immutable revision of an artifact. Lists omit
// Content; an inline version serves it per version, a blob version through
// the content route (never Content). The blob key and the tenant stay
// server-side. store.ArtifactVersion.View builds it.
type ArtifactVersion struct {
	ArtifactID    string `json:"artifactId"`
	Version       int    `json:"version"`
	ParentVersion int    `json:"parentVersion"`
	// ContentHash is the sha256 of the version's bytes.
	ContentHash string `json:"contentHash"`
	Content     string `json:"content,omitempty"`
	// Storage is "inline" or "blob".
	Storage   string `json:"storage"`
	SizeBytes int64  `json:"sizeBytes"`
	MimeType  string `json:"mimeType,omitempty"`
	// SourceJobID / SourceSeq link the version to the agent job and the room
	// message that produced it (zero for a manual publish).
	SourceJobID  string             `json:"sourceJobId,omitempty"`
	SourceSeq    int64              `json:"sourceSeq,omitempty"`
	Summary      string             `json:"summary,omitempty"`
	AuthorUserID string             `json:"authorUserId"`
	CreatedAtMs  int64              `json:"createdAtMs"`
	Thumbnail    *ArtifactThumbnail `json:"thumbnail,omitempty"`
	// EditedFrom is the version an edit request started from (0 = not an
	// edit); below ParentVersion when the file changed meanwhile.
	EditedFrom int `json:"editedFrom,omitempty"`
}

// ArtifactList is GET /v1/rooms/{id}/artifacts: newest-updated first;
// NextCursor pages on (absent on the last page). MaxBytes is the room's
// blob version cap, so a publisher skips a file the room would refuse.
type ArtifactList struct {
	Artifacts  []Artifact `json:"artifacts"`
	MaxBytes   int64      `json:"maxBytes"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// ArtifactDetail is GET .../artifacts/{id}: the artifact and its newest
// versions (metadata only).
type ArtifactDetail struct {
	Artifact Artifact          `json:"artifact"`
	Versions []ArtifactVersion `json:"versions"`
}

// ArtifactReply answers a create (Version absent when it published none),
// a publish, and a blob publish whose bytes were already the current
// version (Unchanged, nothing new published).
type ArtifactReply struct {
	Artifact  Artifact         `json:"artifact"`
	Version   *ArtifactVersion `json:"version,omitempty"`
	Unchanged bool             `json:"unchanged,omitempty"`
}

// ArtifactVersionReply is GET .../artifacts/{id}/versions/{n}.
type ArtifactVersionReply struct {
	Version ArtifactVersion `json:"version"`
}

// ArtifactThumbnailReply answers a thumbnail put: Stored is false when a
// thumbnail of higher rank was already stored (the version is unchanged).
type ArtifactThumbnailReply struct {
	Version ArtifactVersion `json:"version"`
	Stored  bool            `json:"stored"`
}

// ArtifactUpdated is the payload of an artifact_updated room event: an
// artifact changed (metadata only, never content). Reason is "" for a new
// version, "thumbnail" when a version thumbnail was stored.
type ArtifactUpdated struct {
	ArtifactID     string `json:"artifactId"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	MimeType       string `json:"mimeType"`
	FileName       string `json:"fileName"`
	CurrentVersion int    `json:"currentVersion"`
	UpdatedBy      string `json:"updatedBy"`
	UpdatedAtMs    int64  `json:"updatedAtMs"`
	Reason         string `json:"reason,omitempty"`
	// SourceJobID is the agent job whose turn published the new version
	// ("" for a person's own): what caused it, so a scheduled run's own
	// file never triggers its room's tasks (docs/prd/2026-10-21-room-events.md).
	SourceJobID string `json:"sourceJobId,omitempty"`
}
