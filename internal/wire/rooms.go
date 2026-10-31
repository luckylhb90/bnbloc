package wire

// Room projections: what the room and realtime services answer about rooms,
// members and unread counts. Each is the one shape of its route (the web
// reads it from wire.gen.ts); the store rows they are built from stay
// server-side.

// RoomSummary is one room of the caller's own room list
// (GET /v1/users/{id}/rooms?detail=1). Role is the caller's role in it.
type RoomSummary struct {
	RoomID      string `json:"roomId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility" tstype:"RoomVisibility"`
	SyncMode    string `json:"syncMode" tstype:"RoomSyncMode"`
	Role        string `json:"role" tstype:"RoomRole"`
	MemberCount int    `json:"memberCount"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// RoomsReply is the caller's room list.
type RoomsReply struct {
	Rooms []RoomSummary `json:"rooms"`
}

// RoomDetail is GET /v1/rooms/{id} for a member (and the answer to a create
// or a PATCH). The caller's role is not part of it: it comes from the member
// list (RoomMembersReply), the one source a role change updates. Welcome
// and QuickPrompts are what an empty room shows every member
// (docs/prd/2026-10-20-room-setup-by-conversation.md §2.1).
type RoomDetail struct {
	RoomID      string `json:"roomId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility" tstype:"RoomVisibility"`
	// Previewable: anyone signed in to the room's organization may read the
	// room without joining it (read-only). Independent of Visibility.
	Previewable  bool              `json:"previewable"`
	SyncMode     string            `json:"syncMode" tstype:"RoomSyncMode"`
	CreatedBy    string            `json:"createdBy"`
	CreatedAtMs  int64             `json:"createdAtMs"`
	MemberCount  int               `json:"memberCount"`
	Welcome      string            `json:"welcome"`
	QuickPrompts []RoomQuickPrompt `json:"quickPrompts"`
	// The room's rules and onboarding (docs/prd/2026-10-28-channels-and-
	// threads.md §2.5a): its rules and their version, whether a member
	// agrees before posting (C16), the channels a new member joins, and who
	// creates channels (C15).
	Rules                 string   `json:"rules"`
	RulesVersion          int      `json:"rulesVersion"`
	RequireRulesAgreement bool     `json:"requireRulesAgreement"`
	RecommendedChannels   []string `json:"recommendedChannels"`
	ChannelCreation       string   `json:"channelCreation" tstype:"ChannelCreationPolicy"`
}

// RoomPatchRequest is PATCH /v1/rooms/{id}: a nil field is left as it is.
// Name, description, visibility, previewable and sync are the owner's; the
// rules, the agreement switch, the recommended channels and the channel
// creation policy are the owner's or an admin's.
type RoomPatchRequest struct {
	Name                  *string   `json:"name,omitempty"`
	Description           *string   `json:"description,omitempty"`
	Visibility            *string   `json:"visibility,omitempty" tstype:"RoomVisibility"`
	Previewable           *bool     `json:"previewable,omitempty"`
	SyncMode              *string   `json:"syncMode,omitempty" tstype:"RoomSyncMode"`
	Rules                 *string   `json:"rules,omitempty"`
	RequireRulesAgreement *bool     `json:"requireRulesAgreement,omitempty"`
	RecommendedChannels   *[]string `json:"recommendedChannels,omitempty"`
	ChannelCreation       *string   `json:"channelCreation,omitempty" tstype:"ChannelCreationPolicy"`
}

// RoomQuickPrompt is one quick prompt of a room: a title and the text a
// click puts into the message box (nothing is sent).
type RoomQuickPrompt struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

// RoomWelcomeRequest is PATCH /v1/rooms/{id}/welcome (the room's owner or
// an admin): a nil field is left as it is. The bounds are a room
// package's (contentpkg.CheckRoomStart).
type RoomWelcomeRequest struct {
	Welcome      *string            `json:"welcome,omitempty"`
	QuickPrompts *[]RoomQuickPrompt `json:"quickPrompts,omitempty"`
}

// RoomPreview is GET /v1/rooms/{id} for a non-member of a shared room: just
// enough for a join card. Preview is always true (a private room answers
// 404 to a non-member).
type RoomPreview struct {
	RoomID      string `json:"roomId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility" tstype:"RoomVisibility"`
	MemberCount int    `json:"memberCount"`
	Preview     bool   `json:"preview"`
	// Previewable: the room may be read before joining it, so the room view
	// opens read-only; otherwise this card (and a join) is all a non-member
	// sees.
	Previewable bool `json:"previewable"`
}

// RoomMember is one membership of a room. AgentHost marks a platform
// agent's host: in the room only so its bridge drives that agent, never a
// person (clients keep it out of people lists).
type RoomMember struct {
	UserID    string `json:"userId"`
	Role      string `json:"role" tstype:"RoomRole"`
	AgentHost bool   `json:"agentHost"`
}

// RoomMembersReply is GET /v1/rooms/{id}/members (members only).
type RoomMembersReply struct {
	Members []RoomMember `json:"members"`
}

// PlazaRoom is one room of the plaza (GET /v1/rooms?q=). A private room is
// Locked: only its name and description are shown, never who made it or
// how many are in it.
type PlazaRoom struct {
	RoomID      string `json:"roomId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Visibility  string `json:"visibility" tstype:"RoomVisibility"`
	Locked      bool   `json:"locked"`
	// Previewable: the room may be read before joining it (a private one
	// still needs an invite code to join).
	Previewable bool   `json:"previewable"`
	SyncMode    string `json:"syncMode,omitempty" tstype:"RoomSyncMode"`
	CreatedBy   string `json:"createdBy,omitempty"`
	CreatedAtMs int64  `json:"createdAtMs,omitempty"`
	MemberCount int    `json:"memberCount,omitempty"`
}

