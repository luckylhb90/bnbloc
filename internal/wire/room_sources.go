package wire

// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3): a kind of
// organization record a room package's scheduled tasks may read. A package
// asks for one with a purpose; installing it records the ask for the new
// room, and the organization's admin grants it for that room with a scope,
// or withdraws it (the gateway's room_sources.go, the one consent owner).
// A schedule of the room reads it only through the schedule-run tool
// read_records, within the grant.

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
// the package it was installed from ("name@version") and Schedules that
// version's scheduled tasks (what would read: the grant dialog's AI
// summary reads them with the purpose). Scope "" is waiting for the
// admin (never granted, or withdrawn).
type RoomSourceGrant struct {
	RoomID      string                `json:"roomId"`
	RoomName    string                `json:"roomName"`
	Source      string                `json:"source" tstype:"RoomSource"`
	Purpose     string                `json:"purpose"`
	ComponentID string                `json:"componentId"`
	From        string                `json:"from"`
	Schedules   []RoomPackageSchedule `json:"schedules"`
	AskedBy     string                `json:"askedBy"`
	AskedAtMs   int64                 `json:"askedAtMs"`
	Scope       string                `json:"scope,omitempty" tstype:"RoomSourceScope"`
	DecidedBy   string                `json:"decidedBy,omitempty"`
	DecidedAtMs int64                 `json:"decidedAtMs,omitempty"`
}

// RoomSourceGrantsReply is GET /v1/tenant/room-sources: every ask of the
// organization's rooms, newest first.
type RoomSourceGrantsReply struct {
	Grants []RoomSourceGrant `json:"grants"`
}

// RoomSourceDecision is PUT /v1/tenant/room-sources/{roomId}/{source}:
// the scope the admin grants (DELETE withdraws).
type RoomSourceDecision struct {
	Scope string `json:"scope" tstype:"RoomSourceScope"`
}

// RoomSourceAskState is a source the room asks to read and where it
// stands (Scope "" = waiting for the organization admin).
type RoomSourceAskState struct {
	Source  string `json:"source" tstype:"RoomSource"`
	Purpose string `json:"purpose"`
	Scope   string `json:"scope,omitempty" tstype:"RoomSourceScope"`
}

// RoomReviewer is a room that reads this room's records under a grant:
// its name always (members are told which room does), its id only when
// the caller is a member of it (they may open it).
type RoomReviewer struct {
	Source   string `json:"source" tstype:"RoomSource"`
	RoomName string `json:"roomName"`
	RoomID   string `json:"roomId,omitempty"`
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
