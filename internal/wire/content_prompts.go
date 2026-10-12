package wire

// Prompt packs (docs/prd/2026-10-12-experts-and-teams.md §4) on the wire:
// a pack's prompts on its ContentComponent, an expert quick prompt's
// resolved reference, and the "Save as prompt" route.

// ContentPrompt is one prompt of a prompt pack. Body holds {{name}}
// placeholders, each one of Variables.
type ContentPrompt struct {
	ID        string                  `json:"id"`
	Title     string                  `json:"title"`
	Body      string                  `json:"body"`
	Variables []ContentPromptVariable `json:"variables,omitempty"`
	Tags      []string                `json:"tags,omitempty"`
	// Slash is the composer command that inserts it ("/release-notes").
	Slash string `json:"slash,omitempty"`
	// Uses counts its insertions by every member.
	Uses int64 `json:"uses"`
}

// ContentPromptVariable is one {{name}} of a prompt: a text field, or a
// select of Options.
type ContentPromptVariable struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Default string   `json:"default,omitempty"`
	Options []string `json:"options,omitempty"`
}

// ContentPromptRef is a pack's prompt an expert quick prompt resolved to:
// the pack (component id, name, version in effect) and the prompt.
type ContentPromptRef struct {
	ComponentID string        `json:"componentId"`
	Pack        string        `json:"pack"`
	Version     string        `json:"version"`
	Prompt      ContentPrompt `json:"prompt"`
}

// SavePromptRequest is POST /v1/content-components/prompts: a prompt saved
// into the caller's personal prompt pack (created on the first save).
type SavePromptRequest struct {
	Title string   `json:"title,omitempty"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags,omitempty"`
}

// SavePromptReply answers it: the pack at its new version and the saved
// prompt's id.
type SavePromptReply struct {
	Component ContentComponent `json:"component"`
	PromptID  string           `json:"promptId"`
	// Created is true when this save created the pack.
	Created bool `json:"created"`
}