// PlazaReply is one page of the plaza: Total rooms match, this page starts
// at Offset and holds at most Limit.
type PlazaReply struct {
	Rooms  []PlazaRoom `json:"rooms"`
	Total  int         `json:"total"`
	Limit  int         `json:"limit"`
	Offset int         `json:"offset"`
}

// ChannelUnread is one channel's unread count.
type ChannelUnread struct {
	ChannelID string `json:"channelId"`
	Count     int    `json:"count"`
}

// UnreadReply is GET /v1/rooms-unread?rooms= (at most MaxUnreadRooms):
// per room, the unread total (a room with none is absent) and the channels
// that have unread messages, default channel first.
type UnreadReply struct {
	Unread   map[string]int             `json:"unread"`
	Channels map[string][]ChannelUnread `json:"channels"`
}

// The member's activity stream (GET /v1/my/activity?rooms=, SSE): one per
// client, across the rooms it names, instead of polling their unread counts
// (docs/prd/2026-10-30-replicas-without-owners.md, part B). On opening, and
// again whenever the stream may have missed something, it sends the rooms'
// unread state (UnreadStateKind, an UnreadReply); after that only what
// changed (UnreadDeltaKind, at most once a second).
const (
	UnreadStateKind = "unread_state"
	UnreadDeltaKind = "unread_delta"
)

// UnreadDeltaPayload is what changed in the named rooms' unread counts
// since the last delta.
type UnreadDeltaPayload struct {
	Rooms map[string]RoomUnreadDelta `json:"rooms"`
}

// RoomUnreadDelta is one room's change: Seqs are new messages the member
// may read that count as unread (a client counts each once, by sequence,
// unless it is reading that room), Deleted those removed, and Read that the
// member marked something read in the room (on any device): its count is
// read again.
type RoomUnreadDelta struct {
	Seqs    []int64 `json:"seqs,omitempty"`
	Deleted []int64 `json:"deleted,omitempty"`
	Read    bool    `json:"read,omitempty"`
}

// ReadMarkedPayload is EventReadMarked: a member marked a room's channel
// read, sent to that member's own activity streams only.
type ReadMarkedPayload struct {
	RoomID    string `json:"roomId"`
	ChannelID string `json:"channelId"`
}

// MemberActivityKey is the hub key a member's own events go on (their
// activity streams subscribe to it beside their rooms).
func MemberActivityKey(userID string) string { return "member:" + userID }

// UnreadSummary is GET /v1/my/unread: how many of the caller's rooms (in the
// tenant the request is signed for) have unread messages, at most
// MaxUnreadSummaryRooms rooms looked at and UnreadSummaryCap counted. More is
// true when the number is a floor: the cap was reached or the member has more
// rooms than were looked at. It is what an account switcher shows beside an
// account no window shows, so it costs a few index seeks, never a per-room
// request.
type UnreadSummary struct {
	Rooms int  `json:"rooms"`
	More  bool `json:"more"`
}

// DeletedRooms is GET /v1/my/deleted-rooms?ids= (at most 100): the asked-for
// rooms that were deleted. A live, unknown or never-created id is absent:
// only a deletion is evidence (docs/bugs/2026-10-22-deleted-room-workspaces-never-removed.md).
type DeletedRooms struct {
	Deleted []string `json:"deleted"`
}
