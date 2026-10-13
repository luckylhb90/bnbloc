package wire

// The works gallery (docs/prd/2026-10-13-app-gallery.md,
// docs/prd/2026-10-13-works-gallery-and-make-similar.md): any deliverable
// published as a content component of kind "work" (a snapshot of one
// deliverable version, never its data), favorites and "Use this app" (an
// app's copy as a new site).

// A work's category (component.json "category"), the gallery's tabs. A
// work names one of these or none; any other value is refused
// (contentpkg's work kind).
//
//wire:union WorkCategory
const (
	WorkCategoryOffice       = "office"
	WorkCategoryData         = "data"
	WorkCategoryProductivity = "productivity"
	WorkCategoryContent      = "content"
	WorkCategoryLearning     = "learning"
	WorkCategoryEngineering  = "engineering"
	WorkCategoryFinance      = "finance"
	WorkCategoryLife         = "life"
	WorkCategoryOther        = "other"
)

// WorkCategories lists the categories in the gallery's tab order.
var WorkCategories = []string{
	WorkCategoryOffice, WorkCategoryData, WorkCategoryProductivity, WorkCategoryContent, WorkCategoryLearning,
	WorkCategoryEngineering, WorkCategoryFinance, WorkCategoryLife, WorkCategoryOther,
}

// ValidWorkCategory reports a known category.
func ValidWorkCategory(c string) bool {
	for _, k := range WorkCategories {
		if c == k {
			return true
		}
	}
	return false
}

// A work's type (component.json "type"): what the deliverable it
// snapshots is. It is derived from the deliverable (contentpkg.WorkTypeOf,
// the one rule), never typed by hand: an app is a site that loads the app
// SDK (store.Artifact.IsApp), a document is markdown, PDF, text or an
// office document.
//
//wire:union WorkType
const (
	WorkTypeApp      = "app"
	WorkTypeSite     = "site"
	WorkTypeImage    = "image"
	WorkTypeVideo    = "video"
	WorkTypeAudio    = "audio"
	WorkTypeDocument = "document"
	WorkTypeSlides   = "slides"
	WorkTypeTable    = "table"
	WorkTypeOther    = "other"
)

// WorkTypes lists the types in the gallery's filter order.
var WorkTypes = []string{
	WorkTypeApp, WorkTypeImage, WorkTypeVideo, WorkTypeAudio, WorkTypeDocument, WorkTypeSlides,
	WorkTypeTable, WorkTypeSite, WorkTypeOther,
}

// WorkTypeIsSite reports a type whose files are a site (an entry page run
// in the sandboxed site frame).
func WorkTypeIsSite(t string) bool { return t == WorkTypeApp || t == WorkTypeSite }

// Whether a member's publish to the organization waits for an org admin
// (the tenant settings' publishApproval; app gallery PRD §11). Required is
// the default; with none a publish takes effect at once, still audited,
// and admins can still unpublish, restore or remove it. It covers every
// content kind; contentshare.NeedsApproval is its one reader.
//
//wire:union PublishApproval
const (
	PublishApprovalRequired = "required"
	PublishApprovalNone     = "none"
)

// ValidPublishApproval reports a known setting.
func ValidPublishApproval(p string) bool {
	return p == PublishApprovalRequired || p == PublishApprovalNone
}

// WorkPublishRequest is POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-work:
// snapshot the deliverable's current version (a site's files, or the one
// file, never its data) as a work component of the caller's. Without
// ComponentID it creates one named Name; with it, the snapshot is the next
// version of that work of the caller's. DisplayName defaults to the
// deliverable's title. The recipe is captured from the producing job
// (WorkRecipeCapture): Recipe overrides it (its references are a subset
// of the captured ones, named by attachmentId), and IncludeRecipe false
// publishes none.
type WorkPublishRequest struct {
	ComponentID   string      `json:"componentId,omitempty"`
	Name          string      `json:"name,omitempty"`
	DisplayName   string      `json:"displayName,omitempty"`
	Description   string      `json:"description,omitempty"`
	Category      string      `json:"category,omitempty" tstype:"WorkCategory"`
	Tags          []string    `json:"tags,omitempty"`
	Recipe        *WorkRecipe `json:"recipe,omitempty"`
	IncludeRecipe *bool       `json:"includeRecipe,omitempty"`
}

