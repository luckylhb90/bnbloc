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
	RoomID       string            `json:"roomId"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Visibility   string            `json:"visibility" tstype:"RoomVisibility"`
	SyncMode     string            `json:"syncMode" tstype:"RoomSyncMode"`
	CreatedBy    string            `json:"createdBy"`
	CreatedAtMs  int64             `json:"createdAtMs"`
	MemberCount  int               `json:"memberCount"`
	Welcome      string            `json:"welcome"`
	QuickPrompts []RoomQuickPrompt `json:"quickPrompts"`
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

// DeletedRooms is GET /v1/my/deleted-rooms?ids= (at most 100): the asked-for
// rooms that were deleted. A live, unknown or never-created id is absent:
// only a deletion is evidence (docs/bugs/2026-10-22-deleted-room-workspaces-never-removed.md).
type DeletedRooms struct {
	Deleted []string `json:"deleted"`
}
