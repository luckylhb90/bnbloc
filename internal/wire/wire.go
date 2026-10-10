// Package wire defines the JSON DTOs exchanged between member daemon adapters
// and the bnbloc control-plane. Field shapes mirror the local agent runtime's
// Go contracts so no second schema is introduced. roomId is always the
// member's workspaceId (roomId ≡ workspaceId, one-to-one, no translation).
package wire

// ReportSessionStateInput is the body of the session-state ingest endpoint.
// It mirrors the open-source agentsessionstore ReportSessionStateInput.
type ReportSessionStateInput struct {
	WorkspaceID       string `json:"workspaceId"`
	AgentSessionID    string `json:"agentSessionId"`
	ProviderSessionID string `json:"providerSessionId,omitempty"`
	OwnerUserID       string `json:"ownerUserId"`
	State             any    `json:"state"`
	MessageVersion    int64  `json:"messageVersion"`
	OccurredAtUnixMs  int64  `json:"occurredAtUnixMs"`
}

// ReportSessionStateReply acknowledges a persisted session-state report.
type ReportSessionStateReply struct {
	SyncCursor int64 `json:"syncCursor"`
}

// Message is one canonical agent activity message.
type Message struct {
	Seq             int64  `json:"seq"`
	ClientSubmitID  string `json:"clientSubmitId,omitempty"`
	Payload         any    `json:"payload"`
	CreatedAtUnixMs int64  `json:"createdAtUnixMs"`
}

// ReportMessagesInput is the body of the messages ingest endpoint.
type ReportMessagesInput struct {
	WorkspaceID    string    `json:"workspaceId"`
	AgentSessionID string    `json:"agentSessionId"`
	ChannelID      string    `json:"channelId,omitempty"`
	Messages       []Message `json:"messages"`
	Cursor         int64     `json:"cursor"`
}

// ReportMessagesReply returns the next durable message cursor.
type ReportMessagesReply struct {
	NextCursor int64 `json:"nextCursor"`
}

// SubmitProvenanceInput is the body of the atomic submit-provenance barrier.
// The server must persist the canonical client-submit message and turn
// admission in one transaction and only ack after clientSubmitId is queryable.
type SubmitProvenanceInput struct {
	WorkspaceID    string `json:"workspaceId"`
	AgentSessionID string `json:"agentSessionId"`
	ClientSubmitID string `json:"clientSubmitId"`
	TurnAdmission  any    `json:"turnAdmission"`
}

// WorkspaceAgentSyncState mirrors the open-source sync-state projection.
type WorkspaceAgentSyncState struct {
	PendingCount  int    `json:"pendingCount"`
	FailedCount   int    `json:"failedCount"`
	LastError     string `json:"lastError,omitempty"`
	MessageCursor int64  `json:"messageCursor"`
}

// SyncStatesReply returns per-session sync states for a room.
type SyncStatesReply struct {
	States map[string]WorkspaceAgentSyncState `json:"states"`
}

// RosterAgent is one agent a member's bridge advertises as reachable in a room.
// The room agent roster is the union of these across members, letting the
// composer picker offer real drivable agents without the gateway reaching any
// user's machine (remote-safe). Core is empty for a borrowed/coreless entry.
type RosterAgent struct {
	OwnerUserID string `json:"ownerUserId"`
	Core        string `json:"core,omitempty"`
	Provider    string `json:"provider"`
	// Sandboxed: borrowed drives of this agent run in its owner's agent
	// sandbox (advertised by the owner's bridge).
	Sandboxed bool `json:"sandboxed,omitempty"`
}

// Event is a schema-first business event or presence delta fanned out over the
// realtime lane. Sequence is per-connection; older deliveries are ignored.
type Event struct {
	Sequence int64  `json:"sequence"`
	RoomID   string `json:"roomId"`
	// ChannelID routes the event to one channel of the room's message stream.
	// "" is room-wide (presence, roster, and other non-channel kinds): every
	// subscriber receives it regardless of the channel they follow.
	ChannelID string `json:"channelId,omitempty"`
	Kind      string `json:"kind"`
	Payload   any    `json:"payload"`
	AtUnixMs  int64  `json:"atUnixMs"`
}

// Access-revocation event kinds. bnbloc-room publishes them (transient, never
// persisted) when access to a room ends, and realtime closes the affected live
// streams after delivering them, so a removed member or a deleted room stops
// receiving events immediately instead of at the next reconnect.
const (
	// EventMemberRemoved carries MemberRemovedPayload; the removed member's
	// streams in the room close.
	EventMemberRemoved = "member_removed"
	// EventRoomDeleted carries no payload; every stream in the room closes.
	EventRoomDeleted = "room_deleted"
)

// MemberRemovedPayload is the payload of an EventMemberRemoved event.
type MemberRemovedPayload struct {
	UserID string `json:"userId"`
}

// Job notices. A synthetic assistant row about a job (a refused borrow, a
// failed or swept turn) is reported under the job's OWN activity session,
// never a real agent session: activity dedupes replies per
// (room, agentSessionId) by seq, so a notice in a real session would move its
// cursor and swallow later replies. Each job's notice session holds exactly
// one notice at JobNoticeSeq, so a retried or duplicate notice is a replay.
// The bridge and activity's job sweeper both use these, so their notices for
// one job share one namespace.
const JobNoticeSeq int64 = 1

// JobNoticeSessionID is the activity session a job's notice is recorded under.
func JobNoticeSessionID(jobID string) string { return "job-" + jobID }