// WorkRecipe is how a work was made (works PRD §3): the request that
// produced the deliverable version, the expert, skill packs and prompt
// packs the turn used, the files the request attached (stored in the
// work's bundle under references/) and the agent. It is captured from the
// producing job when the work is published, shown to the author first,
// and what "Make similar" starts from. Agent is informational only.
type WorkRecipe struct {
	Prompt     string           `json:"prompt,omitempty"`
	Expert     *WorkRecipePart  `json:"expert,omitempty"`
	Skills     []WorkRecipePart `json:"skills,omitempty"`
	Prompts    []WorkRecipePart `json:"prompts,omitempty"`
	References []WorkReference  `json:"references,omitempty"`
	Agent      *WorkRecipeAgent `json:"agent,omitempty"`
}

// WorkRecipePart is a content component a recipe used: an expert, a skill
// pack or a prompt pack, at the version the turn used.
type WorkRecipePart struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version,omitempty"`
}

// WorkReference is one input file of a recipe. In a work's manifest File
// is its path in the bundle (references/<name>); while a recipe is only
// captured (before publishing) AttachmentID names the request's
// attachment it comes from and File is empty.
type WorkReference struct {
	File         string `json:"file,omitempty"`
	AttachmentID string `json:"attachmentId,omitempty"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	SizeBytes    int64  `json:"sizeBytes"`
}

// WorkRecipeAgent is the agent that produced the work (informational:
// "Make similar" runs on the member's own choice).
type WorkRecipeAgent struct {
	Core     string `json:"core,omitempty"`
	Provider string `json:"provider"`
}

// WorkRecipeCapture answers GET /v1/rooms/{roomId}/artifacts/{artifactId}/recipe:
// the recipe a publish of the deliverable's current version (Version)
// captures; Recipe is absent when no producing job is found (an uploaded
// file). Skipped names the request's attachments left out (over the
// reference caps, wire.MaxWorkReferences and MaxWorkReferenceBytes).
type WorkRecipeCapture struct {
	Version int         `json:"version"`
	Recipe  *WorkRecipe `json:"recipe,omitempty"`
	Skipped []string    `json:"skipped,omitempty"`
}

// AppUseRequest is POST /v1/content-components/{id}/use-app: copy an app
// work (Version, default its latest) into a room the caller owns (RoomID)
// or into a new room (NewRoom).
type AppUseRequest struct {
	Version string       `json:"version,omitempty"`
	RoomID  string       `json:"roomId,omitempty"`
	NewRoom *WorkNewRoom `json:"newRoom,omitempty"`
}

// WorkNewRoom names the room "Use this app" creates
// (default: the work's display name).
type WorkNewRoom struct {
	Name string `json:"name,omitempty"`
}

// AppDemo answers POST /v1/content-components/{id}/versions/{v}/demo:
// where an app version's demo runs. SiteURL is a site URL (on the sites
// origin when the deployment has one) whose ~app/sdk.js is the SDK in demo
// mode: its data lives in the page's memory only, and no app data call
// is answered under it.
type AppDemo struct {
	SiteURL     string `json:"siteUrl"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// AppUseReply answers "Use this app": the new site deliverable, which
// starts with an empty data space of its own.
type AppUseReply struct {
	RoomID      string `json:"roomId"`
	ArtifactID  string `json:"artifactId"`
	Version     int    `json:"version"`
	CreatedRoom bool   `json:"createdRoom,omitempty"`
	// From attributes the copy: "<name>@<version>".
	From string `json:"from"`
}
