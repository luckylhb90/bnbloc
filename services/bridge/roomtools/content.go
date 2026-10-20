package roomtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content tools (docs/prd/2026-10-12-experts-and-teams.md §5): an agent
// authoring an expert or a skill pack (the built-in Expert builder) saves
// the draft it wrote into its workspace, and reads the member's components
// to improve one. Every call goes to the ordinary content routes with a
// ?job=&room= grant: the gateway admits it only while the bound job runs on
// this bridge, and acts as the job's human requester (the save lands in
// their Library). The agent never names whose Library: it is always the
// person who asked.

// Workspace reads the driven session's workspace through the core's
// session-files seam (core.SessionFiles).
type Workspace interface {
	ListFiles(ctx context.Context) ([]core.SessionFile, error)
	ReadFile(ctx context.Context, rel string) ([]byte, error)
	// Deliverable is the room deliverable id of a workspace file, published
	// now when its content is not yet.
	Deliverable(ctx context.Context, rel string) (string, error)
	// CopyDeliverables copies the room's deliverables into the workspace
	// now (.room/deliverables/, as at a turn's start) and answers each
	// copy's directory by artifact id (nil: the core writes no files).
	CopyDeliverables(ctx context.Context) map[string]string
}

// RawBody is a request body sent as-is (a package archive, an app data
// write) instead of JSON-encoded, with the request headers it needs (an
// app write's If-Match / If-None-Match). An empty ContentType sends no
// body. Relay marks a room relay's call (relay.go): bound only to its
// context, never to the client's fixed request limit.
type RawBody struct {
	ContentType string
	Data        []byte
	Header      http.Header
	Relay       bool
}

// Content tool bounds.
const (
	// maxComponentFileText bounds one file's text get_component returns.
	maxComponentFileText = 64 << 10
	// maxComponentText bounds all of them.
	maxComponentText = 512 << 10
	// maxListedDescription bounds each description list_components shows.
	maxListedDescription = 300
)

// Publish scopes (publish_component).
const (
	ScopePersonal = "personal"
	ScopeOrg      = "org"
	ScopePlatform = "platform"
)

func saveComponentTool() Tool {
	return Tool{
		Name:  "save_component",
		Title: "Save a drafted component",
		Description: "Save to the Library of the person who asked you, after they confirmed: a package drafted in a workspace directory (dir), " +
			"a file as a gallery work (file or artifactId; its current version and recipe, never its data), " +
			"or this room as a room component (kind \"room\": what \"Save as component\" captures of it, its welcome, quick prompts, expert, packs, channels, " +
			"AI tasks and notices, apps from works and the sources it asks for (room_setup's ask_source), never its members or messages; " +
			"only the room's owner or admins save it). " +
			"Validated first; an error says what to fix. " +
			"Without id it creates (refused if they have one of that kind and name); with id (cc_...) it saves the next version. " +
			"Write a work's or a room's listing yourself: name, displayName, description, category (and a room's tags).",
		InputSchema: objectSchema(map[string]any{
			"dir":         map[string]any{"type": "string", "description": "Workspace-relative directory holding component.json, e.g. drafts/contract-reviewer."},
			"file":        map[string]any{"type": "string", "description": "A workspace file (published first if needed)."},
			"artifactId":  map[string]any{"type": "string", "description": "Or any deliverable's id (artf_...)."},
			"kind":        map[string]any{"type": "string", "enum": []string{"room"}, "description": "room: save this room as a room component."},
			"id":          map[string]any{"type": "string", "description": "The component to save a new version of; omit to create."},
			"name":        map[string]any{"type": "string", "description": "A new work's or room component's name (lower-case, hyphens)."},
			"category":    map[string]any{"type": "string", "description": "A work's: " + strings.Join(wire.WorkCategories, ", ") + "; a room's: one lower-case word."},
			"displayName": map[string]any{"type": "string", "description": "A work's or a room's title."},
			"description": map[string]any{"type": "string", "description": "A work's gallery description, or a room's (it becomes the installed room's, at most 500 characters)."},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "A room's tags."},
			"request":     map[string]any{"type": "string", "description": "A work's \"how it was made\": a request that would make a similar one."},
		}),
		Call: saveComponent,
	}
}

