package wire

// A room's channels, their sections and who follows them
// (docs/prd/2026-10-28-channels-and-threads.md, slice 1): the shapes the room
// service answers and the web and the bridge read. The store keeps the same
// settings document (store.RoomChannel.Settings), so a setting has one shape.

// ChannelAgent is a channel's default agent, by its key (owner, core,
// provider): what the composer proposes in the channel for a member who has
// made no choice of their own (C8). It only proposes, never acts.
type ChannelAgent struct {
	OwnerID  string `json:"ownerId"`
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider"`
}

// Who may post in a channel (ChannelSettings.WhoPosts).
//
//wire:union ChannelWhoPosts
const (
	// ChannelPostersEveryone: every member who may chat (the default).
	ChannelPostersEveryone = ""
	// ChannelPostersAdmins: the room's owner and admins only; an agent an
	// admin asks still replies.
	ChannelPostersAdmins = "admins"
)

// When a channel's default agent answers (ChannelSettings.AgentParticipation,
// C19). Stored in slice 1; nothing acts on it yet.
//
//wire:union ChannelAgentParticipation
const (
	// ChannelAgentsWhenMentioned: only when addressed (today's behaviour,
	// the default).
	ChannelAgentsWhenMentioned = ""
	// ChannelAgentsQuestions: to every question, by the agent's own reading.
	ChannelAgentsQuestions = "questions"
	// ChannelAgentsThreads: to every new thread.
	ChannelAgentsThreads = "threads"
)

// IsChannelWhoPosts and IsChannelAgentParticipation are the one validation
// of a channel's settings values (the web reads the generated
// ChannelWhoPostsValues and ChannelAgentParticipationValues unions).
func IsChannelWhoPosts(v string) bool {
	return v == ChannelPostersEveryone || v == ChannelPostersAdmins
}

func IsChannelAgentParticipation(v string) bool {
	return v == ChannelAgentsWhenMentioned || v == ChannelAgentsQuestions || v == ChannelAgentsThreads
}

// ChannelSettings is a channel's settings document (its owners', §2.1). The
// zero value is today's channel: everyone posts, agents are admitted, no
// slow mode, agents answer when mentioned.
type ChannelSettings struct {
	// DefaultAgent is proposed by the composer (C8); nil for none.
	DefaultAgent *ChannelAgent `json:"defaultAgent,omitempty"`
	WhoPosts     string        `json:"whoPosts,omitempty" tstype:"ChannelWhoPosts"`
	// NoAgents bars agents from the channel: they never read it or answer in
	// it (store.ChannelAdmitsAgents). The zero value admits them.
	NoAgents bool `json:"noAgents,omitempty"`
	// SlowModeSeconds is the least time between two posts of one member in
	// the channel; 0 is off. Set by the channel's owners, no platform
	// default (C22).
	SlowModeSeconds int `json:"slowModeSeconds,omitempty"`
	// AgentParticipation is stored only in this slice (C19).
	AgentParticipation string `json:"agentParticipation,omitempty" tstype:"ChannelAgentParticipation"`
}

