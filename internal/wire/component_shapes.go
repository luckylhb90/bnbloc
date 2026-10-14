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

// ComponentLocalSetting is one field of a plugin's desktop-local settings schema.
// Secret values are write-only in the desktop UI (masked, never shown back).
type ComponentLocalSetting struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Help     string `json:"help,omitempty"`
	Secret   bool   `json:"secret,omitempty"`
	Required bool   `json:"required,omitempty"`
}

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
	// Suggestions are the Ask AI panel's starting prompts for the files it
	// shows (docs/prd/2026-10-14-ai-document-editing.md rule 6).
	Suggestions []ComponentPreviewerSuggestion `json:"suggestions,omitempty"`
}

// ComponentPreviewerSuggestion is one starting prompt: its chip label and
// the request it puts in the composer.
type ComponentPreviewerSuggestion struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
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
	// Env are the variables the server needs; values never ship in a
	// package (Credentials says where they come from).
	Env []ComponentMCPEnv `json:"env,omitempty"`
	// Credentials is member or org; required exactly when Env is declared.
	Credentials string             `json:"credentials,omitempty"`
	Stdio       *ComponentMCPStdio `json:"stdio,omitempty"`
	HTTP        *ComponentMCPHTTP  `json:"http,omitempty"`
}

// ComponentMCPTool is one tool of the server.
type ComponentMCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ComponentMCPEnv is one variable an MCP server needs.
type ComponentMCPEnv struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// ComponentMCPStdio is how a stdio server is started: the materialized binary with
// these arguments (env values arrive as its process environment).
type ComponentMCPStdio struct {
	Args []string `json:"args,omitempty"`
}

// ComponentMCPHTTP is how an endpoint server is called.
type ComponentMCPHTTP struct {
	Auth ComponentMCPHTTPAuth `json:"auth"`
}

// ComponentMCPHTTPAuth is the header an endpoint server expects: none, a bearer token
// (Authorization: Bearer <Env>), a named header (<Header>: <Env>), or a
// member's OAuth sign-in (Authorization: Bearer <their access token>).
type ComponentMCPHTTPAuth struct {
	Scheme string `json:"scheme"`
	Header string `json:"header,omitempty"`
	Env    string `json:"env,omitempty"`
	// ClientID (oauth, optional) is a client the publisher registered with
	// the server's authorization server; without it the desktop registers
	// itself where the authorization server allows it.
	ClientID string `json:"clientId,omitempty"`
	// Scopes (oauth, optional) are requested when the server's challenge
	// names none.
	Scopes []string `json:"scopes,omitempty"`
}
