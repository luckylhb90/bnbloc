package wire

// Room components (docs/prd/2026-10-20-rooms-as-components.md §2.1): a
// room's setup saved as a content component (contentpkg.KindRoom, its
// manifest checked by contentpkg) and installed as a new room the
// installer owns. Since docs/prd/2026-10-20-complete-room-capture.md a
// package also carries the room's instructions, the apps made in it and
// the files its saver ticks.

// RoomPackageSchedule is a scheduled task of a room package: an AI task,
// a notice to the room's members or a request to an agent without the
// agent (Recipient kind "ai", "notice" or "agent"), its text and when it
// recurs. Channel is a channel name of the package ("" the room's default
// channel). Installing creates it disabled, owned by the installer; a
// request waits for the installer to pick one of their agents.
type RoomPackageSchedule struct {
	Channel    string             `json:"channel,omitempty"`
	Text       string             `json:"text"`
	Recipient  ScheduleRecipient  `json:"recipient"`
	Recurrence ScheduleRecurrence `json:"recurrence"`
	Timezone   string             `json:"timezone"`
}

// Bounds of what a room package carries beyond its setup
// (docs/prd/2026-10-20-complete-room-capture.md §2.2): contentpkg's room
// checks hold a package to them, and the save dialog shows a capture's
// size against them.
const (
	// MaxRoomCarryBytes bounds the apps' sites and the files a room
	// package carries, together: one site snapshot's cap
	// (tarbundle.SiteLimits; a test holds them equal).
	MaxRoomCarryBytes = 64 << 20
	// MaxRoomFiles bounds the files a room package carries.
	MaxRoomFiles = 20
)

// RoomAppSnapshot is an app made in a room that its package carries: its
// title and the package folder holding its site ("apps/<name>"), copied
// into the new room as a new site, without its data.
type RoomAppSnapshot struct {
	Title string `json:"title"`
	Dir   string `json:"dir"`
}

// RoomPackageFile is a room file its package carries: its name in the new
// room, the package path holding it ("files/<name>") and its size.
type RoomPackageFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
}

// RoomSpec is a room package's setup on its Library view (the room
// expert, skill packs and prompt packs are its requires). Apps are works
// by "name@range"; AppSnapshots the apps made in the room it carries;
// Files the room files it carries.
type RoomSpec struct {
	Channels     []string              `json:"channels"`
	Schedules    []RoomPackageSchedule `json:"schedules"`
	Apps         []string              `json:"apps"`
	AppSnapshots []RoomAppSnapshot     `json:"appSnapshots"`
	Files        []RoomPackageFile     `json:"files"`
	Sources      []RoomSourceAsk       `json:"sources"`
}

// RoomDraftPart is a component a room uses, as a draft captured it: its
// id, names, the version in effect and Ref, the "name@^major" a package
// names it by.
type RoomDraftPart struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	Ref         string `json:"ref"`
}

// RoomDraftDeliverable is a deliverable of the room a draft offers to
// carry: an app made in the room (a site: its current version as a site
// snapshot) or a room file (its current version), with its size.
type RoomDraftDeliverable struct {
	ArtifactID string `json:"artifactId"`
	Title      string `json:"title"`
	FileName   string `json:"fileName,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	SizeBytes  int64  `json:"sizeBytes"`
}

// RoomDraftTool is an MCP tool component the room's runs had (the run
// records' tools): its name and the permission a package requiring it
// declares (mcp.stdio, or mcp.http:<host>).
type RoomDraftTool struct {
	Name       string `json:"name"`
	Permission string `json:"permission"`
}

// RoomComponentDraft answers GET /v1/rooms/{roomId}/component-draft: what
// "Save as component" captures of a room for its owner or an admin, to be
// reviewed and saved as a room package through the content form. Name is
// a package name proposed from the room's. Sources are the room's own asks
// (each with its purpose, granted or not: a package asks again for the
// room installed from it). MadeApps and Files are what the room made that
// a package can carry, for the saver to tick (a save names the ticked ones
// in its include); Tools are the MCP tools its recent runs had, which a
// package requires. Left says, a line each, what of the room a package
// does not hold.
type RoomComponentDraft struct {
	RoomID       string                 `json:"roomId"`
	Name         string                 `json:"name"`
	DisplayName  string                 `json:"displayName"`
	Description  string                 `json:"description"`
	Welcome      string                 `json:"welcome"`
	QuickPrompts []RoomQuickPrompt      `json:"quickPrompts"`
	Channels     []string               `json:"channels"`
	Schedules    []RoomPackageSchedule  `json:"schedules"`
	Expert       *RoomDraftPart         `json:"expert,omitempty"`
	Skills       []RoomDraftPart        `json:"skills"`
	Prompts      []RoomDraftPart        `json:"prompts"`
	Apps         []RoomDraftPart        `json:"apps"`
	MadeApps     []RoomDraftDeliverable `json:"madeApps"`
	Files        []RoomDraftDeliverable `json:"files"`
	Tools        []RoomDraftTool        `json:"tools"`
	Sources      []RoomSourceAsk        `json:"sources"`
	Left         []string               `json:"left"`
}

// RoomInclude is what a room package's content save takes from the room it
// captures (the save request's "include"): the room, and by artifact id the
// apps made in it and the room files to carry, each read as the saver reads
// it (the room's owner or an admin). The bytes never pass through the
// client.
type RoomInclude struct {
	RoomID string   `json:"roomId"`
	Apps   []string `json:"apps,omitempty"`
	Files  []string `json:"files,omitempty"`
}

// RoomInstallRequest is POST /v1/content-components/{id}/create-room: the
// version to install (default the latest) and the new room's id (one is
// made when empty), name (default the component's) and visibility
// ("private" by default, or "shared").
type RoomInstallRequest struct {
	Version    string `json:"version,omitempty"`
	RoomID     string `json:"roomId,omitempty"`
	Name       string `json:"name,omitempty"`
	Visibility string `json:"visibility,omitempty"`
}

// RoomInstallReply is what installing a room package set up: the new room,
// From ("name@version"), its channels, the ids of its schedules (all
// paused) and, of them, the requests to an agent that wait for the
// installer to pick one of their agents (WaitingForAgent: each one's text
// in a line), the room expert, the packs turned on and the apps copied in
// (labels), the expert made from the package's instructions (Instructions,
// its label), the apps made in the source room and the files copied in
// (labels), what was left out and why (Skipped), and what the room asks
// for that nobody granted yet: its sources (each waits for the organization
// admin's consent, room_sources.go) and its MCP tools, of which ToolsToAdd
// are listed to the installer but not added yet (the install dialog offers
// Add). The package's welcome and quick prompts are the new room's own
// (RoomDetail).
type RoomInstallReply struct {
	RoomID          string          `json:"roomId"`
	Name            string          `json:"name"`
	ComponentID     string          `json:"componentId"`
	From            string          `json:"from"`
	Channels        []string        `json:"channels"`
	Schedules       []string        `json:"schedules"`
	WaitingForAgent []string        `json:"waitingForAgent"`
	Expert          string          `json:"expert,omitempty"`
	Enabled         []string        `json:"enabled"`
	Apps            []string        `json:"apps"`
	Instructions    string          `json:"instructions,omitempty"`
	MadeApps        []string        `json:"madeApps"`
	Files           []string        `json:"files"`
	Skipped         []string        `json:"skipped"`
	Sources         []RoomSourceAsk `json:"sources"`
	Tools           []string        `json:"tools"`
	ToolsToAdd      []string        `json:"toolsToAdd"`
}
