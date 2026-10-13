package wire

// EventContentUsesChanged is a transient room event: a room's content uses
// (pinned experts, the room expert, room skill and prompt packs) changed.
// Clients refetch GET /v1/content-uses?room= on it, so the composer and the
// room settings follow without reopening the room. Payload:
// ContentUsesChanged.
const EventContentUsesChanged = "content_uses_changed"

// ContentUsesChanged is the payload of EventContentUsesChanged.
type ContentUsesChanged struct {
	Scope       string `json:"scope"`
	ComponentID string `json:"componentId"`
}

// EventContentSaved is a transient room event: the room tools saved a
// content component (save_component) or asked to publish one
// (publish_component) for the job's requester, in the job's room. The
// workshop shows the save from it ("Saved expert …" with "Open in Library"
// and "Try it now") instead of re-listing the Library after every agent
// reply. Payload: ContentSaved; clients act only on their own (Owner).
const EventContentSaved = "content_saved"

// ContentSaved actions: a save, a publish request to the organization, or
// a publication to it that took effect at once (the organization requires
// no approval).
//
//wire:union ContentSavedAction
const (
	ContentSavedSaved     = "saved"
	ContentSavedSubmitted = "submitted"
	ContentSavedPublished = "published"
)

// ContentSaved is the payload of EventContentSaved.
type ContentSaved struct {
	ComponentID string `json:"componentId"`
	Kind        string `json:"kind" tstype:"ContentKind"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	// Action is saved (a new component or version), submitted (a publish
	// request to the organization, opened by publish_component or by the
	// save of a new version of a component the organization uses) or
	// published (the same, where the organization requires no approval).
	Action string `json:"action" tstype:"ContentSavedAction"`
	// Created marks a new component (not a new version).
	Created bool `json:"created,omitempty"`
	// Owner is the member it was saved for (the job's requester).
	Owner string `json:"owner"`
}
