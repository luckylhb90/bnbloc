package wire

// Component package shapes: the parts of a signed component package
// (core/pkgmeta.Package) that the gateway also shows in its component
// projections (components.go). They are defined once, here, so the web's
// types are generated from them; core/pkgmeta names them for package
// authors (pkgmeta.Sidecar = ComponentSidecar, …) and validates them.

// ComponentEndpoint is part of a component package (core/pkgmeta).
type ComponentEndpoint struct {
	BaseURL string `json:"baseUrl"`
}

// ComponentArtifact is part of a component package (core/pkgmeta).
type ComponentArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// ComponentPermissions is part of a component package (core/pkgmeta).
type ComponentPermissions struct {
	Network []string `json:"network,omitempty"`
	// Host lists the desktop plugin host API capabilities a plugin sidecar
	// may use (plugin packages only; PluginHostPermissions is the registry).
	// A plugin declaring any gets BNBLOC_PLUGIN_HOST_URL/_TOKEN at launch.
	Host []string `json:"host,omitempty"`
}

// ComponentSetting is one setting a component declares in its manifest
// (pkgmeta.Setting; docs/prd/2026-10-19-component-settings.md): what it
// holds (Type), who sets it and for whom (Scope), and how its value
// reaches the component (Deliver; every value is also in the component's
// settings file). Every value is text: a number in decimal, a boolean
// "true" or "false". core/pkgmeta validates a declaration and a value.
type ComponentSetting struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	Type  string `json:"type" tstype:"ComponentSettingType"`
	Scope string `json:"scope" tstype:"ComponentSettingScope"`
	// Required: the component does not run until it is set ("needs setup").
	Required bool `json:"required,omitempty"`
	// Default is the value used while none is set (never a secret's).
	Default string `json:"default,omitempty"`
	// Options are a choice's values.
	Options []string                 `json:"options,omitempty"`
	Deliver *ComponentSettingDeliver `json:"deliver,omitempty"`
}

// ComponentSettingDeliver says how a setting's value reaches the component
// beyond its settings file: an environment variable of its process, or a
// request header of an http MCP tool (a named header, or Authorization:
// Bearer <value>).
type ComponentSettingDeliver struct {
	Env    string `json:"env,omitempty"`
	Header string `json:"header,omitempty"`
	Bearer bool   `json:"bearer,omitempty"`
}

// ComponentPage is part of a component package (core/pkgmeta): a page of the
// component's own, an HTML file (Entry) of a static bundle the platform shows
// in a sandboxed frame. The component's settings page
// (docs/prd/2026-10-19-component-settings.md §2.2a) is one, where the one
// settings form would be; its panel (docs/architecture/plugin-panels.md) is
// another. The bundle is an archive in the previewer bundle format, pinned
// here (a gateway artifact's url and sha256) rather than in the package's
// artifacts, which are a plugin's or a stdio tool's per-platform binaries (an
// http tool has none).
type ComponentPage struct {
	Entry  string            `json:"entry"`
	Bundle ComponentArtifact `json:"bundle"`
}

// The types of a setting (ComponentSetting.Type). A secret is write-only:
// never shown back, only "set". A path is a path on the member's own
// machine (member scope only). Lines is a list of text lines (folders,
// name patterns, names): stored and sent between the platform's pieces as
// its lines joined by "\n" (so is a deliver.env value), and handed to the
// component as a JSON array of strings in its settings file and on its
// settings page.
//
//wire:union ComponentSettingType
const (
	SettingText    = "text"
	SettingSecret  = "secret"
	SettingURL     = "url"
	SettingNumber  = "number"
	SettingBoolean = "boolean"
	SettingChoice  = "choice"
	SettingPath    = "path"
	SettingLines   = "lines"
)

// Who sets a setting, and for whom it holds (ComponentSetting.Scope): the
// super admin for the deployment, an organization admin for their
// organization, each member for themselves.
//
//wire:union ComponentSettingScope
const (
	SettingScopePlatform = "platform"
	SettingScopeOrg      = "org"
	SettingScopeMember   = "member"
)

// ComponentSidecar is part of a component package (core/pkgmeta).
type ComponentSidecar struct {
	ListenEnv string `json:"listenEnv,omitempty"`
	// SupervisedEnv names the env var the desktop sets to "1" at launch so the
	// sidecar knows it is supervised and exits when its stdin pipe closes.
	SupervisedEnv string   `json:"supervisedEnv,omitempty"`
	LocalURL      string   `json:"localUrl"`
	Args          []string `json:"args,omitempty"`
	// TokenEnv names the env var the desktop sets to a fresh random bearer
	// token at every launch. A sidecar declaring it must require that bearer
	// on every request; the desktop hands the same token to its local callers.
	TokenEnv string `json:"tokenEnv,omitempty"`
	// DataDirEnv names the env var the desktop sets to a per-component
	// private data directory it creates (owner-only) before launch.
	DataDirEnv string `json:"dataDirEnv,omitempty"`
	// MountsEnv names the env var the desktop sets to the descriptor file a
	// plugin with the "sandbox-mounts" capability writes its read-only sandbox
	// mounts to (docs/architecture/sandbox-mounts.md); MountRootEnv the folder
	// the host side of those mounts must be under. Both only with that
	// capability.
	MountsEnv    string `json:"mountsEnv,omitempty"`
	MountRootEnv string `json:"mountRootEnv,omitempty"`
	// Files are extra per-platform artifacts the sidecar needs beside its
	// executable (e.g. a VMM helper binary and a firmware library). Each is
	// published in Package.Artifacts under "<platform>/<name>", downloaded
	// and sha256-verified by the desktop exactly like the executable, and its
	// local path is handed to the sidecar in env Env.
	Files []ComponentSidecarFile `json:"files,omitempty"`
}

