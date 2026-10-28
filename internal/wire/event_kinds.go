package wire

// Room event kinds: every kind a room's event stream carries, with the
// attributes realtime acts on. RoomEventKinds is the one registry: realtime
// sequences, persists, replays, labels metrics and closes streams by it, and
// the web's event kinds are generated from it. A kind that is not registered
// is never published by a bnbloc service (TestEventKindsRegistered); a new
// kind is added here first.

// Room event kinds not declared beside their payloads.
const (
	// EventAgentJobCreated carries the queued AgentJob (realtime, PostChat).
	EventAgentJobCreated = "agent_job_created"
	// EventAgentJobUpdated carries the AgentJob after a status change
	// (activity).
	EventAgentJobUpdated = "agent_job_updated"
	// EventAgentSessionState carries ReportSessionStateInput (activity): an
	// agent session working or idle.
	EventAgentSessionState = "agent_session_state"
	// EventAgentRoster carries {"agents": [...]}: the agents members'
	// bridges advertise in the room.
	EventAgentRoster = "agent_roster"
	// EventPresenceJoin / EventPresenceLeave carry {"userId"}; a
	// EventPresenceSnapshot follows each with {"members": [...]}.
	EventPresenceJoin     = "presence_join"
	EventPresenceLeave    = "presence_leave"
	EventPresenceSnapshot = "presence_snapshot"
	// EventPresenceTyping carries {"userId"}, scoped to the channel typed in.
	EventPresenceTyping = "presence_typing"
	// EventMessageDeleted / EventMessageEdited carry the message's sequence
	// (and the new text), scoped to its channel.
	EventMessageDeleted = "message_deleted"
	EventMessageEdited  = "message_edited"
	// EventArtifactUpdated announces a new artifact version (bnbloc-room).
	EventArtifactUpdated = "artifact_updated"
	// EventArtifactState relays an artifact's live collaboration state.
	EventArtifactState = "artifact_state"
)

// EventKindSpec is one registered room event kind.
type EventKindSpec struct {
	// Name is the kind string (wire.Event.Kind).
	Name string `json:"name" tstype:"RoomEventKind"`
	// Durable kinds take a per-room sequence and are persisted to the room
	// feed (room_messages): the only kinds history and a handoff package
	// hold. Every other kind is transient, with sequence 0.
	Durable bool `json:"durable,omitempty"`
	// Replay kinds are retained in the hub's replay buffer and delivered
	// again to a reconnecting stream. A transient kind is live only: a
	// replayed toast, badge, typing hint or stale state would look new to
	// the client.
	Replay bool `json:"replay,omitempty"`
	// Revocation kinds end access to the room (a member removed, the room
	// deleted): the affected streams close after delivering them, and they
	// are never replayed.
	Revocation bool `json:"revocation,omitempty"`
	// ChannelScoped kinds carry the channelId of the message or channel they
	// concern; a single-channel stream receives them only for its channel.
	ChannelScoped bool `json:"channelScoped,omitempty"`
	// Preview kinds reach a preview's stream (a person reading a shared room
	// they have not joined, docs/prd/2026-10-22-shared-room-preview.md): the
	// kinds that match what a preview may read over REST, the feed and the
	// deliverables. Every other kind (agent runs and their progress, live
	// views, tasks, rosters, presence, content and app changes) is members'.
	Preview bool `json:"preview,omitempty"`
}

// RoomEventKinds is the registry of room event kinds. The web's
// RoomEventKind union (and RoomEventKindValues) is generated from the Name
// of each row: a kind registered here is a kind the web listens for.
//
//wire:union RoomEventKind Name
var RoomEventKinds = []EventKindSpec{
	{Name: KindChatMessage, Durable: true, Replay: true, ChannelScoped: true, Preview: true},
	{Name: KindAgentMessages, Durable: true, Replay: true, ChannelScoped: true, Preview: true},
	{Name: EventAgentJobCreated, ChannelScoped: true},
	{Name: EventAgentJobUpdated, ChannelScoped: true},
	{Name: EventAgentJobProgress, ChannelScoped: true},
	{Name: EventJobLiveFrame, ChannelScoped: true},
	{Name: EventAgentSessionState},
	{Name: EventAgentRoster},
	{Name: EventPresenceJoin},
	{Name: EventPresenceLeave},
	{Name: EventPresenceSnapshot},
	{Name: EventPresenceTyping, ChannelScoped: true},
	{Name: EventMessageDeleted, ChannelScoped: true, Preview: true},
	{Name: EventMessageEdited, ChannelScoped: true, Preview: true},
	{Name: EventTaskUpdated, ChannelScoped: true},
	{Name: EventArtifactUpdated, Preview: true},
	{Name: EventArtifactState, Preview: true},
	{Name: EventMemberJoined, Preview: true},
	{Name: EventMemberRemoved, Revocation: true, Preview: true},
	{Name: EventRoomDeleted, Revocation: true, Preview: true},
	{Name: EventContentSaved},
	{Name: EventContentUsesChanged},
	{Name: EventAppChanged},
	// A channel or section changed (bnbloc-room): room-wide, so every
	// stream reloads the listing and agent filters forget their verdicts.
	// A preview reads the listing too.
	{Name: EventChannelUpdated, Preview: true},
}

var eventKinds = func() map[string]EventKindSpec {
	m := make(map[string]EventKindSpec, len(RoomEventKinds))
	for _, k := range RoomEventKinds {
		m[k.Name] = k
	}
	return m
}()

// LookupEventKind returns a registered kind's spec.
func LookupEventKind(name string) (EventKindSpec, bool) {
	k, ok := eventKinds[name]
	return k, ok
}

// PersistedFeedKind reports whether kind is a durable room-feed kind.
func PersistedFeedKind(kind string) bool { return eventKinds[kind].Durable }

// PreviewedKind reports whether a preview's stream carries kind.
func PreviewedKind(kind string) bool { return eventKinds[kind].Preview }

// ReplayedKind reports whether a stream reconnecting to the room is sent
// kind again from the hub's replay buffer.
func ReplayedKind(kind string) bool { return eventKinds[kind].Replay }
