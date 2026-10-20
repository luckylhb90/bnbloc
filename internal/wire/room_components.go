package wire

// Room components (docs/prd/2026-10-20-rooms-as-components.md §2.1): a
// room's setup saved as a content component (contentpkg.KindRoom, its
// manifest checked by contentpkg) and installed as a new room the
// installer owns.

// RoomPackageSchedule is a scheduled task of a room package: an AI task
// or a notice to the room's members (Recipient kind "ai" or "notice"),
// its text and when it recurs. Channel is a channel name of the package
// ("" the room's default channel). Installing creates it disabled, owned
// by the installer.
type RoomPackageSchedule struct {
	Channel    string             `json:"channel,omitempty"`
	Text       string             `json:"text"`
	Recipient  ScheduleRecipient  `json:"recipient"`
	Recurrence ScheduleRecurrence `json:"recurrence"`
	Timezone   string             `json:"timezone"`
}

// RoomSpec is a room package's setup on its Library view (the room
// expert, skill packs and prompt packs are its requires). Apps are works
// by "name@range".
type RoomSpec struct {
	Channels  []string              `json:"channels"`
	Schedules []RoomPackageSchedule `json:"schedules"`
	Apps      []string              `json:"apps"`
	Sources   []RoomSourceAsk       `json:"sources"`
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

// RoomComponentDraft answers GET /v1/rooms/{roomId}/component-draft: what
// "Save as component" captures of a room for its owner or an admin, to be
// reviewed and saved as a room package through the content form. Name is
// a package name proposed from the room's. Sources are the room's own asks
// (each with its purpose, granted or not: a package asks again for the
// room installed from it). Left says, a line each, what of the room a
// package does not hold.
type RoomComponentDraft struct {
	RoomID       string                `json:"roomId"`
	Name         string                `json:"name"`
	DisplayName  string                `json:"displayName"`
	Description  string                `json:"description"`
	Welcome      string                `json:"welcome"`
	QuickPrompts []RoomQuickPrompt     `json:"quickPrompts"`
	Channels     []string              `json:"channels"`
	Schedules    []RoomPackageSchedule `json:"schedules"`
	Expert       *RoomDraftPart        `json:"expert,omitempty"`
	Skills       []RoomDraftPart       `json:"skills"`
	Prompts      []RoomDraftPart       `json:"prompts"`
	Apps         []RoomDraftPart       `json:"apps"`
	Sources      []RoomSourceAsk       `json:"sources"`
	Left         []string              `json:"left"`
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
// paused), the room expert, the packs turned on and the apps copied in
// (labels), what was left out and why (Skipped), and what the room asks
// for that nobody granted yet: its sources (each waits for the
// organization admin's consent, room_sources.go) and its MCP tools. The
// package's welcome and quick prompts are the new room's own (RoomDetail).
type RoomInstallReply struct {
	RoomID      string          `json:"roomId"`
	Name        string          `json:"name"`
	ComponentID string          `json:"componentId"`
	From        string          `json:"from"`
	Channels    []string        `json:"channels"`
	Schedules   []string        `json:"schedules"`
	Expert      string          `json:"expert,omitempty"`
	Enabled     []string        `json:"enabled"`
	Apps        []string        `json:"apps"`
	Skipped     []string        `json:"skipped"`
	Sources     []RoomSourceAsk `json:"sources"`
	Tools       []string        `json:"tools"`
}
