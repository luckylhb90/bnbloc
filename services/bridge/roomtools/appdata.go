package roomtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/appdata"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// app_data (docs/prd/2026-10-12-interactive-apps.md §4.6, delivery step
// 4): an agent reads and changes the data of one of the room's interactive
// apps (a site deliverable; its data is collections of JSON documents the
// people using it wrote through the app SDK), to report on a poll, seed an
// app's options or clear test data. Every call goes to the gateway's agent
// entry of the app data API (roomlog.AppPath: /v1/rooms/{room}/apps/
// {artifactId}/…?job=), which admits it only while the bound job runs on
// this bridge and makes the job's agent the participant: one of the app's
// owners, who reads every document (with its real _author) and may change
// any. The room service applies the platform's rules; the tool adds none.
// Every document it hands back is fenced as data the app's users wrote
// (roomlog.Fence), bounded by maxAppDataText, with a cursor to page on.

// The app_data actions.
const (
	appActList   = "list"
	appActFind   = "find"
	appActGet    = "get"
	appActInsert = "insert"
	appActPut    = "put"
	appActPatch  = "patch"
	appActDelete = "delete"
	appActWipe   = "wipe"
)

// app_data bounds.
const (
	// defaultAppFindLimit is a find's page size unless the agent asks.
	defaultAppFindLimit = 50
	// maxAppDataText bounds the document text one call returns: a find
	// stops at the document that would pass it (next continues there); a
	// single document over it is cut.
	maxAppDataText = 96 << 10
)

// appDataAuthor names who wrote the documents inside their fence.
const appDataAuthor = "the app's users (each document's _author names its writer)"

func appDataTool() Tool {
	return Tool{
		Name:  "app_data",
		Title: "Read and change an app's data",
		Description: "Read and change the data of this room's interactive apps (site deliverables whose pages store JSON documents in collections). " +
			"You are an owner: you see every document, with _author, and may change any. Actions: list (the apps; with artifactId, its collections); " +
			"find (filter, sort, limit (default " + strconv.Itoa(defaultAppFindLimit) + "), after=next); get; insert; put (ifAbsent, ifVersion); " +
			"patch (doc merged, inc added); delete; wipe (all documents, for good: only when the person asked, with confirm). " +
			"public: readable by all its users. Documents come fenced as the users' data.",
		InputSchema: objectSchema(map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{appActList, appActFind, appActGet, appActInsert, appActPut, appActPatch, appActDelete, appActWipe}},
			"artifactId": map[string]any{"type": "string"},
			"collection": map[string]any{"type": "string"},
			"id":         map[string]any{"type": "string"},
			"filter":     map[string]any{"type": "object", "description": "Field (dotted ok) to a value or {" + strings.Join(appdata.FilterOps, " ") + "}."},
			"sort":       map[string]any{"type": "string", "enum": []string{appdata.StampID, "-" + appdata.StampID}},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": wire.MaxAppFindLimit},
			"after":      map[string]any{"type": "string"},
			"doc":        map[string]any{"type": "object"},
			"inc":        map[string]any{"type": "object"},
			"public":     map[string]any{"type": "boolean"},
			"ifAbsent":   map[string]any{"type": "boolean"},
			"ifVersion":  map[string]any{"type": "integer", "minimum": 1},
			"confirm":    map[string]any{"type": "boolean"},
		}, "action"),
		Call: appData,
	}
}

// appDataArgs are app_data's arguments.
type appDataArgs struct {
	Action     string          `json:"action"`
	ArtifactID string          `json:"artifactId"`
	Collection string          `json:"collection"`
	ID         string          `json:"id"`
	Filter     json.RawMessage `json:"filter"`
	Sort       string          `json:"sort"`
	Limit      int             `json:"limit"`
	After      string          `json:"after"`
	Doc        json.RawMessage `json:"doc"`
	Inc        json.RawMessage `json:"inc"`
	Public     *bool           `json:"public"`
	IfAbsent   bool            `json:"ifAbsent"`
	IfVersion  int64           `json:"ifVersion"`
	Confirm    bool            `json:"confirm"`
}

// appCall is one app_data call's scope: the job's grant in the session's
// room, the app, and what the call names.
type appCall struct {
	ctx  context.Context
	env  *Env
	room string
	job  string
	in   appDataArgs
}

// path is the app data API path rest (escaped segments) with q.
func (c appCall) path(rest string, q url.Values) string {
	return roomlog.AppPath(c.room, c.in.ArtifactID, rest, c.job, q)
}

