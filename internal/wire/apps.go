package wire

import "encoding/json"

// Interactive apps' data API (docs/prd/2026-10-12-interactive-apps.md §4.1):
// the bodies the room service's app data routes take and answer
// (services/room/appdata.go), which the gateway relays for the app SDK. A
// document as an app reads it is a JSON object: the app's fields plus the
// server's stamps (_id, _by, _createdAt, _updatedAt, _version; _author for
// the app's owners only).

// AppDocWrite is an insert (POST) or a replace (PUT). Doc is the document's
// JSON object; stamps in it are ignored. Public opts the document into
// being readable by everyone holding the app (insert: default false;
// replace: unchanged when absent).
type AppDocWrite struct {
	Doc    json.RawMessage `json:"doc"`
	Public *bool           `json:"public,omitempty"`
}

// AppDocPatch is a PATCH: Merge is a JSON merge patch (RFC 7396) and Inc
// adds numbers at top-level or dotted paths; Public as in AppDocWrite.
type AppDocPatch struct {
	Merge  json.RawMessage `json:"merge,omitempty"`
	Inc    json.RawMessage `json:"$inc,omitempty"`
	Public *bool           `json:"public,omitempty"`
}

// AppDocPage is one page of a find. Next is the cursor of the following
// page (pass it as after=), empty on the last page.
type AppDocPage struct {
	Docs []map[string]any `json:"docs"`
	Next string           `json:"next,omitempty"`
}

// AppMe is who the caller is to the app: ID is their per-app pseudonym (the
// _by of what they write); Name their display name, for a signed-in
// member only.
type AppMe struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// AppDataStats is an app's usage, for its owners.
type AppDataStats struct {
	Documents   int64            `json:"documents"`
	Bytes       int64            `json:"bytes"`
	Collections map[string]int64 `json:"collections"`
}

// AppDataWiped answers an owner's delete of all of an app's data.
type AppDataWiped struct {
	Deleted int64 `json:"deleted"`
}
