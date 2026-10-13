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
	// AssistContextLibrary: the experts and skill packs the member can use.
	AssistContextLibrary = "library"
	// AssistContextClock: the current time in the member's timezone.
	AssistContextClock = "clock"
)

// AssistContexts are the contexts an assist task may declare.
var AssistContexts = []string{AssistContextRoom, AssistContextArtifact, AssistContextAttachments, AssistContextLibrary, AssistContextClock}

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

// A JSON task's field shapes (AssistField.Type).
//
//wire:union AssistFieldType
const (
	// AssistFieldText is a string (the default).
	AssistFieldText = "text"
	// AssistFieldList is a list of strings (tags).
	AssistFieldList = "list"
	// AssistFieldItems is a list of objects whose fields (Item) are strings.
	AssistFieldItems = "items"
)

// AssistFieldTypes are the field shapes a JSON task may declare.
var AssistFieldTypes = []string{AssistFieldText, AssistFieldList, AssistFieldItems}

// MaxAssistItems bounds an items field of a reply.
const MaxAssistItems = 20

// AssistField is one field of a JSON task's reply: a string, a list of
// strings, or a list of objects whose own fields (Item, strings only) it
// names.
type AssistField struct {
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Type        string        `json:"type,omitempty" tstype:"AssistFieldType"`
	Item        []AssistField `json:"item,omitempty"`
}

// AssistRequest is POST /v1/assist/{task}: what the member typed and the
// ids of what it refers to. ChannelID is the room channel the composer is
// in (its recent messages; "" = the room's default channel); ExpertID the
// expert picked for the message (else the room's). An artifact and the
// attachments are the room's, so they need RoomID.
type AssistRequest struct {
	Text          string   `json:"text"`
	RoomID        string   `json:"roomId,omitempty"`
	ChannelID     string   `json:"channelId,omitempty"`
	ExpertID      string   `json:"expertId,omitempty"`
	ArtifactID    string   `json:"artifactId,omitempty"`
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
	// Timezone is the member's IANA timezone (the clock context; UTC when
	// absent or unknown).
	Timezone string `json:"timezone,omitempty"`
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

// AssistJSONReply is a JSON task's answer: the declared fields only, in
// their shapes (a string, a list of strings, a list of string objects).
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
	// OrgHosts are the hosts an organization's own model may use (platform
	// model only; none = organizations cannot set their own).
	OrgHosts    []string `json:"orgHosts,omitempty"`
	UpdatedBy   string   `json:"updatedBy,omitempty"`
	UpdatedAtMs int64    `json:"updatedAtMs,omitempty"`
}

// AssistModelUpdate is PUT /v1/platform/assist-model (and, without
// OrgHosts, PUT /v1/tenant/assist-model). An empty APIKey keeps the
// stored key; ClearKey removes it. An empty BaseURL turns the model off
// (an organization's: back to the platform's).
type AssistModelUpdate struct {
	BaseURL  string   `json:"baseUrl"`
	Model    string   `json:"model"`
	APIKey   string   `json:"apiKey,omitempty"`
	ClearKey bool     `json:"clearKey,omitempty"`
	OrgHosts []string `json:"orgHosts,omitempty"`
}

// AssistOrgModel is GET /v1/tenant/assist-model: the organization's own
// model (Model.BaseURL "" = it uses the platform's) and the hosts it may
// name.
type AssistOrgModel struct {
	Model AssistModelConfig `json:"model"`
	// AllowedHosts are the platform's OrgHosts; empty = an organization
	// cannot set its own model.
	AllowedHosts []string `json:"allowedHosts"`
	// PlatformModel names the platform's model ("" = none), shown as what
	// members use when the organization sets none.
	PlatformModel string `json:"platformModel,omitempty"`
}

// AssistModelTest is POST /v1/platform/assist-model/test (and
// /v1/tenant/assist-model/test): one short completion through the stored
// model.
type AssistModelTest struct {
	OK    bool        `json:"ok"`
	Error string      `json:"error,omitempty"`
	Reply string      `json:"reply,omitempty"`
	Usage AssistUsage `json:"usage"`
}
