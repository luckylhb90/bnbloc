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
	// Prompts are a prompt pack's prompts (content_prompts.go).
	Prompts []ContentPrompt `json:"prompts,omitempty"`
	// Mine is true for the caller's own personal component: they author it
	// (new versions, sharing, publishing).
	Mine bool `json:"mine"`
	// Removable is true when the caller may delete it: their own personal
	// component or a shared copy they hold.
	Removable bool `json:"removable"`
	// SharedBy is the author of a shared copy (Owner is its holder).
	SharedBy string `json:"sharedBy,omitempty"`
	// SourceID is the author's component a shared copy or an org
	// publication was made from.
	SourceID string `json:"sourceId,omitempty"`
	// SourceLatestVersion is, on a shared copy, the author's latest version
	// when it is newer than the copy's ("update to the author's latest").
	SourceLatestVersion string `json:"sourceLatestVersion,omitempty"`
	// Publication is, on the caller's own personal component, its
	// organization publication and latest publish request.
	Publication *ContentPublication `json:"publication,omitempty"`
	// Acked is true when the caller confirmed this version's declared
	// permissions (always true for content they authored and for one that
	// declares none).
	Acked bool `json:"acked"`
	// Uses counts the members and rooms that enabled or pinned it.
	Uses int `json:"uses"`
	// Runs counts the agent runs that wore an expert and Members their
	// distinct requesters; for a pack, Members counts the members who
	// turned it on for themselves. Each is capped (store.MaxUsageCount).
	Runs    int `json:"runs"`
	Members int `json:"members"`
	// Team is a team expert's roster (as its manifest names it).
	Team        *ContentTeam `json:"team,omitempty"`
	CreatedAtMs int64        `json:"createdAtMs"`
	UpdatedAtMs int64        `json:"updatedAtMs"`
}

// ContentRequires mirrors a manifest's requires.
type ContentRequires struct {
	Skills      []string                `json:"skills,omitempty"`
	Prompts     []string                `json:"prompts,omitempty"`
	MCP         []ContentMCPRequirement `json:"mcp,omitempty"`
	Permissions []string                `json:"permissions,omitempty"`
}

// ContentSkill is one skill of a package.
type ContentSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// ContentQuickPrompt is one expert quick prompt: its text, or Ref
// ("pack/prompt-id") to a prompt of a pack the expert requires, Resolved
// for the viewer when they may use such a pack (nil: the pack is missing).
type ContentQuickPrompt struct {
	Title    string            `json:"title"`
	Prompt   string            `json:"prompt"`
	Ref      string            `json:"ref,omitempty"`
	Resolved *ContentPromptRef `json:"resolved,omitempty"`
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
	// ContentUseRoomExpert is a room's expert: one of its pinned experts
	// every agent turn in the room wears (scope id = the room).
	ContentUseRoomExpert = "room-expert"
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
	// RoomExpert is the id of the room's expert (one of its pinned
	// experts), when it has one.
	RoomExpert string `json:"roomExpert,omitempty"`
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
	ContentSourceRoomExpert  = "room-expert"     // the room's expert
)

