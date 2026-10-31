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
	// EventPresenceDelta carries PresenceDeltaPayload: a room's arrivals and
	// departures, at most one a second; EventPresenceSnapshot carries
	// {"members": [...]}, written to a stream alone when it opens.
	EventPresenceDelta    = "presence_delta"
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
	// Sequenced kinds change who may read what (the revocations, a
	// section's membership): they store nothing but take the room's
	// sequence, so every replica delivers them in order with the room's
	// messages and a message stored after one never reaches a reader it
	// took access from.
	Sequenced bool `json:"sequenced,omitempty"`
	// ChannelScoped kinds carry the channelId of the message or channel they
	// concern; a single-channel stream receives them only for its channel.
	ChannelScoped bool `json:"channelScoped,omitempty"`
	// CrossChannel kinds name the channel they belong to (a deliverable's)
	// yet reach every stream of the room, a one-channel stream included,
	// whose reader may read that channel (store.MayReadChannel): a room-wide
	// surface (the Docs panel) that still keeps a members-only channel's
	// content to its readers (C13).
	CrossChannel bool `json:"crossChannel,omitempty"`
	// Lane is the kind's lane on a lanes stream (§2.4 of
	// docs/prd/2026-10-28-channels-and-threads.md; StreamLane): "" a
	// channel-scoped kind reaches only the channels the stream has open
	// (a room-wide kind, ChannelID "", always), LaneRoom reaches every
	// channel the reader may read, LaneActivity becomes a coalesced
	// channel_activity outside the open channels.
	Lane string `json:"lane,omitempty"`
	// Internal kinds tell realtime itself something (its room state) and
	// never reach a stream.
	Internal bool `json:"internal,omitempty"`
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
	{Name: KindChatMessage, Durable: true, Replay: true, ChannelScoped: true, Preview: true, Lane: LaneActivity},
	{Name: KindAgentMessages, Durable: true, Replay: true, ChannelScoped: true, Preview: true, Lane: LaneActivity},
	{Name: EventAgentJobCreated, ChannelScoped: true, Lane: LaneRoom},
	{Name: EventAgentJobUpdated, ChannelScoped: true, Lane: LaneRoom},
	{Name: EventAgentJobProgress, ChannelScoped: true},
	{Name: EventJobLiveFrame, ChannelScoped: true},
	{Name: EventAgentSessionState},
	{Name: EventAgentRoster},
	{Name: EventPresenceDelta},
	{Name: EventPresenceSnapshot},
	{Name: EventPresenceTyping, ChannelScoped: true},
	{Name: EventMessageDeleted, ChannelScoped: true, Preview: true, Lane: LaneActivity},
	{Name: EventMessageEdited, ChannelScoped: true, Preview: true},
	{Name: EventTaskUpdated, ChannelScoped: true, Lane: LaneRoom},
	{Name: EventArtifactUpdated, CrossChannel: true, Preview: true},
	{Name: EventArtifactState, CrossChannel: true, Preview: true},
	{Name: EventMemberJoined, Preview: true},
	{Name: EventMemberRemoved, Revocation: true, Sequenced: true, Preview: true},
	{Name: EventRoomDeleted, Revocation: true, Sequenced: true, Preview: true},
	{Name: EventContentSaved},
	{Name: EventContentUsesChanged},
	{Name: EventAppChanged},
	// A channel or section changed (bnbloc-room): room-wide, so every
	// stream reloads the listing and agent filters forget their verdicts.
	// A preview reads the listing too.
	{Name: EventChannelUpdated, Preview: true},
	// A members-only section's membership changed (bnbloc-room):
	// room-wide, so listings reload and realtime re-scopes the member's
	// streams at once (C11: never shown to a preview).
	{Name: EventSectionMembership, Sequenced: true},
	// The room's settings or a member's agreement changed (bnbloc-room):
	// room-wide, so realtime's room state reads the room again.
	{Name: EventRoomUpdated},
	// A member followed, left or re-marked a channel (bnbloc-room): realtime
	// forgets what it kept of the channel's followers; never on a stream.
	{Name: EventChannelFollow, ChannelScoped: true, Internal: true},
	// A realtime replica's view of the room's live state (who is here, the
	// agents members' bridges serve), on the bus between replicas
	// (realtime.RoomViews); never on a stream.
	{Name: EventRoomView, Internal: true},
	// A member marked a room's channel read (realtime): on the member's own
	// key only (MemberActivityKey), for their activity streams; never on a
	// room stream.
	{Name: EventReadMarked, Internal: true},
	// The member's rooms changed (realtime): a join on the member's own key
	// (MemberActivityKey), and what a member stream tells its client when
	// it carries other rooms; never on a room stream.
	{Name: EventMemberRooms},
	// A thread opened, renamed, archived or replied in (realtime and
	// bnbloc-room): scoped to the thread's parent channel, whose stream
	// redraws the root's summary line.
	{Name: EventThreadUpdated, ChannelScoped: true, Preview: true, Lane: LaneRoom},
	// Made by a lanes stream itself, never published to a room (§2.4):
	// a channel's activity outside the open channels, and the stream's id
	// that re-scoping names.
	{Name: EventChannelActivity, ChannelScoped: true, Preview: true},
	{Name: EventStreamOpened, Preview: true},
	{Name: EventStreamGap, Preview: true},
}

// The lanes of a lanes stream (EventKindSpec.Lane, StreamLane).
const (
	LaneFull     = "full"
	LaneRoom     = "room"
	LaneActivity = "activity"
	LaneNone     = "none"
)

// StreamLane is the one rule of a lanes stream: how an event of kind for
// channelID reaches a subscriber who has open the channels open says yes to
// (§2.4). Full delivers the event; activity turns it into the channel's
// coalesced channel_activity; none drops it. Read access
// (store.MayReadChannel) is asked separately, for every lane.
func StreamLane(kind, channelID string, open func(string) bool) string {
	spec := eventKinds[kind]
	switch {
	case channelID == "", spec.CrossChannel, spec.Lane == LaneRoom, open(channelID):
		return LaneFull
	case spec.Lane == LaneActivity:
		return LaneActivity
	}
	return LaneNone
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

// CrossChannelKind reports whether kind reaches a one-channel stream of
// another channel (EventKindSpec.CrossChannel).
func CrossChannelKind(kind string) bool { return eventKinds[kind].CrossChannel }

// InternalKind reports whether kind is realtime's own, never streamed.
func InternalKind(kind string) bool { return eventKinds[kind].Internal }

// PreviewedKind reports whether a preview's stream carries kind.
func PreviewedKind(kind string) bool { return eventKinds[kind].Preview }

// ReplayedKind reports whether a stream reconnecting to the room is sent
// kind again from the hub's replay buffer.
func ReplayedKind(kind string) bool { return eventKinds[kind].Replay }