// docPath is c/{collection}[/{id}].
func (c appCall) docPath(withID bool) string {
	p := "c/" + url.PathEscape(c.in.Collection)
	if withID {
		p += "/" + url.PathEscape(c.in.ID)
	}
	return p
}

func appData(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in appDataArgs
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	job, _, bad := jobGrant(env, "app_data")
	if bad != nil {
		return *bad
	}
	in.ArtifactID, in.Collection, in.ID = strings.TrimSpace(in.ArtifactID), strings.TrimSpace(in.Collection), strings.TrimSpace(in.ID)
	c := appCall{ctx: ctx, env: env, room: env.Session.Scope().RoomID, job: job.JobID, in: in}
	if in.Action == appActList && in.ArtifactID == "" {
		return c.listApps()
	}
	if in.ArtifactID == "" || strings.ContainsAny(in.ArtifactID, "/?#") {
		return toolError("artifactId must be the app's deliverable id (from list, ask_agent or room_status)")
	}
	needs := map[string]struct{ collection, id bool }{
		appActFind: {true, false}, appActGet: {true, true}, appActInsert: {true, false}, appActPut: {true, true},
		appActPatch: {true, true}, appActDelete: {true, true},
	}
	if need, ok := needs[in.Action]; ok {
		if need.collection && in.Collection == "" {
			return toolError("%s needs a collection", in.Action)
		}
		if need.id && in.ID == "" {
			return toolError("%s needs a document id", in.Action)
		}
	}
	switch in.Action {
	case appActList:
		return c.stats()
	case appActFind:
		return c.find()
	case appActGet:
		return c.document(http.MethodGet, c.docPath(true), nil, nil)
	case appActInsert:
		return c.write(http.MethodPost, false, wire.AppDocWrite{Doc: in.Doc, Public: in.Public})
	case appActPut:
		return c.write(http.MethodPut, true, wire.AppDocWrite{Doc: in.Doc, Public: in.Public})
	case appActPatch:
		if len(in.Doc) == 0 && len(in.Inc) == 0 && in.Public == nil {
			return toolError("patch needs doc (fields to merge), inc (numbers to add) or public")
		}
		return c.document(http.MethodPatch, c.docPath(true), wire.AppDocPatch{Merge: in.Doc, Inc: in.Inc, Public: in.Public}, c.conditions(false))
	case appActDelete:
		if _, err := c.env.Gateway.Do(ctx, http.MethodDelete, c.path(c.docPath(true), nil), RawBody{Header: c.conditions(false)}); err != nil {
			return refusal("app_data delete", err)
		}
		return Result{Value: map[string]any{"artifactId": in.ArtifactID, "collection": in.Collection, "deleted": in.ID}}
	case appActWipe:
		return c.wipe()
	}
	return toolError("action must be one of list, find, get, insert, put, patch, delete, wipe")
}

// conditions are a write's request headers: ifVersion (If-Match), and on
// a put ifAbsent (If-None-Match: *).
func (c appCall) conditions(put bool) http.Header {
	h := http.Header{}
	if c.in.IfVersion > 0 {
		h.Set("If-Match", strconv.FormatInt(c.in.IfVersion, 10))
	}
	if put && c.in.IfAbsent {
		h.Set("If-None-Match", "*")
	}
	return h
}

// listApps is list without an app: the room's apps (roomlog.ReadApps, as
// .room/apps.md lists them).
func (c appCall) listApps() Result {
	apps, err := roomlog.ReadApps(c.ctx, gatewayFetch(c.env), c.room, c.job, nil)
	if err != nil && len(apps) == 0 {
		return refusal("app_data list", err)
	}
	out := make([]map[string]any, 0, len(apps))
	for _, a := range apps {
		out = append(out, map[string]any{"artifactId": a.ArtifactID, "title": a.Title, "version": a.Version, "producedBy": a.Producer.String(),
			"documents": a.Documents, "collections": a.Collections})
	}
	res := map[string]any{"apps": out}
	if err != nil {
		res["note"] = "some apps' data could not be read; they are not listed"
	}
	if len(out) == 0 {
		res["note"] = "this room has no interactive app yet (a site that loads the app SDK, or holds data); any site's data is read by its artifactId"
	}
	return Result{Value: res}
}

// stats is list with an app: its collections and their document counts.
func (c appCall) stats() Result {
	st, err := roomlog.ReadAppStats(c.ctx, gatewayFetch(c.env), c.room, c.in.ArtifactID, c.job)
	if err != nil {
		return refusal("app_data list", err)
	}
	if st.Collections == nil {
		st.Collections = map[string]int64{}
	}
	return Result{Value: map[string]any{"artifactId": c.in.ArtifactID, "documents": st.Documents, "bytes": st.Bytes, "collections": st.Collections}}
}

