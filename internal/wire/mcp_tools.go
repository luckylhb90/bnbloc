package wire

// MCP tool components (docs/prd/2026-10-12-experts-and-teams.md §6) on the
// wire: what content requires and what a job's turn gets.

// ContentMCPRequirement is one requires.mcp entry of a package: an MCP tool
// component by name, and the keys of the component's settings the package
// needs set.
type ContentMCPRequirement struct {
	Name     string   `json:"name"`
	Settings []string `json:"settings,omitempty"`
}

// RoomRelayRoutes are the gateway routes a room-credential MCP tool reaches
// through the bridge's room relay ("METHOD /path"; pkgmeta.MCPCredentialsRoom):
// each admits a job grant (?room=&job=) and acts as the job's requester. A
// route joins the list only with that admission; the relay refuses the
// rest. A {name} segment matches one path segment (a video's id). The
// relay's own answer about the turn is component.RelaySession.
var RoomRelayRoutes = []string{
	"POST /v1/decisions",
	"POST /v1/systemone",
	"POST /v1/media/image",
	"POST /v1/media/transcriptions",
	"POST /v1/media/video",
	"GET /v1/media/video/{id}",
	"POST /v1/live",
}

// MCP transports of a resolved tool (ContentMCPTool.Transport).
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

// ContentMCPTool is one MCP tool component in effect for a job's turn, as
// the bridge driving it needs it. Values carries the tool's platform and
// organization setting values (secrets included): it is answered only to
// the bridge holding the job's lease and never logged or persisted.
type ContentMCPTool struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Transport string `json:"transport"`
	// Artifacts are a stdio server's sha256 pins by platform
	// ("darwin-arm64"): the bridge uses the desktop's materialized binary
	// only when it carries this platform's pin.
	Artifacts map[string]string `json:"artifacts,omitempty"`
	Args      []string          `json:"args,omitempty"`
	// Placement and PrivatePaths are the stdio server's declaration
	// (ComponentMCPStdio): where it runs and what it keeps private.
	Placement    string   `json:"placement,omitempty"`
	PrivatePaths []string `json:"privatePaths,omitempty"`
	// URL and Auth describe an http server.
	URL  string                `json:"url,omitempty"`
	Auth *ComponentMCPHTTPAuth `json:"auth,omitempty"`
	// Settings is the tool's declaration, a setting the content in effect
	// needs marked required.
	Settings []ComponentSetting `json:"settings,omitempty"`
	// Credentials is "room" when the room is the tool's credential.
	Credentials string            `json:"credentials,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	// RequiredBy are the ids of the packages in effect that require it.
	RequiredBy []string `json:"requiredBy"`
}
