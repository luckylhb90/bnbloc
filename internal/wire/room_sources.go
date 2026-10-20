package wire

// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3): a kind of
// organization record a room package's scheduled tasks may read. A package
// asks for one with a purpose; installing it records the ask for the new
// room, and the organization's admin grants it for that private room, read
// by its owner, with a scope, or withdraws it (the gateway's
// room_sources.go, the one consent owner). A schedule of that person in the
// room reads it only through the schedule-run tool read_records, within
// the grant.

// Room sources a package may ask for.
//
//wire:union RoomSource
const (
	// RoomSourceRuns is the organization's agent conversations: request
	// and reply text, the agent, the outcome, the room and the time.
	RoomSourceRuns = "runs"
)

// RoomSources are the sources a room package may ask for: the one list.
var RoomSources = []string{RoomSourceRuns}

// RoomSourceAsk is a source a room package asks for (its manifest's
// sources[]): the source and the purpose its tasks read it for, in the
// author's words, shown to the admin who decides.
type RoomSourceAsk struct {
	Name    string `json:"name" tstype:"RoomSource"`
	Purpose string `json:"purpose"`
}

// What a grant covers: the rooms whose records the reading room may read.
//
//wire:union RoomSourceScope
const (
	// RoomSourceScopeAll: every room of the organization.
	RoomSourceScopeAll = "all"
	// RoomSourceScopeOptedIn: only the rooms whose owner opted in.
	RoomSourceScopeOptedIn = "opted-in"
	// RoomSourceScopeNonePrivate: every room but the private ones (invite
	// only, store.VisibilityPrivate).
	RoomSourceScopeNonePrivate = "none-private"
)

// RoomSourceScopes are the scopes an admin may grant.
var RoomSourceScopes = []string{RoomSourceScopeAll, RoomSourceScopeOptedIn, RoomSourceScopeNonePrivate}

// RoomSourceGrant is one room's ask for a source and the organization
// admin's answer, on the console (GET /v1/tenant/room-sources). From is
// the package it was installed from ("name@version"). Scope "" is waiting
// for the admin (never granted, withdrawn, or paused when the room's owner
// changed). Grantee is the person the grant is pinned to (the room's owner
// when it was granted): only their schedules read it. Changed counts the
// grantee's tasks changed since the admin consented to them, until an
// admin acknowledges.
type RoomSourceGrant struct {
	RoomID      string `json:"roomId"`
	RoomName    string `json:"roomName"`
	Source      string `json:"source" tstype:"RoomSource"`
	Purpose     string `json:"purpose"`
	ComponentID string `json:"componentId"`
	From        string `json:"from"`
	AskedBy     string `json:"askedBy"`
	AskedAtMs   int64  `json:"askedAtMs"`
	Scope       string `json:"scope,omitempty" tstype:"RoomSourceScope"`
	Grantee     string `json:"grantee,omitempty"`
	DecidedBy   string `json:"decidedBy,omitempty"`
	DecidedAtMs int64  `json:"decidedAtMs,omitempty"`
	Changed     int    `json:"changed,omitempty"`
}

// RoomSourceGrantsReply is GET /v1/tenant/room-sources: every ask of the
// organization's rooms, newest first.
type RoomSourceGrantsReply struct {
	Grants []RoomSourceGrant `json:"grants"`
}

// RoomSourceTask is one of a grantee's AI tasks in the reading room as the
// admin consented to it: the schedule and its words.
type RoomSourceTask struct {
	ScheduleID string `json:"scheduleId"`
	Text       string `json:"text"`
}

// RoomSourceDrift is a grantee's AI task created or changed since the
// admin consented: its words then (Before, "" for a new task) and now,
// and the AI explanation the admins were sent (explain-source-use's
// summary and mismatch; empty when no explanation could be made).
type RoomSourceDrift struct {
	ScheduleID string `json:"scheduleId"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Summary    string `json:"summary,omitempty"`
	Mismatch   string `json:"mismatch,omitempty"`
	AtMs       int64  `json:"atMs"`
}

// RoomSourceDetail is GET /v1/tenant/room-sources/{roomId}/{source}: one
// ask with what its admin decides on. Shared: the reading room is shared
// (a grant needs a private room: its members read what it reads). Owner:
// the room's owner, the person a grant is pinned to. Members: the room's
// human members now. Tasks: the owner's AI tasks in the room now (what
// would read). Drift: the grantee's tasks changed since the admin
// consented. ExplainInput: the explain-source-use assist's <text> for the
// ask (its purpose and the tasks, a changed one with its words before).
type RoomSourceDetail struct {
	Grant        RoomSourceGrant   `json:"grant"`
	Shared       bool              `json:"shared"`
	Owner        string            `json:"owner"`
	Members      []RoomMember      `json:"members"`
	Tasks        []RoomSchedule    `json:"tasks"`
	Drift        []RoomSourceDrift `json:"drift"`
	ExplainInput string            `json:"explainInput"`
}

// RoomSourceDecision is PUT /v1/tenant/room-sources/{roomId}/{source}:
// the scope the admin grants and the person it is read by, the room's
// owner as the admin saw them (a grant to someone no longer the owner is
// refused). DELETE withdraws.
type RoomSourceDecision struct {
	Scope   string `json:"scope" tstype:"RoomSourceScope"`
	Grantee string `json:"grantee"`
}

// RoomSourceAskState is a source the room asks to read and where it
// stands (Scope "" = waiting for the organization admin) and the person
// a grant is pinned to.
type RoomSourceAskState struct {
	Source  string `json:"source" tstype:"RoomSource"`
	Purpose string `json:"purpose"`
	Scope   string `json:"scope,omitempty" tstype:"RoomSourceScope"`
	Grantee string `json:"grantee,omitempty"`
}

// RoomReviewer is a room that reads this room's records under a grant:
// its name always (members are told which room does) and the person whose
// tasks read, its id only when the caller is a member of it (they may
// open it).
type RoomReviewer struct {
	Source   string `json:"source" tstype:"RoomSource"`
	RoomName string `json:"roomName"`
	RoomID   string `json:"roomId,omitempty"`
	Grantee  string `json:"grantee"`
}

// RoomSourcesInfo is GET /v1/rooms/{roomId}/sources for a member: what the
// room asks to read and where each ask stands, which rooms read its own
// records (transparency), and the sources its owner opted it in to (for
// grants of scope opted-in).
type RoomSourcesInfo struct {
	Asks       []RoomSourceAskState `json:"asks"`
	ReviewedBy []RoomReviewer       `json:"reviewedBy"`
	OptedIn    []string             `json:"optedIn" tstype:"RoomSource"`
}

// RoomSourceOptIn is PUT /v1/rooms/{roomId}/sources/{source}/opt-in: the
// room's owner opts its records in to (or out of) grants of scope
// opted-in.
type RoomSourceOptIn struct {
	OptedIn bool `json:"optedIn"`
}