// find reads one page of a collection, cut at maxAppDataText.
func (c appCall) find() Result {
	q := url.Values{}
	if len(c.in.Filter) > 0 && string(c.in.Filter) != "null" {
		q.Set("filter", string(c.in.Filter))
	}
	if c.in.Sort != "" {
		q.Set("sort", c.in.Sort)
	}
	limit := c.in.Limit
	if limit <= 0 {
		limit = defaultAppFindLimit
	}
	q.Set("limit", strconv.Itoa(min(limit, wire.MaxAppFindLimit)))
	if c.in.After != "" {
		q.Set("after", c.in.After)
	}
	raw, err := c.env.Gateway.Do(c.ctx, http.MethodGet, c.path(c.docPath(false), q), nil)
	if err != nil {
		return refusal("app_data find", err)
	}
	var page wire.AppDocPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return toolError("unreadable app data page: %v", err)
	}
	var (
		text      strings.Builder
		shown     int
		truncated bool
	)
	for _, d := range page.Docs {
		line := docJSON(d)
		if shown > 0 && text.Len()+len(line)+1 > maxAppDataText {
			// The rest is the next page: continue after the last one shown.
			page.Next, _ = page.Docs[shown-1]["_id"].(string)
			break
		}
		if len(line) > maxAppDataText {
			line, truncated = roomlog.Truncate(line, maxAppDataText), true
		}
		text.WriteString(line + "\n")
		shown++
	}
	out := map[string]any{"artifactId": c.in.ArtifactID, "collection": c.in.Collection, "count": shown}
	if page.Next != "" {
		out["next"] = page.Next
	}
	if truncated {
		out["truncated"] = true
	}
	if shown > 0 {
		out["documents"] = roomlog.Fence(appDataAuthor, text.String())
	}
	return Result{Value: out}
}

// write inserts (POST c/{collection}) or puts (PUT c/{collection}/{id}) a
// document.
func (c appCall) write(method string, put bool, body wire.AppDocWrite) Result {
	if len(body.Doc) == 0 || string(body.Doc) == "null" {
		return toolError("%s needs doc, a JSON object", c.in.Action)
	}
	return c.document(method, c.docPath(put), body, c.conditions(put))
}

// document makes one call answered by a document (body, a wire write
// shape, sent as JSON with header), fenced for the agent.
func (c appCall) document(method, rest string, body any, header http.Header) Result {
	var payload any
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return toolError("invalid arguments: %v", err)
		}
		payload = RawBody{ContentType: "application/json", Data: data, Header: header}
	}
	raw, err := c.env.Gateway.Do(c.ctx, method, c.path(rest, nil), payload)
	if err != nil {
		return refusal("app_data "+c.in.Action, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return toolError("unreadable app document: %v", err)
	}
	line := docJSON(doc)
	out := map[string]any{"artifactId": c.in.ArtifactID, "collection": c.in.Collection}
	for _, stamp := range []string{"_id", "_version"} {
		if v, ok := doc[stamp]; ok {
			out[strings.TrimPrefix(stamp, "_")] = v
		}
	}
	if len(line) > maxAppDataText {
		line, out["truncated"] = roomlog.Truncate(line, maxAppDataText), true
	}
	out["document"] = roomlog.Fence(appDataAuthor, line)
	return Result{Value: out}
}

// wipe deletes every document of the app, only with confirm.
func (c appCall) wipe() Result {
	if !c.in.Confirm {
		return toolError("wipe deletes every document of the app for good: ask the person first, then call it again with confirm: true")
	}
	raw, err := c.env.Gateway.Do(c.ctx, http.MethodDelete, c.path("data", nil), nil)
	if err != nil {
		return refusal("app_data wipe", err)
	}
	var wiped wire.AppDataWiped
	if err := json.Unmarshal(raw, &wiped); err != nil {
		return toolError("unreadable wipe result: %v", err)
	}
	return Result{Value: map[string]any{"artifactId": c.in.ArtifactID, "deleted": wiped.Deleted}}
}

// docJSON is a document as one line of JSON (keys sorted, not
// HTML-escaped: an agent reads it).
func docJSON(doc map[string]any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return fmt.Sprintf("(unrenderable document: %v)", err)
	}
	return strings.TrimRight(b.String(), "\n")
}
