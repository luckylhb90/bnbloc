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
// content component (components save) for the job's requester, in the
// job's room, or the save of a new version sent it to the organization. The
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
	// request to the organization, opened by components publish or by the
	// save of a new version of a component the organization uses) or
	// published (the same, where the organization requires no approval).
	Action string `json:"action" tstype:"ContentSavedAction"`
	// Created marks a new component (not a new version).
	Created bool `json:"created,omitempty"`
	// Owner is the member it was saved for (the job's requester).
	Owner string `json:"owner"`
}

// PublishProposal is what an agent proposes to share for the person who
// asked it (components publish, docs/prd/2026-10-20-room-setup-by-conversation.md
// §2.3): a notice row of kind NoticePublishProposal carries it as
// payload.proposal. Clients show it, as a card with a Publish button, only
// to For (the job's requester); the share happens when they click it (the
// publish route, as them), never from the tool call.
type PublishProposal struct {
	ComponentID string `json:"componentId"`
	Kind        string `json:"kind" tstype:"ContentKind"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	// Scope is who it would be shared with (ContentScopeOrg).
	Scope string `json:"scope"`
	// Note is the agent's note for the organization's admins.
	Note string `json:"note,omitempty"`
	For  string `json:"for"`
}
