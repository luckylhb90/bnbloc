package wire

// EventContentUsesChanged is a transient room event: a room's content uses
// (pinned experts, the room expert, room skill and prompt packs) changed.
// Clients refetch GET /v1/content-uses?room= on it, so the composer and the
// room settings follow without reopening the room. Payload:
// ContentUsesChanged.
const EventContentUsesChanged = "content_uses_changed"

// ContentUsesChanged is the payload of EventContentUsesChanged.
type ContentUsesChanged struct {
	Scope       string `json:"scope"`
	ComponentID string `json:"componentId"`
}
