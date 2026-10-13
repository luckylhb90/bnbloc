package wire

// The app gallery (docs/prd/2026-10-13-app-gallery.md): an interactive app
// published as a content component of kind "app" (a template: the site's
// files, never its data), favorites, and "Use this app" (a copy of the
// template as a new site in a room of the caller's).

// An app's category (component.json "category"), the gallery's tabs. An
// app names one of these or none; any other value is refused
// (contentpkg's app kind).
//
//wire:union AppCategory
const (
	AppCategoryOffice       = "office"
	AppCategoryData         = "data"
	AppCategoryProductivity = "productivity"
	AppCategoryContent      = "content"
	AppCategoryLearning     = "learning"
	AppCategoryEngineering  = "engineering"
	AppCategoryFinance      = "finance"
	AppCategoryLife         = "life"
	AppCategoryOther        = "other"
)

// AppCategories lists the categories in the gallery's tab order.
var AppCategories = []string{
	AppCategoryOffice, AppCategoryData, AppCategoryProductivity, AppCategoryContent, AppCategoryLearning,
	AppCategoryEngineering, AppCategoryFinance, AppCategoryLife, AppCategoryOther,
}

// ValidAppCategory reports a known category.
func ValidAppCategory(c string) bool {
	for _, k := range AppCategories {
		if c == k {
			return true
		}
	}
	return false
}

// Whether a member's publish to the organization waits for an org admin
// (the tenant settings' publishApproval; PRD §11). Required is the
// default; with none a publish takes effect at once, still audited, and
// admins can still unpublish, restore or remove it. It covers every
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

// AppPublishRequest is POST /v1/rooms/{roomId}/artifacts/{artifactId}/publish-app:
// snapshot the site's current version (its files, cover and README, never
// its data) as an app component of the caller's. Without ComponentID it
// creates one named Name; with it, the snapshot is the next version of
// that app of the caller's. DisplayName defaults to the site's title.
type AppPublishRequest struct {
	ComponentID string   `json:"componentId,omitempty"`
	Name        string   `json:"name,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Description string   `json:"description,omitempty"`
	Category    string   `json:"category,omitempty" tstype:"AppCategory"`
	Tags        []string `json:"tags,omitempty"`
}

// AppUseRequest is POST /v1/content-components/{id}/use-app: copy the app
// (Version, default its latest) into a room the caller owns (RoomID) or
// into a new room (NewRoom).
type AppUseRequest struct {
	Version string      `json:"version,omitempty"`
	RoomID  string      `json:"roomId,omitempty"`
	NewRoom *AppNewRoom `json:"newRoom,omitempty"`
}

// AppNewRoom names the room "Use this app" creates (default: the app's
// display name).
type AppNewRoom struct {
	Name string `json:"name,omitempty"`
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
