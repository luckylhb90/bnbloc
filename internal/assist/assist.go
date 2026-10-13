// Package assist is the one owner of AI assists
// (docs/prd/2026-10-13-ai-assist.md): one fast model call behind every
// manual entry point (the composer's "enhance prompt", a form's "Fill
// with AI").
//
//   - The assist model (settings.go): the deployment's OpenAI-compatible
//     chat completions endpoint, set by a super admin, its key sealed.
//   - The client (client.go): a streamed text reply or a JSON reply, held
//     to the configured host.
//   - The prompt (prompt.go): the task's instructions as the system
//     message, then one user message where everything is fenced as data.
//   - The reply (reply.go): a JSON reply held to the task's fields.
//   - The tasks (tasks.go): assist content components resolved by name,
//     nearest first.
//
// The gateway serves it (services/gateway/assist.go); nothing else calls a
// model.
package assist

import (
	"errors"
	"fmt"
)

// Model is the deployment's assist model: an OpenAI-compatible base URL
// (".../v1", where /chat/completions answers), the model name and the API
// key ("" for a proxy that needs none).
type Model struct {
	BaseURL string
	Model   string
	APIKey  string
	// Org marks an organization's own model (Settings.Effective).
	Org bool
}

// Configured reports a model assists can run on.
func (m Model) Configured() bool { return m.BaseURL != "" && m.Model != "" }

// Bounds of one call.
const (
	// MaxReplyTokens bounds a reply (max_tokens).
	MaxReplyTokens = 2000
	// maxReplyBytes bounds what is read of a model's answer.
	maxReplyBytes = 1 << 20
)

// ModelError is a model's refusal or failure: its HTTP status and message
// (bounded, from the provider's error body).
type ModelError struct {
	Status  int
	Message string
}

func (e *ModelError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("the assist model answered %d", e.Status)
	}
	return fmt.Sprintf("the assist model answered %d: %s", e.Status, e.Message)
}

// ErrBadReply: a JSON task's reply is not an object holding its fields.
var ErrBadReply = errors.New("the assist model's reply is not the task's JSON object")
