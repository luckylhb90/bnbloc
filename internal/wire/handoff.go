package wire

import "encoding/json"

// Room handoff package (F4): a room's conversation, artifacts, attachments and
// platform-held agent session metadata exported as one zip, importable into a
// NEW room owned by the importer. The package layout and every field below are
// documented in docs/room-export-format.md; these structs are that contract.

// RoomExportFormat is the manifest "format" of the only package version this
// build writes and accepts. An import of any other value is refused.
const RoomExportFormat = "bnbloc.room-export/v1"

// Room feed kinds. Only these two are durable history (RoomEventKinds:
// realtime persists them; presence/typing/state churn is transient), so they
// are the only kinds a handoff package carries.
const (
	KindChatMessage   = "chat_message"
	KindAgentMessages = "agent_messages"
)

// RoomExportManifest is manifest.json at the package root.
type RoomExportManifest struct {
	Format       string         `json:"format"`
	ExportedAtMs int64          `json:"exportedAtMs"`
	ExportedBy   string         `json:"exportedBy"`
	Room         RoomExportRoom `json:"room"`
	// Channels lets an import recreate the room's message streams; messages
	// reference them by the original channel id.
	Channels []RoomExportChannel `json:"channels"`
	// Members is informational only: an import never grants anyone access from
	// it, it only offers the list as invite suggestions.
	Members []RoomExportMember `json:"members"`
	Agents  []RoomExportAgent  `json:"agents"`
	// AgentSessions is the platform's view of each agent session the feed
	// carries (ids + attribution + counts) — never core-internal state.
	AgentSessions   []RoomExportAgentSession `json:"agentSessions"`
	MessageCount    int                      `json:"messageCount"`
	ArtifactCount   int                      `json:"artifactCount"`
	AttachmentCount int                      `json:"attachmentCount"`
}

// RoomExportRoom describes the exported room.
type RoomExportRoom struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Topic       string `json:"topic,omitempty"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// RoomExportChannel is one message stream of the exported room. Kind is
// RoomExportChannelPeople for the room's people chat (human-only, never
// visible to agents) and omitted for an ordinary channel; an import restores
// it as the new room's people chat.
type RoomExportChannel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	Kind      string `json:"kind,omitempty"`
}

// RoomExportChannelPeople marks the people chat in RoomExportChannel.Kind.
const RoomExportChannelPeople = "people"

// RoomExportMember is one membership of the exported room.
type RoomExportMember struct {
	UserID string `json:"userId"`
	Role   string `json:"role"`
}

// RoomExportAgent is one agent that took part in the room, by its room handle
// (owner/provider) and, when the feed recorded it, the core that drove it.
type RoomExportAgent struct {
	Owner    string `json:"owner"`
	Provider string `json:"provider"`
	Core     string `json:"core"`
}

// RoomExportAgentSession is one agent session seen in the room feed.
type RoomExportAgentSession struct {
	AgentSessionID string `json:"agentSessionId"`
	Owner          string `json:"owner,omitempty"`
	Provider       string `json:"provider,omitempty"`
	ChannelID      string `json:"channelId,omitempty"`
	MessageCount   int    `json:"messageCount"`
	FirstAtMs      int64  `json:"firstAtMs"`
	LastAtMs       int64  `json:"lastAtMs"`
}

// FeedMessage is one persisted room-feed entry: the room history row shape
// (sequence/kind/payload/atUnixMs/editedAtMs) plus the channel it belongs to.
// It is a messages.jsonl line, a page row of realtime's internal feed read,
// and a row of an import batch.
type FeedMessage struct {
	Sequence   int64           `json:"sequence"`
	ChannelID  string          `json:"channelId,omitempty"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload"`
	AtUnixMs   int64           `json:"atUnixMs"`
	EditedAtMs *int64          `json:"editedAtMs,omitempty"`
}

// FeedPage is one ascending page of realtime's internal feed read.
type FeedPage struct {
	Messages []FeedMessage `json:"messages"`
}

// ImportedMarker is stamped by realtime into payload.imported of every message
// written through the import path, so history readers (the web feed) can mark
// the row as imported. It is server-authored: no client send path can set it.
type ImportedMarker struct {
	FromRoomID       string `json:"fromRoomId"`
	FromRoomName     string `json:"fromRoomName,omitempty"`
	OriginalSequence int64  `json:"originalSequence"`
	ImportedAtMs     int64  `json:"importedAtMs"`
	// EditedAtMs carries the original edit stamp; imported rows are history
	// and cannot be edited again.
	EditedAtMs *int64 `json:"editedAtMs,omitempty"`
}

