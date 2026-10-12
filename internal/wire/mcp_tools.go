package wire

// MCP tool components (docs/prd/2026-10-12-experts-and-teams.md §6) on the
// wire: what content requires, what a job's turn gets, and the org
// connectors org admins configure.

// ContentMCPRequirement is one requires.mcp entry of a package: an MCP tool
// component by name, and the component's env names the package needs set.
type ContentMCPRequirement struct {
	Name string   `json:"name"`
	Env  []string `json:"env,omitempty"`
}

// MCP transports of a resolved tool (ContentMCPTool.Transport).
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http"
)

// ContentMCPTool is one MCP tool component in effect for a job's turn, as
// the bridge driving it needs it. Values carries an org connector's values
// (secrets): it is answered only to the bridge holding the job's lease and
// never logged or persisted.
type ContentMCPTool struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Transport string `json:"transport"`
	// Artifacts are a stdio server's sha256 pins by platform
	// ("darwin-arm64"): the bridge uses the desktop's materialized binary
	// only when it carries this platform's pin.
	Artifacts map[string]string `json:"artifacts,omitempty"`
	Args      []string          `json:"args,omitempty"`
	// URL and Auth describe an http server.
	URL  string          `json:"url,omitempty"`
	Auth *ContentMCPAuth `json:"auth,omitempty"`
	Env  []ContentMCPEnv `json:"env,omitempty"`
	// Credentials is "member" or "org" ("" when the tool takes no env).
	Credentials string            `json:"credentials,omitempty"`
	Values      map[string]string `json:"values,omitempty"`
	// RequiredBy are the ids of the packages in effect that require it.
	RequiredBy []string `json:"requiredBy"`
}

// ContentMCPAuth is the header an http MCP server expects (scheme none,
// bearer or header, Env naming the value's variable; or oauth: the
// requester's own sign-in from their desktop, attached as a bearer token by
// their bridge and never lent to someone else's drive).
type ContentMCPAuth struct {
	Scheme string `json:"scheme"`
	Header string `json:"header,omitempty"`
	Env    string `json:"env,omitempty"`
}

// ContentMCPEnv is one variable a resolved tool takes; Required means the
// turn needs it set (declared required, or named by a requiring package).
type ContentMCPEnv struct {
	Name     string `json:"name"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// MCPConnectorField is one variable of an org connector as an org admin
// sees it: a secret value is never returned (Set says whether one is
// stored).
type MCPConnectorField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Set         bool   `json:"set"`
	Value       string `json:"value,omitempty"`
}

// MCPConnector is one org-credential MCP tool component visible to the
// organization, with its connector's state.
type MCPConnector struct {
	Name        string              `json:"name"`
	Version     string              `json:"version"`
	Description string              `json:"description,omitempty"`
	Transport   string              `json:"transport"`
	Configured  bool                `json:"configured"`
	Fields      []MCPConnectorField `json:"fields"`
	UpdatedBy   string              `json:"updatedBy,omitempty"`
	UpdatedAtMs int64               `json:"updatedAtMs,omitempty"`
}

// MCPConnectorsReply answers GET /v1/tenant/mcp-connectors. Available is
// false when the gateway has no secret store key (org connectors are off).
type MCPConnectorsReply struct {
	Available  bool           `json:"available"`
	Connectors []MCPConnector `json:"connectors"`
}

// MCPConnectorUpdate is PUT /v1/tenant/mcp-connectors/{name}: the values to
// store (an empty secret keeps the stored one).
type MCPConnectorUpdate struct {
	Values map[string]string `json:"values"`
}
