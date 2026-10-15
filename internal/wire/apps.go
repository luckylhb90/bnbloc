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
	// Account is the caller's own verified account, for a signed-in caller
	// (a member, or a person who signed in to open an organization link);
	// absent for an anonymous visitor and for an agent
	// (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §3).
	Account *AppAccount `json:"account,omitempty"`
}

// AppAccount is who a signed-in caller is: their login (the identity
// provider's subject, what they sign in with) and the email it asserted
// ("" when it asserted none).
type AppAccount struct {
	Login string `json:"login"`
	Email string `json:"email,omitempty"`
}

// AppDataStats is an app's usage, for its owners.
type AppDataStats struct {
	Documents   int64            `json:"documents"`
	Bytes       int64            `json:"bytes"`
	Collections map[string]int64 `json:"collections"`
	// Files and FileBytes are the files the app's pages uploaded.
	Files     int64 `json:"files"`
	FileBytes int64 `json:"fileBytes"`
}

// AppFile is a file an app's page uploaded (app.files.upload), as the app
// reads it: its id (what the app keeps in its own documents), name, type
// (the attachment type it was checked as) and size, and the stamps a
// document carries: _by (the uploader's per-app id), _createdAt, and
// _author for the app's owners only.
type AppFile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Size      int64  `json:"size"`
	Public    bool   `json:"public"`
	By        string `json:"_by"`
	CreatedAt int64  `json:"_createdAt"`
	Author    string `json:"_author,omitempty"`
}

// AppDataWiped answers an owner's delete of all of an app's data.
type AppDataWiped struct {
	Deleted int64 `json:"deleted"`
}

// EventAppChanged is a transient room event (bnbloc-room): a document of
// one of the room's apps changed. It is never replayed or persisted and
// carries only AppChanged: an app's change feed (GET ~app/events at the
// gateway) re-fetches under its own read rights, so the feed leaks no
// content.
const EventAppChanged = "app_changed"

// AppChanged is the payload of EventAppChanged and the data of each event
// of an app's change feed. Version is the document's version after the
// change, 0 when it was deleted; a wipe of all of the app's data names no
// collection and no id.
type AppChanged struct {
	AppID      string `json:"appId"`
	Collection string `json:"collection,omitempty"`
	ID         string `json:"id,omitempty"`
	Version    int64  `json:"version"`
}

// AppVisitorToken answers POST /v1/public/shares/{token}/visitor: a signed
// visitor token bound to the share link and to one random visitor id (the
// participant a public link's app knows). The share page keeps it and hands
// it to the app frame (AppVisitorRequest / AppVisitorGrant); the SDK sends
// it in AppVisitorHeader.
type AppVisitorToken struct {
	Token       string `json:"token"`
	ExpiresAtMs int64  `json:"expiresAtMs"`
}

// AppVisitorRenew is the optional body of the visitor route: the token the
// share page holds. A valid token of the same link is renewed for the same
// visitor; anything else mints a new visitor.
type AppVisitorRenew struct {
	Token string `json:"token,omitempty"`
}

// AppVisitorHeader carries a public link's visitor token on an app data
// call: a header, never the URL, so the token stays out of access logs.
const AppVisitorHeader = "X-Bnbloc-Visitor"

// The visitor handshake between the app frame (the SDK, ~app/sdk.js) and
// the share page hosting it, over window.postMessage. The frame's origin
// is opaque, so neither side can name the other's origin; each checks the
// message's source window instead:
//
//	frame  -> parent: {type: AppVisitorRequest, v: AppHandshakeVersion}
//	parent -> frame:  {type: AppVisitorGrant, v: AppHandshakeVersion, token: AppVisitorToken.token}
//
// The parent answers only a request whose event.source is its own app
// frame's window, and may send a grant unasked (after it renewed the
// token). The SDK accepts a grant only from window.parent.
const (
	AppVisitorRequest   = "bnbloc.app.visitor.request"
	AppVisitorGrant     = "bnbloc.app.visitor"
	AppHandshakeVersion = 1
)

// The error codes of an app data call (httpx.ErrorBody code) the SDK acts
// on: it asks the share page for a visitor token and retries once.
//
//wire:union AppErrorCode
const (
	// AppVisitorRequired: a public link's app call carried no visitor
	// token (401).
	AppVisitorRequired = "visitor_token_required"
	// AppVisitorInvalid: the visitor token is forged, expired or another
	// link's (401).
	AppVisitorInvalid = "visitor_token_invalid"
)

// The formats of an owner's export of an app's data (GET
// /v1/rooms/{roomId}/apps/{artifactId}/export?collection=&format=): JSON
// is {"collections": {"<name>": [document, …]}} of every collection, or of
// the one named; CSV is one collection, its stamps first, then the other
// top-level fields in order of first appearance, nested values as their
// JSON text (internal/appdata export.go).
//
//wire:union AppExportFormat
const (
	AppExportJSON = "json"
	AppExportCSV  = "csv"
)

// AppAIRequest is an app's ai call (POST ~app/ai): the app's own Prompt
// (its instruction to the model) and Input (the data to work on, fenced
// as data); JSON asks for a JSON object instead of text.
type AppAIRequest struct {
	Prompt string `json:"prompt"`
	Input  string `json:"input,omitempty"`
	JSON   bool   `json:"json,omitempty"`
}

// AppAIReply is its answer: Text, or JSON for a json call.
type AppAIReply struct {
	Text string         `json:"text,omitempty"`
	JSON map[string]any `json:"json,omitempty"`
}

// The codes of an app's ai refusals (httpx.ErrorBody.Code).
const (
	// AppAISignInRequired: the caller did not sign in (an anonymous
	// visitor), or is an agent.
	AppAISignInRequired = "ai_sign_in_required"
	// AppAIUnavailable: no assist model, or the organization turned AI off.
	AppAIUnavailable = "ai_unavailable"
)
