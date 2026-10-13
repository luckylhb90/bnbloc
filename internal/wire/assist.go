package wire

// AI assist (docs/prd/2026-10-13-ai-assist.md): one fast model call behind
// every manual entry point, through one API. A task is an assist content
// component resolved by name; the assist model is the deployment's, set by
// a super admin.

// Assist task outputs (an assist component's manifest "output").
//
//wire:union AssistOutput
const (
	AssistOutputText = "text"
	AssistOutputJSON = "json"
)

// AssistOutputs are the outputs an assist task may declare.
var AssistOutputs = []string{AssistOutputText, AssistOutputJSON}

// Context an assist task may receive (its manifest "context"), resolved by
// the server from the ids in the request after checking the caller may
// read them.
//
//wire:union AssistContext
const (
	AssistContextRoom        = "room"
	AssistContextArtifact    = "artifact"
	AssistContextAttachments = "attachments"
)

// AssistContexts are the contexts an assist task may declare.
var AssistContexts = []string{AssistContextRoom, AssistContextArtifact, AssistContextAttachments}

// Bounds of an assist request.
const (
	MaxAssistText        = 8000
	MaxAssistAttachments = 20
)

// AssistStatus is GET /v1/assist: whether assists are available to the
// caller (a configured model, and an organization that has not turned
// them off) and the tasks they can run.
type AssistStatus struct {
	Available bool         `json:"available"`
	Tasks     []AssistTask `json:"tasks"`
}

// AssistTask is one runnable task: the component it resolved to, its
// output and, for JSON output, the fields its reply holds.
type AssistTask struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName,omitempty"`
	ComponentID string        `json:"componentId"`
	Output      string        `json:"output" tstype:"AssistOutput"`
	Fields      []AssistField `json:"fields,omitempty"`
	Context     []string      `json:"context,omitempty" tstype:"AssistContext"`
}

// AssistSpec is an assist component's task (its component.json output,
// fields and context), on its Library view.
type AssistSpec struct {
	Output  string        `json:"output" tstype:"AssistOutput"`
	Fields  []AssistField `json:"fields,omitempty"`
	Context []string      `json:"context,omitempty" tstype:"AssistContext"`
}

// AssistField is one field of a JSON task's reply.
type AssistField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// List marks a field holding a list of strings (tags).
	List bool `json:"list,omitempty"`
}

// AssistRequest is POST /v1/assist/{task}: what the member typed and the
// ids of what it refers to.
type AssistRequest struct {
	Text          string   `json:"text"`
	RoomID        string   `json:"roomId,omitempty"`
	ArtifactID    string   `json:"artifactId,omitempty"`
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
}

// AssistUsage is a call's token counts and latency.
type AssistUsage struct {
	Model            string `json:"model"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	LatencyMs        int64  `json:"latencyMs"`
}

// AssistDelta is a text task's "delta" event: the next piece of the reply.
type AssistDelta struct {
	Text string `json:"text"`
}

// AssistDone is a text task's "done" event.
type AssistDone struct {
	Usage AssistUsage `json:"usage"`
}

// AssistJSONReply is a JSON task's answer: the declared fields only
// (strings, or string lists for list fields).
type AssistJSONReply struct {
	Fields map[string]any `json:"fields"`
	Usage  AssistUsage    `json:"usage"`
}

// AssistModelConfig is GET /v1/platform/assist-model: the deployment's
// assist model. The key is never returned, only whether one is set.
// Configurable is false without a secret store key (the key is never
// stored in clear).
type AssistModelConfig struct {
	Configurable bool   `json:"configurable"`
	BaseURL      string `json:"baseUrl"`
	Model        string `json:"model"`
	KeySet       bool   `json:"keySet"`
	UpdatedBy    string `json:"updatedBy,omitempty"`
	UpdatedAtMs  int64  `json:"updatedAtMs,omitempty"`
}

// AssistModelUpdate is PUT /v1/platform/assist-model. An empty APIKey
// keeps the stored key; ClearKey removes it. An empty BaseURL turns the
// model off.
type AssistModelUpdate struct {
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey,omitempty"`
	ClearKey bool   `json:"clearKey,omitempty"`
}

// AssistModelTest is POST /v1/platform/assist-model/test: one short
// completion through the stored model.
type AssistModelTest struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Reply string      `json:"reply,omitempty"`
	Usage AssistUsage `json:"usage"`
}
