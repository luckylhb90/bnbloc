package wire

// ContentDraft answers POST /v1/content-drafts?job=&room= (the room tools'
// components try): a staged, unsaved expert or skill pack that a job the staging
// job dispatches may wear until ExpiresAtMs. It is never listed in a
// Library. Dispatch it with DispatchRequest.Draft = ID.
type ContentDraft struct {
	ID          string `json:"id"`
	Kind        string `json:"kind" tstype:"ContentKind"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}
