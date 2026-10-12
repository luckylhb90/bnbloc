package wire

// Content components (docs/prd/2026-10-12-experts-and-teams.md): experts and
// skill packs on the wire. JSON tags are the public contract the web client,
// the bridge and the CLI read. Kind is the short kind ("expert", "skill").

// ContentComponent is one content component as a member sees it, at its
// latest version (or the version a detail names).
type ContentComponent struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName,omitempty"`
	Description   string   `json:"description,omitempty"`
	Category      string   `json:"category,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Icon          string   `json:"icon,omitempty"`
	Owner         string   `json:"owner"`
	Scope         string   `json:"scope"`
	Status        string   `json:"status"`
	LatestVersion string   `json:"latestVersion"`
	// Version is the version the rest of the fields describe (the latest
	// in a listing).
	Version string `json:"version"`
	// Permissions are the declared permissions (scripts, mcp.stdio,
	// mcp.http:<host>); NeedsSandbox is true when they force a sandboxed
	// drive for anyone but the author.
	Permissions  []string             `json:"permissions"`
	NeedsSandbox bool                 `json:"needsSandbox,omitempty"`
	Requires     ContentRequires      `json:"requires"`
	Skills       []ContentSkill       `json:"skills,omitempty"`
	QuickPrompts []ContentQuickPrompt `json:"quickPrompts,omitempty"`
	Welcome      string               `json:"welcome,omitempty"`
	// Mine is true for the caller's own component.
	Mine bool `json:"mine"`
	// Acked is true when the caller confirmed this version's declared
	// permissions (always true for their own component and for one that
	// declares none).
	Acked bool `json:"acked"`
	// Uses counts the members and rooms that enabled or pinned it.
	Uses        int   `json:"uses"`
	CreatedAtMs int64 `json:"createdAtMs"`
	UpdatedAtMs int64 `json:"updatedAtMs"`
}

// ContentRequires mirrors a manifest's requires.
type ContentRequires struct {
	Skills      []string `json:"skills,omitempty"`
	Prompts     []string `json:"prompts,omitempty"`
	MCP         []string `json:"mcp,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// ContentSkill is one skill of a package.
type ContentSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ContentQuickPrompt is one expert quick prompt.
type ContentQuickPrompt struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
}

// ContentVersion is one immutable version.
type ContentVersion struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	BundleKey string `json:"bundleKey"`
	// BundleURL is the download path of the bundle (GET /v1/artifacts/…).
	BundleURL   string `json:"bundleUrl"`
	SizeBytes   int64  `json:"sizeBytes"`
	CreatedBy   string `json:"createdBy"`
	CreatedAtMs int64  `json:"createdAtMs"`
}

// ContentComponentDetail answers GET /v1/content-components/{id}.
type ContentComponentDetail struct {
	Component ContentComponent `json:"component"`
	Versions  []ContentVersion `json:"versions"`
}

// ContentFile is one file of a version: its text when it is UTF-8 and
// small enough to view.
type ContentFile struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"sizeBytes"`
	Text      string `json:"text,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
}

// ContentVersionDetail answers GET /v1/content-components/{id}/versions/{v}:
// the component as of that version, its instructions (an expert) and its
// files, and each skill's body for the Library form.
type ContentVersionDetail struct {
	Component    ContentComponent  `json:"component"`
	Version      ContentVersion    `json:"version"`
	Instructions string            `json:"instructions,omitempty"`
	SkillBodies  map[string]string `json:"skillBodies,omitempty"`
	Files        []ContentFile     `json:"files"`
}

// Content use scopes (ContentUse.Scope).
const (
	ContentUseMember = "member"
	ContentUseRoom   = "room"
)

// ContentUse is where a component is enabled (a member's skill packs, a
// room's skill packs) or pinned (a room's experts).
type ContentUse struct {
	Scope         string            `json:"scope"`
	ScopeID       string            `json:"scopeId"`
	ComponentID   string            `json:"componentId"`
	Kind          string            `json:"kind"`
	PinnedVersion string            `json:"pinnedVersion,omitempty"`
	CreatedBy     string            `json:"createdBy"`
	CreatedAtMs   int64             `json:"createdAtMs"`
	Component     *ContentComponent `json:"component,omitempty"`
	// UpdateAvailable is true when a pinned version is behind the latest.
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
}

// ContentUsesReply answers GET /v1/content-uses?room=: the caller's member
// uses and, with a room, the room's.
type ContentUsesReply struct {
	Member []ContentUse `json:"member"`
	Room   []ContentUse `json:"room,omitempty"`
}

// ContentUseRequest is PUT /v1/content-uses.
type ContentUseRequest struct {
	Scope       string `json:"scope"`
	RoomID      string `json:"roomId,omitempty"`
	ComponentID string `json:"componentId"`
	// Version pins a version; empty follows the latest.
	Version string `json:"version,omitempty"`
}

// ChatExpert is the expert a chat asks its agent to wear: a component id
// and an optional version (empty = the latest when the chat is posted;
// the job records the version actually used).
type ChatExpert struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// Content sources of an effective package (ContentRef.Source).
const (
	ContentSourceExpert      = "expert"          // the chat's expert
	ContentSourceExpertNeeds = "expert-requires" // a skill pack the expert requires
	ContentSourceRoom        = "room"            // a room skill pack
	ContentSourceMember      = "member"          // the agent owner's skill pack
)

// ContentRef is one resolved package in effect for a turn.
type ContentRef struct {
	ComponentID  string   `json:"componentId"`
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName,omitempty"`
	Version      string   `json:"version"`
	SHA256       string   `json:"sha256"`
	BundleKey    string   `json:"bundleKey"`
	Owner        string   `json:"owner"`
	Source       string   `json:"source"`
	Permissions  []string `json:"permissions,omitempty"`
	NeedsSandbox bool     `json:"needsSandbox,omitempty"`
}

// ContentEffective answers GET /v1/content-components/effective?job=&room=
// for the bridge driving the job: the expert the chat picked and the skill
// packs in effect (room, the agent owner's, the expert's requirements), and
// requirements no usable package satisfies.
type ContentEffective struct {
	Expert     *ContentRef  `json:"expert,omitempty"`
	SkillPacks []ContentRef `json:"skillPacks"`
	Missing    []string     `json:"missing,omitempty"`
}