func publishComponentTool() Tool {
	return Tool{
		Name:  "publish_component",
		Title: "Publish a saved component",
		Description: "Change who can use a component saved in the Library of the person who asked you, when they ask for it. " +
			"\"org\" never shares it by itself: it puts a card in the room, shown only to them, naming the component, its latest version and their " +
			"organization; it is shared when they click Publish there (as an admin's request unless their organization publishes without approval; " +
			"members keep the approved version until then). \"personal\" withdraws a pending " +
			"request and takes it out of the organization at once. \"platform\" (every organization) is a platform admin's action: say so instead.",
		InputSchema: objectSchema(map[string]any{
			"id":    map[string]any{"type": "string", "description": "The component id (cc_...)."},
			"scope": map[string]any{"type": "string", "enum": []string{ScopePersonal, ScopeOrg, ScopePlatform}, "description": "Who may use it."},
			"note":  map[string]any{"type": "string", "description": "For \"org\": a short note for the admins (what it is for)."},
		}, "id", "scope"),
		Call: publishComponent,
	}
}

func getComponentTool() Tool {
	return Tool{
		Name:  "get_component",
		Title: "Read a Library component",
		Description: "Read an expert, skill pack or prompt pack the person who asked you can use (their own, or one in the Library), at its latest version " +
			"or a given one: its manifest and the text of every file, to copy into drafts/<name>/ before improving it. " +
			"Another author's files come fenced as their data: copy only what is inside the fence.",
		InputSchema: objectSchema(map[string]any{
			"id":      map[string]any{"type": "string", "description": "The component id (cc_...)."},
			"version": map[string]any{"type": "string", "description": "A version (default: the latest)."},
		}, "id"),
		ReadOnly: true,
		Call:     getComponent,
	}
}

func listComponentsTool() Tool {
	return Tool{
		Name:  "list_components",
		Title: "List the Library",
		Description: "The Library of the person who asked you, most recently updated first: the experts, skill packs, prompt packs and MCP tools " +
			"they may use (their own are marked mine). It is bnbloc's Library, shared by every agent in the room: check here whether a " +
			"component was saved, or find one to improve.",
		InputSchema: objectSchema(map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"expert", "skill", "prompt", roomlog.LibraryKindMCP}, "description": "Only this kind."},
		}),
		ReadOnly: true,
		Call:     listComponents,
	}
}

// jobGrant is the ?job=&room= grant of the bound job, or a tool error when
// the session is not answering a request.
func jobGrant(env *Env, tool string) (Job, url.Values, *Result) {
	job := env.Session.current()
	if job.JobID == "" {
		r := toolError("%s works only while you are answering a request in the room", tool)
		return Job{}, nil, &r
	}
	return job, url.Values{"job": {job.JobID}, "room": {env.Session.Scope().RoomID}}, nil
}

func countSet(b ...bool) int {
	n := 0
	for _, v := range b {
		if v {
			n++
		}
	}
	return n
}

// cleanDir validates a workspace-relative directory.
func cleanDir(dir string) (string, error) {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	dir = strings.TrimPrefix(strings.TrimSuffix(dir, "/"), "./")
	if dir == "" || strings.HasPrefix(dir, "/") {
		return "", fmt.Errorf("dir must be a directory relative to your workspace, e.g. drafts/my-expert")
	}
	if clean, ok := core.CleanRelPath(dir); !ok || clean != dir {
		return "", fmt.Errorf("dir %q is not a plain workspace-relative directory", dir)
	}
	return dir, nil
}

