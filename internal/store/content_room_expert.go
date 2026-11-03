package store

// ContentUseRoomExpert marks a room's expert (docs/prd/2026-10-12-experts-
// and-teams.md, step 6): one of the room's pinned experts that every agent
// turn in the room wears, under the chat's own expert. It is a content use
// of its own scope (scope id = the room) beside the room's pin, so a room
// has at most one (the gateway replaces it) and it needs no schema of its
// own. It is not counted as a separate use of the component: the room's
// pin already counts the room.
const ContentUseRoomExpert ContentUseScope = "room-expert"

// ContentUseChannelExpert marks a channel's expert (channels PRD C9): one of
// the room's pinned experts that turns in that channel wear instead of the
// room's (a thread follows its parent's). Scope id = ChannelExpertScopeID,
// so a channel has at most one.
const ContentUseChannelExpert ContentUseScope = "channel-expert"

// ChannelExpertScopeID is a channel's expert's scope id: the room and the
// channel, so DeleteRoom's room scope and a channel's own read find it.
func ChannelExpertScopeID(roomID, channelID string) string { return roomID + ":" + channelID }
