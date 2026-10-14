package wire

import "encoding/json"

// Room event payloads as their readers decode them. An event's payload is a
// typed value in the process that published it and a JSON object after the
// internal ingest, a stored history row or an SSE hop, so a reader never
// type-switches on the shape: it calls DecodePayload with the kind's type.

// ChatMessagePayload is the payload of a chat_message event (realtime's
// PostChat writes it; readers ignore fields they do not know).
type ChatMessagePayload struct {
	// UserID is the author.
	UserID string `json:"userId"`
	Text   string `json:"text"`
	// ClientMsgID echoes the sender's idempotency id.
	ClientMsgID string `json:"clientMsgId,omitempty"`
	// Recipient addresses one member's agent; nil = people only.
	Recipient   *ChatRecipientRef `json:"recipient,omitempty"`
	Attachments []ChatAttachment  `json:"attachments,omitempty"`
	// DispatchedBy marks a task an agent dispatched through the room tools.
	DispatchedBy *DispatchMarker `json:"dispatchedBy,omitempty"`
	// Imported marks a row a handoff import wrote.
	Imported *ImportedMarker `json:"imported,omitempty"`
}

// ChatRecipientRef is the agent a chat message addresses.
type ChatRecipientRef struct {
	OwnerID  string `json:"ownerId"`
	Provider string `json:"provider"`
	Core     string `json:"core,omitempty"`
}

// FileRef names the room file a chat message is about: a deliverable at a
// version, which the agent edits (docs/prd/2026-10-14-ai-document-editing.md
// rules 2–4). Name is the file's display name, filled by the server.
type FileRef struct {
	ArtifactID string `json:"artifactId"`
	Version    int    `json:"version"`
	Name       string `json:"name,omitempty"`
}

// ChatAttachment is an attachment a chat message embeds.
type ChatAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

// AgentMessagesPayload is the payload of an agent_messages event: the batch
// a bridge (or a JobNotice writer) reported (ReportMessagesInput), with each
// message's payload read as an AgentMessageBody.
type AgentMessagesPayload struct {
	WorkspaceID    string         `json:"workspaceId"`
	AgentSessionID string         `json:"agentSessionId"`
	ChannelID      string         `json:"channelId,omitempty"`
	Messages       []AgentMessage `json:"messages"`
}

// AgentMessage is one reported message of an agent_messages batch.
type AgentMessage struct {
	Seq             int64            `json:"seq"`
	Payload         AgentMessageBody `json:"payload"`
	CreatedAtUnixMs int64            `json:"createdAtUnixMs"`
}

// AgentMessageBody is the read view of one agent message.
type AgentMessageBody struct {
	MessageID   string `json:"messageId"`
	Role        string `json:"role"`
	Text        string `json:"text"`
	OwnerUserID string `json:"ownerUserId"`
	Provider    string `json:"provider"`
	Core        string `json:"core,omitempty"`
	// Notice is the notice kind of a JobNotice line ("" for the agent's
	// own message).
	Notice string `json:"notice,omitempty"`
	JobID  string `json:"jobId,omitempty"`
}

// DecodePayload reads an event payload as T, whatever shape it arrived in:
// a T or *T published in-process, or the JSON form (a decoded map, raw
// JSON). false when the payload is absent or does not decode as T.
func DecodePayload[T any](payload any) (T, bool) {
	var zero T
	var raw []byte
	switch p := payload.(type) {
	case nil:
		return zero, false
	case T:
		return p, true
	case *T:
		if p == nil {
			return zero, false
		}
		return *p, true
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return zero, false
		}
		raw = b
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, false
	}
	return out, true
}