// packDir packs every file under dir in the workspace (paths relative to
// dir) into a content bundle through the one directory packer
// (tarbundle.PackEntries): junk, hidden files and protected workspace paths
// (core.ProtectedWorkspacePath: room history, the agent config the session
// wears, host-executed project config) are never packed, since a component
// is shared with other people.
func packDir(ctx context.Context, ws Workspace, dir string) ([]byte, int, error) {
	files, err := ws.ListFiles(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("listing your workspace failed: %v", err)
	}
	prefix := dir + "/"
	var entries []tarbundle.Entry
	packed, component := 0, false
	for _, f := range files {
		rel, ok := strings.CutPrefix(f.Path, prefix)
		if !ok {
			continue
		}
		e := tarbundle.Entry{Rel: rel, Path: f.Path, Size: f.Bytes, Read: func() ([]byte, error) { return ws.ReadFile(ctx, f.Path) }}
		if e.Packed() {
			packed++
			component = component || rel == "component.json"
		}
		entries = append(entries, e)
	}
	if packed == 0 {
		return nil, 0, fmt.Errorf("no files under %s in your workspace (write the draft there first)", dir)
	}
	if !component {
		return nil, 0, fmt.Errorf("%s has no component.json", dir)
	}
	raw, n, err := tarbundle.PackEntries(entries, tarbundle.ContentLimits)
	if err != nil {
		return nil, 0, fmt.Errorf("packing %s failed: %v", dir, err)
	}
	return raw, n, nil
}

// componentDetail is the part of the gateway's component detail the tools
// read back.
type componentDetail struct {
	Component struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		DisplayName   string `json:"displayName"`
		Description   string `json:"description"`
		Scope         string `json:"scope"`
		LatestVersion string `json:"latestVersion"`
		Version       string `json:"version"`
		Mine          bool   `json:"mine"`
		Owner         string `json:"owner"`
		SharedBy      string `json:"sharedBy"`
		// Recipe is a saved work's: how it was made.
		Recipe *wire.WorkRecipe `json:"recipe"`
	} `json:"component"`
	// OrgProposed is the version the save left with its author although
	// their organization uses the component: a job's save never reaches
	// the organization, so the tool offers them the publish card.
	OrgProposed string `json:"orgProposed"`
}

