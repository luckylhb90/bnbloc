package wire

// Team-feature DTOs: chat messages marked as tasks (F6) and global search
// (F7). realtime serves both; the JSON tags are the public contract the web
// client reads.

// Message task status values.
const (
	TaskOpen = "open"
	TaskDone = "done"
)

// EventTaskUpdated is the transient room event published when a message task
// is marked, toggled, or unmarked. It carries TaskUpdatedPayload and is never
// persisted (realtime's persist() allowlist stores only chat_message and
// agent_messages).
const EventTaskUpdated = "task_updated"

// MessageTask marks one persisted room message (by its room sequence) as a
// task. Title and MessageAuthor are snapshots taken when the task was marked.
type MessageTask struct {
	RoomID        string `json:"roomId"`
	ChannelID     string `json:"channelId,omitempty"`
	Seq           int64  `json:"seq"`
	Status        string `json:"status"`
	Title         string `json:"title"`
	MessageAuthor string `json:"messageAuthor,omitempty"`
	CreatedBy     string `json:"createdBy"`
	CreatedAtMs   int64  `json:"createdAtMs"`
	DoneBy        string `json:"doneBy,omitempty"`
	DoneAtMs      int64  `json:"doneAtMs,omitempty"`
}

// TaskUpdatedPayload is the payload of an EventTaskUpdated event: the task's
// new state, or Removed with only Seq set when the task was unmarked.
type TaskUpdatedPayload struct {
	Seq     int64        `json:"seq"`
	Task    *MessageTask `json:"task,omitempty"`
	Removed bool         `json:"removed,omitempty"`
}

// Search result kinds.
const (
	SearchMessage  = "message"
	SearchArtifact = "artifact"
)

// SearchResult is one global-search hit. Snippet is plain text (the client
// highlights the query); RoomID/RoomName/Seq/ChannelID locate a message hit,
// ArtifactID an artifact hit.
type SearchResult struct {
	Kind      string `json:"kind"`
	RoomID    string `json:"roomId,omitempty"`
	RoomName  string `json:"roomName,omitempty"`
	ChannelID string `json:"channelId,omitempty"`
	// ChannelKind is "people" when the hit is in the room's people chat.
	ChannelKind string `json:"channelKind,omitempty"`
	Seq         int64  `json:"seq,omitempty"`
	ArtifactID  string `json:"artifactId,omitempty"`
	// ArtifactKind / MimeType / FileName type an artifact hit (file icon,
	// viewer choice) without a second fetch.
	ArtifactKind string `json:"artifactKind,omitempty"`
	MimeType     string `json:"mimeType,omitempty"`
	FileName     string `json:"fileName,omitempty"`
	Title        string `json:"title,omitempty"`
	Snippet      string `json:"snippet"`
	Author       string `json:"author,omitempty"`
	AtMs         int64  `json:"atMs"`
}

// SearchResponse is the GET /v1/search reply. NextBefore, when non-zero, is
// the `before` cursor for the next (older) page of message hits.
type SearchResponse struct {
	Results    []SearchResult `json:"results"`
	NextBefore int64          `json:"nextBefore,omitempty"`
}