// ContentRef is one resolved package in effect for a turn.
type ContentRef struct {
	ComponentID string `json:"componentId"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	BundleKey   string `json:"bundleKey"`
	// Owner is the package's author (a shared copy's author, not its
	// holder; "" for platform content): content that is not the agent
	// owner's own and needs the sandbox forces a sandboxed drive.
	Owner        string   `json:"owner"`
	Source       string   `json:"source"`
	Permissions  []string `json:"permissions,omitempty"`
	NeedsSandbox bool     `json:"needsSandbox,omitempty"`
	// MCP are the MCP tool components the package requires.
	MCP []ContentMCPRequirement `json:"mcp,omitempty"`
}

// ContentEffective answers GET /v1/content-components/effective?job=&room=
// for the bridge driving the job: the expert the chat picked and the skill
// packs in effect (room, the agent owner's, the expert's requirements), and
// requirements no usable package satisfies.
type ContentEffective struct {
	Expert *ContentRef `json:"expert,omitempty"`
	// RoomExpert is the room's expert, worn under the chat's own (absent
	// when the room has none, or it is the chat's expert).
	RoomExpert *ContentRef `json:"roomExpert,omitempty"`
	// Team is the chat expert's roster when it is a team, each member's
	// expert resolved as the requester.
	Team       *ContentTeam `json:"team,omitempty"`
	SkillPacks []ContentRef `json:"skillPacks"`
	// MCP are the MCP tool components the packages above require that the
	// requester may use (mcp_tools.go).
	MCP     []ContentMCPTool `json:"mcp,omitempty"`
	Missing []string         `json:"missing,omitempty"`
}

// Content scopes on the wire (ContentComponent.Scope, publish requests).
const (
	ContentScopePersonal = "personal"
	ContentScopeShared   = "shared"
	ContentScopeOrg      = "org"
	ContentScopePlatform = "platform"
)

// ContentPublication is an author's view of their component's
// organization publication: the org copy members use (its version and
// status, absent until a first approval) and the latest publish request.
type ContentPublication struct {
	OrgComponentID string                 `json:"orgComponentId,omitempty"`
	OrgVersion     string                 `json:"orgVersion,omitempty"`
	OrgStatus      string                 `json:"orgStatus,omitempty"`
	Request        *ContentPublishRequest `json:"request,omitempty"`
}

// ContentPublishRequest is a request to publish a version of an author's
// component to the organization (status pending, approved, declined or
// withdrawn).
type ContentPublishRequest struct {
	ID           string `json:"id"`
	ComponentID  string `json:"componentId"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	DisplayName  string `json:"displayName,omitempty"`
	Version      string `json:"version"`
	RequestedBy  string `json:"requestedBy"`
	Note         string `json:"note,omitempty"`
	Status       string `json:"status"`
	DecidedBy    string `json:"decidedBy,omitempty"`
	DecisionNote string `json:"decisionNote,omitempty"`
	DecidedAtMs  int64  `json:"decidedAtMs,omitempty"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	// ApprovedVersion is the version the organization uses now ("" before
	// a first approval).
	ApprovedVersion string `json:"approvedVersion,omitempty"`
}

// ContentShareRequest is POST /v1/content-components/{id}/shares: named
// members of the author's organization and/or a room (its human members),
// at a version (default: the latest), with an optional note for the
// notification.
type ContentShareRequest struct {
	Members []string `json:"members,omitempty"`
	RoomID  string   `json:"roomId,omitempty"`
	Version string   `json:"version,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// ContentShareReply answers a share: per recipient, the copy and whether it
// was created, moved to the version (updated) or already had it
// (unchanged); recipients skipped with the reason.
type ContentShareReply struct {
	Version string               `json:"version"`
	Shared  []ContentShareResult `json:"shared"`
	Skipped []ContentShareSkip   `json:"skipped"`
}

// Share outcomes (ContentShareResult.Status).
const (
	ContentShareCreated   = "created"
	ContentShareUpdated   = "updated"
	ContentShareUnchanged = "unchanged"
)

// ContentShareResult is one recipient's copy.
type ContentShareResult struct {
	Member      string `json:"member"`
	ComponentID string `json:"componentId"`
	Version     string `json:"version"`
	Status      string `json:"status"`
}

// ContentShareSkip is a recipient a share did not reach.
type ContentShareSkip struct {
	Member string `json:"member"`
	Reason string `json:"reason"`
}

// ContentCopy is one shared copy of an author's component
// (GET /v1/content-components/{id}/shares).
type ContentCopy struct {
	Member      string `json:"member"`
	ComponentID string `json:"componentId"`
	Version     string `json:"version"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
}

// ContentPublishInput is POST /v1/content-components/{id}/publish: scope
// "org" opens a publish request for Version (default: the latest);
// "personal" withdraws a pending request and takes the component out of
// the organization; "platform" is refused with what a platform admin does.
type ContentPublishInput struct {
	Scope   string `json:"scope"`
	Version string `json:"version,omitempty"`
	Note    string `json:"note,omitempty"`
}

// ContentPublishReply answers a publish: the scope now in effect and the
// request it opened (org).
type ContentPublishReply struct {
	Scope   string                 `json:"scope"`
	Request *ContentPublishRequest `json:"request,omitempty"`
	Note    string                 `json:"note"`
}

// ContentDecisionInput approves or declines a publish request.
type ContentDecisionInput struct {
	Note string `json:"note,omitempty"`
}

// ContentReview is GET /v1/tenant/content-requests/{id}: what an org admin
// reviews before approving. Component is the requested version (declared
// permissions, requirements with MCP tools, a team's roster, skills);
// ScriptFiles are the files that execute; Previous is the version the
// organization uses now (nil for a first publication) and Files the
// changes from it, file by file (every file is "added" for a first one).
type ContentReview struct {
	Request     ContentPublishRequest `json:"request"`
	Component   ContentComponent      `json:"component"`
	ScriptFiles []string              `json:"scriptFiles"`
	Previous    *ContentComponent     `json:"previous,omitempty"`
	Files       []ContentFileChange   `json:"files"`
}

// File changes of a review (ContentFileChange.Change).
const (
	ContentFileAdded   = "added"
	ContentFileRemoved = "removed"
	ContentFileChanged = "changed"
)

// ContentFileChange is one file that differs between two versions: its
// text before and after (bounded; Truncated when a side was cut), or
// Binary.
type ContentFileChange struct {
	Path      string `json:"path"`
	Change    string `json:"change"`
	Binary    bool   `json:"binary,omitempty"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ContentAdminComponent is an organization's or the platform's published
// component as an admin manages it: the component (any status) and its
// author.
type ContentAdminComponent struct {
	Component ContentComponent `json:"component"`
	TenantID  string           `json:"tenantId,omitempty"`
}