func saveComponent(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in saveArgs
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	job, grant, refused := jobGrant(env, "save_component")
	if refused != nil {
		return *refused
	}
	id := strings.TrimSpace(in.ID)
	var (
		p, what string
		body    any
		files   int
	)
	art, file := strings.TrimSpace(in.ArtifactID), strings.TrimSpace(in.File)
	if n := countSet(in.Dir != "", art != "", file != "", in.Kind != ""); n > 1 {
		return toolError("give one of dir (a drafted package), file or artifactId (saved as a work) and kind \"room\" (this room)")
	}
	if in.Kind == "" && len(in.Tags) > 0 {
		return toolError("tags are a room's: pass kind \"room\" to save this room")
	}
	if file != "" {
		// A workspace file: its deliverable, published now when it is not
		// yet (one written this turn is in no room record yet).
		ws := env.Session.workspace()
		if ws == nil {
			return toolError("save_component cannot read your workspace: this agent's core does not expose its files. Pass the deliverable's artifactId instead")
		}
		rel, err := cleanDir(file)
		if err != nil {
			return toolError("%v", err)
		}
		if art, err = ws.Deliverable(ctx, rel); err != nil {
			return toolError("%v", err)
		}
	}
	switch {
	case in.Kind == contentKindRoom:
		if in.Request != "" {
			return toolError("request is a work's: a room component carries no recipe")
		}
		var err error
		if p, body, err = roomSave(ctx, env, grant, id, in); err != nil {
			return refusal("capturing this room", err)
		}
		what = "this room"
	case in.Kind != "":
		return toolError("kind is \"room\" (this room); a drafted package's component.json names its own kind")
	case art != "":
		// A deliverable's current version as a work (the gateway snapshots
		// its files as the job's requester; its data never comes along).
		p = "/v1/rooms/" + url.PathEscape(env.Session.Scope().RoomID) + "/artifacts/" + url.PathEscape(art) + "/publish-work?" + url.Values{"job": {job.JobID}}.Encode()
		body = wire.WorkPublishRequest{
			ComponentID: id, Name: strings.TrimSpace(in.Name), Category: strings.TrimSpace(in.Category),
			DisplayName: strings.TrimSpace(in.DisplayName), Description: strings.TrimSpace(in.Description),
			RecipePrompt: strings.TrimSpace(in.Request),
		}
		what = "the deliverable " + art
	case in.Name != "" || in.Category != "" || in.DisplayName != "" || in.Description != "" || in.Request != "":
		return toolError("name, category, displayName, description and request are a work's (pass file or artifactId) or a room's (pass kind \"room\")")
	default:
		dir, err := cleanDir(in.Dir)
		if err != nil {
			return toolError("%v", err)
		}
		ws := env.Session.workspace()
		if ws == nil {
			return toolError("save_component cannot read your workspace: this agent's core does not expose its files. Ask the person to use \"Save as expert\" / \"Save as skill pack\" under your reply's files instead")
		}
		bundle, n, err := packDir(ctx, ws, dir)
		if err != nil {
			return toolError("%v", err)
		}
		p, body, what, files = "/v1/content-components?"+grant.Encode(), RawBody{ContentType: "application/x-tar", Data: bundle}, dir, n
		if id != "" {
			p = "/v1/content-components/" + url.PathEscape(id) + "/versions?" + grant.Encode()
		}
	}
	raw, err := env.Gateway.Do(ctx, "POST", p, body)
	if err != nil {
		return refusal("saving "+what, err)
	}
	var d componentDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return toolError("the save answered an unreadable result: %v", err)
	}
	c := d.Component
	out := map[string]any{
		"saved": true, "id": c.ID, "kind": c.Kind, "name": c.Name, "displayName": c.DisplayName,
		"version": c.LatestVersion, "scope": c.Scope,
		"note": fmt.Sprintf("Saved %s version %s to the Library of the person who asked you (the version is assigned on save; report this one, not the number in your draft). They can pick it in any room's composer.", c.Name, c.LatestVersion),
	}
	if files > 0 {
		out["files"] = files
	}
	if c.Kind == "assist" {
		out["note"] = fmt.Sprintf("Saved the assist %s version %s to the Library of the person who asked you. It runs on the deployment's assist model behind the sparkle button of its task (one named like a built-in assist replaces that one for them), not in an agent's composer; publish_component offers to share it with their organization (they publish it with a click).", c.Name, c.LatestVersion)
	}
	if c.Kind == "work" {
		out["note"] = fmt.Sprintf("Saved the work %s version %s to the Library of the person who asked you: a snapshot of the deliverable, without its data. publish_component offers to share it with their organization's gallery (they publish it with a click).", c.Name, c.LatestVersion)
		if c.Recipe != nil {
			out["recipe"] = recipeSummary(c.Recipe)
			out["note"] = out["note"].(string) + " It carries its recipe (the request, its expert, packs and reference files), which others start from with Make similar: say so. To change its title, description or \"how it was made\" request, save it again with id and displayName, description or request."
		}
	}
	if c.Kind == contentKindRoom {
		out["note"] = fmt.Sprintf("Saved this room as the room component %s version %s to the Library of the person who asked you: only they see it. "+
			"Anyone it is shared with installs it as a new room of theirs, without this room's members or messages. "+
			"publish_component offers to share it with their organization (they publish it with a click).", c.Name, c.LatestVersion)
	}
	if v := d.OrgProposed; v != "" {
		// Their organization uses it: the new version reaches it only by
		// their click on the card.
		if err := postPublishCard(ctx, env, job, publishCard{ID: c.ID, Kind: c.Kind, Name: c.Name, DisplayName: c.DisplayName, Version: v}, ""); err != nil {
			out["note"] = out["note"].(string) + fmt.Sprintf(" Their organization uses this component; version %s reaches it only when they publish it from their Library (the card for it could not be put in the room). Tell them.", v)
		} else {
			out["proposed"] = ScopeOrg
			out["note"] = out["note"].(string) + fmt.Sprintf(" Their organization uses this component; version %s reaches it only when they click Publish on the card in the room, shown only to them. Tell them.", v)
		}
	}
	return Result{Value: out}
}

