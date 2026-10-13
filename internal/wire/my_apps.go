package wire

// A member's apps across rooms (docs/prd/2026-10-13-app-gallery.md §5,
// Library → "My apps"): GET /v1/my/apps[?cursor=&limit=] lists the
// interactive apps (Artifact.IsApp) of the rooms of the caller's effective
// tenant where the caller is one of the app's owners (a room member who may
// chat), newest-updated first. The room service answers it
// (services/room/my_apps.go).

// MyApp is one app of the listing.
type MyApp struct {
	// AppID is the site artifact's id (the app's data space is keyed by it).
	AppID       string `json:"appId"`
	Name        string `json:"name"`
	RoomID      string `json:"roomId"`
	RoomName    string `json:"roomName"`
	Version     int    `json:"version"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
	// Producer made the latest version.
	Producer AppProducer `json:"producer"`
	// Documents is the app's document count (its owners' stats); 0 when
	// the app holds no data.
	Documents int64 `json:"documents"`
	// Links are the app's share links that work now (not revoked, not
	// expired), newest first.
	Links []AppLink `json:"links"`
}

// AppProducer is who produced a version: an agent (Owner, Provider, Core),
// or a person (Person) for a manual publish; empty when the source job's
// run record is gone.
type AppProducer struct {
	Owner    string `json:"owner,omitempty"`
	Provider string `json:"provider,omitempty"`
	Core     string `json:"core,omitempty"`
	Person   string `json:"person,omitempty"`
}

// AppLink is one working share link of an app.
type AppLink struct {
	ShareID     string `json:"shareId"`
	Access      string `json:"access" tstype:"ShareAccess"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
	// Version is the pinned version; 0 follows the latest.
	Version int `json:"version"`
}

// MyAppList is GET /v1/my/apps; NextCursor pages on (absent on the last
// page).
type MyAppList struct {
	Apps       []MyApp `json:"apps"`
	NextCursor string  `json:"nextCursor,omitempty"`
}
