package wire

// Message marks (docs/prd/2026-10-28-channels-and-threads.md §2.3): what
// people put on a stored room message. realtime serves them beside the
// messages they mark.

// Mark kinds.
//
//wire:union MessageMarkKind
const (
	// MarkReaction is a member's emoji on a message, seen by the channel's
	// readers.
	MarkReaction = "reaction"
	// MarkPin is a message pinned in its channel (one pin per message), seen
	// by the channel's readers.
	MarkPin = "pin"
	// MarkSaved is a message a member saved for themselves: theirs only.
	MarkSaved = "saved"
)

// EventMessageMarked is the transient room event published when a reaction
// or a pin is put on or taken off a message (never a save: it is the
// member's own). It carries MessageMarkedPayload and is never persisted.
const EventMessageMarked = "message_marked"

// MessageMark is one mark on a message. UserID is the member a reaction or a
// save belongs to ("" for a pin); Value a reaction's emoji; By who made it.
type MessageMark struct {
	ChannelID   string `json:"channelId,omitempty"`
	Seq         int64  `json:"seq"`
	Kind        string `json:"kind" tstype:"MessageMarkKind"`
	UserID      string `json:"userId,omitempty"`
	Value       string `json:"value,omitempty"`
	By          string `json:"by"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// MessageMarks is GET /v1/rooms/{id}/marks: the reactions and pins of the
// channels the caller may read and the caller's own saves, newest message
// first.
type MessageMarks struct {
	Marks []MessageMark `json:"marks"`
}

// MessageMarkRequest is PUT and DELETE /v1/rooms/{id}/messages/{seq}/marks:
// the kind, and a reaction's emoji.
type MessageMarkRequest struct {
	Kind  string `json:"kind" tstype:"MessageMarkKind"`
	Value string `json:"value,omitempty"`
}

// MessageMarkedPayload is the payload of an EventMessageMarked event: the
// mark that was put on (Removed false) or taken off.
type MessageMarkedPayload struct {
	Mark    MessageMark `json:"mark"`
	Removed bool        `json:"removed,omitempty"`
}