// Import batch bounds, shared by the room service (which chunks a package's
// messages) and realtime (which enforces them). They bound one internal
// request, not a package: a package is streamed as many batches.
const (
	MaxImportBatchBytes    = 8 << 20
	MaxImportBatchMessages = 500
)

// RoomImportBatch is the body of realtime's internal import write: history rows
// to persist (never publish) into the target room, in original order.
type RoomImportBatch struct {
	FromRoomID   string        `json:"fromRoomId"`
	FromRoomName string        `json:"fromRoomName,omitempty"`
	ImportedAtMs int64         `json:"importedAtMs"`
	Messages     []FeedMessage `json:"messages"`
}

// RoomImportBatchReply reports how many rows were persisted and the last
// sequence assigned.
type RoomImportBatchReply struct {
	Imported     int   `json:"imported"`
	LastSequence int64 `json:"lastSequence"`
}

// RoomExportArtifact is artifacts/<artifactId>/artifact.json: the artifact's
// metadata plus every version's metadata and the package path of its content.
type RoomExportArtifact struct {
	ArtifactID     string                      `json:"artifactId"`
	ChannelID      string                      `json:"channelId,omitempty"`
	Kind           string                      `json:"kind"`
	Title          string                      `json:"title"`
	Language       string                      `json:"language,omitempty"`
	MimeType       string                      `json:"mimeType,omitempty"`
	FileName       string                      `json:"fileName,omitempty"`
	CurrentVersion int                         `json:"currentVersion"`
	CreatedBy      string                      `json:"createdBy"`
	CreatedAtMs    int64                       `json:"createdAtMs"`
	UpdatedAtMs    int64                       `json:"updatedAtMs"`
	Versions       []RoomExportArtifactVersion `json:"versions"`
}

// RoomExportArtifactVersion is one immutable artifact revision. Storage
// "blob" marks a binary version: its file holds the raw bytes and
// ContentHash is their sha256 (inline text versions: "" or "inline").
// SourceJobID/SourceSeq are provenance in the SOURCE room only; an import
// does not carry them (jobs and sequences are not portable).
type RoomExportArtifactVersion struct {
	Version      int    `json:"version"`
	ContentHash  string `json:"contentHash"`
	Storage      string `json:"storage,omitempty"`
	MimeType     string `json:"mimeType,omitempty"`
	SizeBytes    int64  `json:"sizeBytes,omitempty"`
	SourceJobID  string `json:"sourceJobId,omitempty"`
	SourceSeq    int64  `json:"sourceSeq,omitempty"`
	Summary      string `json:"summary,omitempty"`
	AuthorUserID string `json:"authorUserId"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	// File is the package path of this version's content.
	File string `json:"file"`
}

// RoomExportAttachment is attachments/<attachmentId>/attachment.json.
type RoomExportAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	// File is the package path of the attachment bytes.
	File string `json:"file"`
}

// RoomHandoffInfo is a room's import provenance (GET /v1/rooms/{id}/handoff),
// present only for rooms created by an import.
type RoomHandoffInfo struct {
	RoomID             string             `json:"roomId"`
	SourceRoomID       string             `json:"sourceRoomId"`
	SourceRoomName     string             `json:"sourceRoomName"`
	SourceExportedAtMs int64              `json:"sourceExportedAtMs"`
	SourceExportedBy   string             `json:"sourceExportedBy"`
	ImportedBy         string             `json:"importedBy"`
	ImportedAtMs       int64              `json:"importedAtMs"`
	SuggestedMembers   []RoomExportMember `json:"suggestedMembers"`
}

// RoomImportResult answers POST /v1/rooms/import.
type RoomImportResult struct {
	RoomID              string `json:"roomId"`
	Name                string `json:"name"`
	ImportedMessages    int    `json:"importedMessages"`
	ImportedArtifacts   int    `json:"importedArtifacts"`
	ImportedAttachments int    `json:"importedAttachments"`
	// SkippedAttachments lists attachment ids the upload path refused (e.g. a
	// type the current upload policy no longer accepts); their references are
	// dropped from the imported messages.
	SkippedAttachments []string        `json:"skippedAttachments"`
	Handoff            RoomHandoffInfo `json:"handoff"`
}