func publishComponent(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
		Note  string `json:"note"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	job, grant, refused := jobGrant(env, "publish_component")
	if refused != nil {
		return *refused
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return toolError("id is required")
	}
	switch in.Scope {
	case ScopeOrg:
		return proposePublish(ctx, env, job, grant, id, strings.TrimSpace(in.Note))
	case ScopePersonal:
	case ScopePlatform:
		return toolError("publishing to every organization is a platform admin's action, not one you or the person who asked can take: " +
			"tell them a platform admin promotes it (exports it as a signed package and installs it). To share it with their organization, " +
			"publish it with scope \"org\": an organization admin approves it")
	default:
		return toolError("scope must be %q, %q or %q", ScopePersonal, ScopeOrg, ScopePlatform)
	}
	raw, err := env.Gateway.Do(ctx, "POST", "/v1/content-components/"+url.PathEscape(id)+"/publish?"+grant.Encode(),
		map[string]any{"scope": in.Scope, "note": strings.TrimSpace(in.Note)})
	if err != nil {
		return refusal("publishing "+id, err)
	}
	var reply struct {
		Scope   string `json:"scope"`
		Note    string `json:"note"`
		Request *struct {
			ID              string `json:"id"`
			Version         string `json:"version"`
			Status          string `json:"status"`
			ApprovedVersion string `json:"approvedVersion"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return toolError("the publish answered an unreadable result: %v", err)
	}
	out := map[string]any{"id": id, "scope": reply.Scope, "note": reply.Note}
	if reply.Request != nil {
		out["request"] = map[string]any{"id": reply.Request.ID, "version": reply.Request.Version, "status": reply.Request.Status,
			"approvedVersion": reply.Request.ApprovedVersion}
	}
	return Result{Value: out}
}

// proposePublish offers the requester to share their component id with
// their organization: a card in the room (a notice row carrying a
// wire.PublishProposal) that only they are shown, whose Publish button
// calls the publish route as them. Sharing beyond its author is their own
// click, never the tool's (the gateway refuses a job's publish to the
// organization), so text an agent read cannot share their work.
func proposePublish(ctx context.Context, env *Env, job Job, grant url.Values, id, note string) Result {
	raw, err := env.Gateway.Do(ctx, http.MethodGet, "/v1/content-components/"+url.PathEscape(id)+"?"+grant.Encode(), nil)
	if err != nil {
		return refusal("reading "+id, err)
	}
	var d componentDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return toolError("unreadable component: %v", err)
	}
	c := d.Component
	if !c.Mine || c.SharedBy != "" {
		return toolError("%s is not a component of the person who asked you: only its author shares it", c.Name)
	}
	if err := postPublishCard(ctx, env, job, publishCard{ID: c.ID, Kind: c.Kind, Name: c.Name, DisplayName: c.DisplayName, Version: c.LatestVersion}, note); err != nil {
		return refusal("putting the publish card in the room", err)
	}
	return Result{Value: map[string]any{"id": c.ID, "proposed": ScopeOrg, "version": c.LatestVersion,
		"note": fmt.Sprintf("Nothing is shared yet: a card in the room, shown only to %s, asks them to publish %s version %s to their organization. "+
			"It is shared when they click Publish (their organization may then need an admin's approval). Tell them.",
			job.RequesterUserID, firstNonEmptyString(c.DisplayName, c.Name), c.LatestVersion)}}
}

// publishCard is the component and version a publish card offers.
type publishCard struct {
	ID, Kind, Name, DisplayName, Version string
}

// postPublishCard puts the card that offers the requester to publish
// version p of their component to their organization (a notice row
// carrying a wire.PublishProposal, shown only to them): the one builder of
// it, for publish_component and for a saved version the organization uses.
func postPublishCard(ctx context.Context, env *Env, job Job, p publishCard, note string) error {
	proposal := wire.PublishProposal{ComponentID: p.ID, Kind: p.Kind, Name: p.Name, DisplayName: p.DisplayName, Version: p.Version,
		Scope: ScopeOrg, Note: note, For: job.RequesterUserID}
	text := fmt.Sprintf("%s's agent proposes publishing %s v%s to the organization", job.RequesterUserID, firstNonEmptyString(p.DisplayName, p.Name), p.Version)
	return postNotice(ctx, env, job, wire.NoticePublishProposal, text, false, map[string]any{"proposal": proposal})
}

