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
}

// Section visibilities. Slice 1 accepts only SectionOpen.
//
//wire:union SectionVisibility
const (
	// SectionOpen: every room member sees its channels.
	SectionOpen = "open"
	// SectionMembers: only its members see its channels (slice 2).
	SectionMembers = "members"
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
	JoinPolicy  string   `json:"joinPolicy"`
	CreatedBy   string   `json:"createdBy"`
	CreatedAtMs int64    `json:"createdAtMs"`
}

// ChannelList is GET /v1/rooms/{id}/channels: the room's channels (live and
// not archived; ?archived=1 the archived ones; the people chat never) and
// its sections.
type ChannelList struct {
	Channels []RoomChannel `json:"channels"`
	Sections []RoomSection `json:"sections"`
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
// it for themselves.
type ChannelFollowRequest struct {
	Starred *bool `json:"starred,omitempty"`
	Hidden  *bool `json:"hidden,omitempty"`
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

// SectionCreateRequest is POST /v1/rooms/{id}/sections.
type SectionCreateRequest struct {
	Name       string `json:"name"`
	Rules      string `json:"rules,omitempty"`
	Position   int    `json:"position,omitempty"`
	Visibility string `json:"visibility,omitempty" tstype:"SectionVisibility"`
}

// SectionPatchRequest is PATCH /v1/rooms/{id}/sections/{sectionId}.
type SectionPatchRequest struct {
	Name     *string   `json:"name,omitempty"`
	Rules    *string   `json:"rules,omitempty"`
	Position *int      `json:"position,omitempty"`
	Owners   *[]string `json:"owners,omitempty"`
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