// ComponentSidecarFile declares one extra sidecar artifact (ComponentSidecar.Files).
type ComponentSidecarFile struct {
	Name string `json:"name"`
	Env  string `json:"env"`
	// Executable marks a file the desktop must mark executable (+x).
	Executable bool `json:"executable,omitempty"`
}

// ComponentGuest is a sandbox package's machine profile (pkgmeta.Package.Guest).
type ComponentGuest struct {
	// Image is the OCI reference the machine boots (never a host path).
	Image     string `json:"image"`
	CPUs      int    `json:"cpus,omitempty"`
	MemoryMiB int    `json:"memoryMiB,omitempty"`
}

// ComponentPreviewer is a previewer package's payload (pkgmeta.Package.Previewer).
type ComponentPreviewer struct {
	// Entry is the bundle-relative HTML document the frame loads.
	Entry string    `json:"entry"`
	Match FileMatch `json:"match"`
	// Rank orders previewers matching a file with equal specificity, like
	// the web registry's built-ins (lower first).
	Rank         int                             `json:"rank"`
	Capabilities []string                        `json:"capabilities,omitempty"`
	Toolbar      []ComponentPreviewerToolbarItem `json:"toolbar,omitempty"`
}

// FileMatch declares the files something applies to: the files a
// previewer renders, the files a prompt-pack prompt is for. One validator:
// internal/filematch.
type FileMatch struct {
	MimeTypes  []string `json:"mimeTypes,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

// ComponentPreviewerToolbarItem is one declared toolbar control; the host renders it
// with its own icon for Icon (a code-owned name).
type ComponentPreviewerToolbarItem struct {
	ID    string `json:"id"`
	Icon  string `json:"icon"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// ComponentMCP is an MCP component's payload (pkgmeta.Package.MCP).
type ComponentMCP struct {
	// Tools are the tools the server offers, for display (the server's own
	// tools/list stays authoritative at run time).
	Tools []ComponentMCPTool `json:"tools"`
	// Credentials is "room" (the room the agent works in is the
	// credential: the bridge gives a stdio server the room relay) or empty.
	// Everything else a server needs is a setting (Package.Settings).
	Credentials string             `json:"credentials,omitempty"`
	Stdio       *ComponentMCPStdio `json:"stdio,omitempty"`
	HTTP        *ComponentMCPHTTP  `json:"http,omitempty"`
}

// ComponentMCPTool is one tool of the server.
type ComponentMCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ComponentMCPStdio is how a stdio server is started: the materialized binary with
// these arguments (setting values arrive as its process environment and
// settings file).
type ComponentMCPStdio struct {
	Args []string `json:"args,omitempty"`
	// Placement is where the server runs: "agent" (or empty) as a child of
	// the agent CLI, "host" as a host process of the bridge that the agent
	// reaches only as an MCP server (docs/prd/2026-10-22-host-run-mcp-tools.md).
	Placement string `json:"placement,omitempty"`
	// PrivatePaths (placement "host" only) are what the server keeps its
	// state in, relative to the user's config directory: a core that
	// confines its agent CLI denies reading them.
	PrivatePaths []string `json:"privatePaths,omitempty"`
}

// Where a stdio MCP server runs (ComponentMCPStdio.Placement).
const (
	MCPPlacementAgent = "agent"
	MCPPlacementHost  = "host"
)

// RunsOnHost reports whether the declaration places the server on the host:
// the one question the gateway's install, the job's tool list and the bridge
// ask.
func (s *ComponentMCPStdio) RunsOnHost() bool { return s != nil && s.Placement == MCPPlacementHost }

// ComponentMCPHTTP is how an endpoint server is called.
type ComponentMCPHTTP struct {
	Auth ComponentMCPHTTPAuth `json:"auth"`
}

// ComponentMCPHTTPAuth is the credential an endpoint server expects: none,
// a bearer token (Authorization: Bearer <the Setting's value>), a named
// header (<Header>: <the Setting's value>), or a member's OAuth sign-in
// (Authorization: Bearer <their access token>).
type ComponentMCPHTTPAuth struct {
	Scheme string `json:"scheme"`
	Header string `json:"header,omitempty"`
	// Setting is the key of the setting carrying a bearer or header
	// scheme's value.
	Setting string `json:"setting,omitempty"`
	// ClientID (oauth, optional) is a client the publisher registered with
	// the server's authorization server; without it the desktop registers
	// itself where the authorization server allows it.
	ClientID string `json:"clientId,omitempty"`
	// Scopes (oauth, optional) are requested when the server's challenge
	// names none.
	Scopes []string `json:"scopes,omitempty"`
}