// RoomChannel is one channel of a room as the room service answers it
// (list, create, patch, archive, restore). Joined, Starred and Hidden are
// the caller's own (C18): following, not access.
type RoomChannel struct {
	ChannelID string `json:"channelId"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	// Kind is "people" for the room's people chat (never listed), omitted
	// for an ordinary channel.
	Kind      string `json:"kind,omitempty"`
	CreatedBy string `json:"createdBy"`
	Purpose   string `json:"purpose"`
	Rules     string `json:"rules"`
	// SectionID is the channel's section, "" at the room's top level.
	SectionID    string          `json:"sectionId"`
	Position     int             `json:"position"`
	OwnerID      string          `json:"ownerId"`
	ArchivedAtMs int64           `json:"archivedAtMs,omitempty"`
	Settings     ChannelSettings `json:"settings"`
	Joined       bool            `json:"joined"`
	Starred      bool            `json:"starred"`
	Hidden       bool            `json:"hidden"`
	// GroupID is the caller's own group the channel sits in on their rail
	// ("" none; C24). Never another member's.
	GroupID string `json:"groupId"`
	// Thread is set for a thread (Kind ChannelKindThread): its parent, root
	// message and activity.
	Thread *ThreadInfo `json:"thread,omitempty"`
}

// ChannelKindThread is RoomChannel.Kind of a thread (§2.2, C1): a channel
// opened from one message, never listed among the room's channels.
const ChannelKindThread = "thread"

// ThreadInfo is what a thread is of its parent: the channel and message it
// was opened from, and what its root shows (reply count, last activity,
// participants).
type ThreadInfo struct {
	ParentID      string   `json:"parentId"`
	RootSeq       int64    `json:"rootSeq"`
	Replies       int      `json:"replies"`
	LastReplyAtMs int64    `json:"lastReplyAtMs,omitempty"`
	Participants  []string `json:"participants"`
}

// ThreadOpenRequest is POST /v1/rooms/{id}/threads: open (or get) the
// thread of message RootSeq of channel ParentID. Title is optional (the
// root's first words when empty).
type ThreadOpenRequest struct {
	ParentID string `json:"parentId"`
	RootSeq  int64  `json:"rootSeq"`
	Title    string `json:"title,omitempty"`
}

// ThreadList is GET /v1/rooms/{id}/channel-threads: the threads the caller may read
// (?parent=<channel> one channel's; ?followed=1 the ones they follow, unread
// first on the web), each with their following.
type ThreadList struct {
	Threads []RoomChannel `json:"threads"`
}

// MaxThreadTitle bounds a thread's title (its name).
const MaxThreadTitle = 120

// EventThreadUpdated (ThreadUpdatedPayload) is published by realtime when a
// reply lands in a thread and by bnbloc-room when one is opened, renamed or
// archived: the parent channel's stream updates its root's summary line.
// Channel-scoped to the PARENT channel, so a reader of the parent receives
// it (and only a reader: the thread's readers are its parent's).
const EventThreadUpdated = "thread_updated"

// ThreadUpdatedPayload names the thread and its root, with its activity.
type ThreadUpdatedPayload struct {
	ThreadID string     `json:"threadId"`
	Title    string     `json:"title"`
	Archived bool       `json:"archived,omitempty"`
	Thread   ThreadInfo `json:"thread"`
}

// ChannelGroup is one of the caller's own groups of their channel rail
// (docs/prd/2026-10-28-channels-and-threads.md C24): a folder only they see;
// a channel's section is unchanged for everyone else.
type ChannelGroup struct {
	GroupID     string `json:"groupId"`
	Name        string `json:"name"`
	Position    int    `json:"position"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// ChannelGroupCreateRequest is POST /v1/rooms/{id}/channel-groups.
type ChannelGroupCreateRequest struct {
	Name     string `json:"name"`
	Position int    `json:"position,omitempty"`
}

// ChannelGroupPatchRequest is PATCH /v1/rooms/{id}/channel-groups/{groupId}.
type ChannelGroupPatchRequest struct {
	Name     *string `json:"name,omitempty"`
	Position *int    `json:"position,omitempty"`
}

// Section visibilities.
//
//wire:union SectionVisibility
const (
	// SectionOpen: every room member sees its channels.
	SectionOpen = "open"
	// SectionMembers: only its members see its channels (C11): a member of
	// the room who is not one sees nothing of it; the room's owners and
	// admins see its name and members and read it only by joining (C12).
	SectionMembers = "members"
)

// How a member joins a members-only section (C11, C17).
//
//wire:union SectionJoinPolicy
const (
	// SectionJoinInvite: the section's owners add its members; a member of
	// the room who is not one sees nothing of it (C11's default; the
	// default for a members-only section, decision: pending owner: C17
	// names "ask" the default, but asking needs the section's name shown,
	// which C11 leaves to its owners).
	SectionJoinInvite = "invite"
	// SectionJoinAsk: its name is shown to the room for joining (never its
	// channels or members); a member asks, the section's owners approve.
	SectionJoinAsk = "ask"
	// SectionJoinOpen: its name is shown for joining; any room member joins
	// at once (for an open section joining is only following).
	SectionJoinOpen = "open"
)

// RoomSection is one section of a room: a named group of channels with its
// own owners and rules (§2.1a).
type RoomSection struct {
	SectionID   string   `json:"sectionId"`
	Name        string   `json:"name"`
	Position    int      `json:"position"`
	Visibility  string   `json:"visibility" tstype:"SectionVisibility"`
	Rules       string   `json:"rules"`
	Owners      []string `json:"owners"`
	JoinPolicy  string   `json:"joinPolicy" tstype:"SectionJoinPolicy"`
	CreatedBy   string   `json:"createdBy"`
	CreatedAtMs int64    `json:"createdAtMs"`
	// Members are a members-only section's members (who reads it), listed
	// to those who may know the section exists; empty for an open one.
	Members []string `json:"members,omitempty"`
	// Joined: the reader is a member of this members-only section (an
	// admin who has not joined sees it but not its channels).
	Joined bool `json:"joined,omitempty"`
}

// SectionRequests is GET /v1/rooms/{id}/sections/{sectionId}/requests: a
// members-only section's open join requests, for those who approve them
// (its owners and the room's owners and admins).
type SectionRequests struct {
	Requests []SectionJoinRequest `json:"requests"`
}

// SectionJoinRequest is one member's open request to join a section.
type SectionJoinRequest struct {
	UserID        string `json:"userId"`
	RequestedAtMs int64  `json:"requestedAtMs"`
}

// SectionJoinReply is POST /v1/rooms/{id}/sections/{sectionId}/join: whether
// the caller joined at once ("open") or asked (Requested, "ask").
type SectionJoinReply struct {
	Joined    bool `json:"joined"`
	Requested bool `json:"requested"`
}

// SectionMemberRequest is POST /v1/rooms/{id}/sections/{sectionId}/members:
// the section's owners (and the room's owners and admins) add a member of
// the room, or approve their request.
type SectionMemberRequest struct {
	UserID string `json:"userId"`
}

// EventSectionMembership (SectionMembershipPayload) is published by
// bnbloc-room when a member joins or leaves a members-only section, is
// added or removed, or asks to join: room-wide, so clients reload the
// listing and realtime's streams re-scope at once (a stream that read the
// section's channels for that member stops delivering them). Joining by a
// room owner or admin is said to the section's members (C12): Admin marks
// it.
const EventSectionMembership = "section_membership"

// SectionMembershipPayload names the section, the member and what happened.
type SectionMembershipPayload struct {
	SectionID string `json:"sectionId"`
	UserID    string `json:"userId"`
	// Action is "joined", "left", "added", "removed", "requested" or
	// "declined".
	Action string `json:"action"`
	// By is who acted (the member themself, or an owner).
	By string `json:"by"`
	// Admin: the member is one of the room's owners or admins (C12: their
	// joining is shown to the section's members).
	Admin bool `json:"admin,omitempty"`
}

// ChannelList is GET /v1/rooms/{id}/channels: the room's channels (live and
// not archived; ?archived=1 the archived ones; the people chat never) and
// its sections.
type ChannelList struct {
	Channels []RoomChannel `json:"channels"`
	Sections []RoomSection `json:"sections"`
	// Groups are the caller's own groups of their rail (C24).
	Groups []ChannelGroup `json:"groups"`
}

// Query flags of GET /v1/rooms/{id}/channels.
const (
	// ChannelListForAgents ("agents=1") lists the channels an agent may
	// read: no people chat, no channel whose settings bar agents, archived
	// ones kept (their history stays readable). The bridge's records read
	// it (roomlog.AgentChannelList); realtime's agentMayReadChannel stays
	// the enforcement.
	ChannelListForAgents = "agents"
	// ChannelListArchived ("archived=1") lists the archived channels only.
	ChannelListArchived = "archived"
	// ChannelListThread ("thread=<id>", with agents=1) adds that thread to
	// the agents' listing when the reader may read it: the turn's own
	// conversation when it is a thread (threads are never listed among the
	// room's channels, §2.2). The thread's root is a message of its parent,
	// whose records stay their own.
	ChannelListThread = "thread"
)

// ChannelDirectoryEntry is one channel of the room's directory: every open
// channel a member may join, with how many follow it.
type ChannelDirectoryEntry struct {
	RoomChannel
	Followers int `json:"followers"`
}

// ChannelDirectory is GET /v1/rooms/{id}/channel-directory.
type ChannelDirectory struct {
	Channels []ChannelDirectoryEntry `json:"channels"`
	Sections []RoomSection           `json:"sections"`
}

// ChannelFollowRequest is PUT /v1/rooms/{id}/channels/{channelId}/follow:
// the caller follows the channel (joins it) and, when given, stars or hides
// it for themselves, or puts it in one of their groups (GroupID: "" takes it
// out of its group).
type ChannelFollowRequest struct {
	Starred *bool   `json:"starred,omitempty"`
	Hidden  *bool   `json:"hidden,omitempty"`
	GroupID *string `json:"groupId,omitempty"`
}

// ChannelPatchRequest is PATCH /v1/rooms/{id}/channels/{channelId}: a nil
// field is left as it is; Settings replaces the whole document.
type ChannelPatchRequest struct {
	Name      *string          `json:"name,omitempty"`
	Purpose   *string          `json:"purpose,omitempty"`
	Rules     *string          `json:"rules,omitempty"`
	SectionID *string          `json:"sectionId,omitempty"`
	Position  *int             `json:"position,omitempty"`
	OwnerID   *string          `json:"ownerId,omitempty"`
	Settings  *ChannelSettings `json:"settings,omitempty"`
}

// ChannelCreateRequest is POST /v1/rooms/{id}/channels.
type ChannelCreateRequest struct {
	Name      string `json:"name"`
	Purpose   string `json:"purpose,omitempty"`
	SectionID string `json:"sectionId,omitempty"`
}

// SectionCreateRequest is POST /v1/rooms/{id}/sections. A members-only
// section's creator is its first owner and member; JoinPolicy defaults to
// "invite" for one (C11).
type SectionCreateRequest struct {
	Name       string `json:"name"`
	Rules      string `json:"rules,omitempty"`
	Position   int    `json:"position,omitempty"`
	Visibility string `json:"visibility,omitempty" tstype:"SectionVisibility"`
	JoinPolicy string `json:"joinPolicy,omitempty" tstype:"SectionJoinPolicy"`
}

// SectionPatchRequest is PATCH /v1/rooms/{id}/sections/{sectionId}. A
// section's visibility is fixed at creation (decision: pending owner):
// opening a members-only section would show its history to the room, and
// closing an open one would take it from members mid-conversation.
type SectionPatchRequest struct {
	Name       *string   `json:"name,omitempty"`
	Rules      *string   `json:"rules,omitempty"`
	Position   *int      `json:"position,omitempty"`
	Owners     *[]string `json:"owners,omitempty"`
	JoinPolicy *string   `json:"joinPolicy,omitempty" tstype:"SectionJoinPolicy"`
}

// Who creates channels in a room (C15): RoomDetail.ChannelCreation.
//
//wire:union ChannelCreationPolicy
const (
	// ChannelCreationAnyone: any member, at the top level or in a section
	// they may place channels in (the default).
	ChannelCreationAnyone = "anyone"
	// ChannelCreationSections: members only inside sections; the top level
	// is the owners' and admins'.
	ChannelCreationSections = "sections"
	// ChannelCreationAdmins: the room's owners and admins only.
	ChannelCreationAdmins = "admins"
)

// IsChannelCreationPolicy is the one validation of a room's channel
// creation policy.
func IsChannelCreationPolicy(v string) bool {
	return v == ChannelCreationAnyone || v == ChannelCreationSections || v == ChannelCreationAdmins
}

// RoomRulesState is GET /v1/rooms/{id}/rules: the room's rules, their
// version, whether agreeing is required before posting (C16) and the
// version the caller agreed to (0 = never).
type RoomRulesState struct {
	Rules         string `json:"rules"`
	Version       int    `json:"version"`
	Required      bool   `json:"required"`
	AgreedVersion int    `json:"agreedVersion"`
}

// RulesAgreeRequest is POST /v1/rooms/{id}/rules/agree: the version of the
// rules the caller read and agrees to. A version that is no longer the
// room's is refused (RulesChanged), so nobody agrees to rules they did not
// see.
type RulesAgreeRequest struct {
	Version int `json:"version"`
}

// EventChannelUpdated (ChannelUpdatedPayload) is published by bnbloc-room on
// every change to a channel or a section of the room (create, patch,
// archive, restore, delete). It is room-wide: clients reload the listing,
// and realtime's agent stream filters forget the admission verdicts they
// memoized.
const EventChannelUpdated = "channel_updated"

// ChannelUpdatedPayload names what changed: a channel, a section, or both
// (a section deleted moves its channels).
type ChannelUpdatedPayload struct {
	ChannelID string `json:"channelId,omitempty"`
	SectionID string `json:"sectionId,omitempty"`
}

// Error codes of a channel refusal (httpx.ErrorBody.Code): a post refused by
// the channel's setup, and the setup routes' own refusals.
const (
	// ChannelArchived: the channel is archived (read-only until restored).
	ChannelArchived = "channel_archived"
	// ChannelAdminsOnly: only the room's owner and admins post here.
	ChannelAdminsOnly = "channel_admins_only"
	// ChannelSlowMode: the member posted here less than the channel's slow
	// mode ago; the body's retryAfterMs says how long to wait.
	ChannelSlowMode = "channel_slow_mode"
	// RulesAgreementRequired: the room asks members to agree to its current
	// rules before they post (C16).
	RulesAgreementRequired = "rules_agreement_required"
	// RulesChanged: the rules agreed to are not the room's current version.
	RulesChanged = "rules_changed"
	// AgentsNotAdmitted: agents are not admitted in the channel (the people
	// chat, or a channel whose settings bar them).
	AgentsNotAdmitted = "agents_not_admitted"
)