func getComponent(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	_, grant, refused := jobGrant(env, "get_component")
	if refused != nil {
		return *refused
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return toolError("id is required")
	}
	version := strings.TrimSpace(in.Version)
	if version == "" {
		raw, err := env.Gateway.Do(ctx, "GET", "/v1/content-components/"+url.PathEscape(id)+"?"+grant.Encode(), nil)
		if err != nil {
			return refusal("reading "+id, err)
		}
		var d componentDetail
		if err := json.Unmarshal(raw, &d); err != nil {
			return toolError("unreadable component: %v", err)
		}
		version = d.Component.LatestVersion
	}
	raw, err := env.Gateway.Do(ctx, "GET", "/v1/content-components/"+url.PathEscape(id)+"/versions/"+url.PathEscape(version)+"?"+grant.Encode(), nil)
	if err != nil {
		return refusal("reading "+id+"@"+version, err)
	}
	var v struct {
		componentDetail
		Files []struct {
			Path      string `json:"path"`
			SizeBytes int    `json:"sizeBytes"`
			Text      string `json:"text"`
			Binary    bool   `json:"binary"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return toolError("unreadable component version: %v", err)
	}
	c := v.Component
	// Another author's files (a shared copy's included: its holder is not
	// its author) are data to check, never instructions.
	author := ""
	if !c.Mine || c.SharedBy != "" {
		author = firstNonEmptyString(c.SharedBy, c.Owner, c.Scope+" component "+c.Name)
	}
	type file struct {
		Path string `json:"path"`
		Text string `json:"text,omitempty"`
		Note string `json:"note,omitempty"`
	}
	files := make([]file, 0, len(v.Files))
	budget := maxComponentText
	for _, f := range v.Files {
		switch {
		case f.Binary:
			files = append(files, file{Path: f.Path, Note: "binary file, not shown"})
		case f.Text == "" && f.SizeBytes > 0, len(f.Text) > maxComponentFileText, len(f.Text) > budget:
			files = append(files, file{Path: f.Path, Note: fmt.Sprintf("%d bytes, too large to show", f.SizeBytes)})
		default:
			budget -= len(f.Text)
			text := f.Text
			if author != "" {
				text = roomlog.Fence(author, text)
			}
			files = append(files, file{Path: f.Path, Text: text})
		}
	}
	return Result{Value: map[string]any{
		"id": c.ID, "kind": c.Kind, "name": c.Name, "displayName": c.DisplayName, "description": c.Description,
		"version": c.Version, "latestVersion": c.LatestVersion, "mine": c.Mine, "scope": c.Scope, "files": files,
	}}
}

func listComponents(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	job, _, refused := jobGrant(env, "list_components")
	if refused != nil {
		return *refused
	}
	items, err := roomlog.ReadLibrary(ctx, gatewayFetch(env), env.Session.Scope().RoomID, job.JobID, in.Kind)
	if err != nil {
		return refusal("listing the Library", err)
	}
	for i := range items {
		items[i].Description = roomlog.Truncate(items[i].Description, maxListedDescription)
	}
	return Result{Value: map[string]any{"components": items}}
}

// recipeSummary is what save_component reports of a saved work's recipe:
// whether it keeps the request's text, the expert, the packs and the
// reference files by name, so the agent can tell the person what it
// carries.
func recipeSummary(r *wire.WorkRecipe) map[string]any {
	out := map[string]any{"request": r.Prompt != ""}
	if r.Expert != nil {
		out["expert"] = r.Expert.Name
	}
	names := func(parts []wire.WorkRecipePart) []string {
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, p.Name)
		}
		return out
	}
	if len(r.Skills) > 0 {
		out["skills"] = names(r.Skills)
	}
	if len(r.Prompts) > 0 {
		out["prompts"] = names(r.Prompts)
	}
	if len(r.References) > 0 {
		refs := make([]string, 0, len(r.References))
		for _, ref := range r.References {
			refs = append(refs, ref.Name)
		}
		out["references"] = refs
	}
	return out
}
