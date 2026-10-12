package roomtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
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
}

// RawBody is a request body sent as-is (a package archive) instead of
// JSON-encoded.
type RawBody struct {
	ContentType string
	Data        []byte
}

// Content tool bounds.
const (
	// maxComponentFileText bounds one file's text get_component returns.
	maxComponentFileText = 64 << 10
	// maxComponentText bounds all of them.
	maxComponentText = 512 << 10
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
		Title: "Save an expert or skill pack",
		Description: "Save the package drafted in a directory of your workspace (for example drafts/contract-reviewer, holding component.json) " +
			"to the Library of the person who asked you. It is validated first; a validation error says what to fix. " +
			"Without id it creates a new component (refused if they already have one of that kind and name); with id (cc_...) " +
			"it saves a new version of that component of theirs (the version number is bumped for you). " +
			"Save only after the person confirmed.",
		InputSchema: objectSchema(map[string]any{
			"dir": map[string]any{"type": "string", "description": "Workspace-relative directory holding component.json, e.g. drafts/contract-reviewer."},
			"id":  map[string]any{"type": "string", "description": "The component to save a new version of (from get_component or list_components); omit to create."},
		}, "dir"),
		Call: saveComponent,
	}
}

func publishComponentTool() Tool {
	return Tool{
		Name:  "publish_component",
		Title: "Publish a saved component",
		Description: "Change who can use a component saved in the Library of the person who asked you, only after they confirm. " +
			"\"org\" asks their organization's admins to publish its latest version to everyone in the organization: it opens a request " +
			"the admins approve or decline (members keep the previously approved version until then). \"personal\" withdraws a pending " +
			"request and takes it out of the organization. \"platform\" (every organization) is a platform admin's action: say so instead.",
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
		Title: "Read an expert or skill pack",
		Description: "Read an expert or skill pack the person who asked you can use (their own, or one in the Library), at its latest version " +
			"or a given one: its manifest and the text of every file, to copy into drafts/<name>/ before improving it.",
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
		Name:        "list_components",
		Title:       "List experts and skill packs",
		Description: "The experts and skill packs the person who asked you has in their Library (their own are marked mine), to find one to improve.",
		InputSchema: objectSchema(map[string]any{
			"kind": map[string]any{"type": "string", "enum": []string{"expert", "skill"}, "description": "Only this kind."},
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

// cleanDir validates a workspace-relative directory.
func cleanDir(dir string) (string, error) {
	dir = strings.TrimSpace(strings.ReplaceAll(dir, "\\", "/"))
	dir = strings.TrimPrefix(strings.TrimSuffix(dir, "/"), "./")
	if dir == "" || strings.HasPrefix(dir, "/") {
		return "", fmt.Errorf("dir must be a directory relative to your workspace, e.g. drafts/my-expert")
	}
	clean := path.Clean(dir)
	if clean != dir || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("dir %q is not a plain workspace-relative directory", dir)
	}
	return clean, nil
}

// packDir reads every file under dir from the workspace and packs them
// (paths relative to dir) into a content bundle.
func packDir(ctx context.Context, ws Workspace, dir string) ([]byte, int, error) {
	files, err := ws.ListFiles(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("listing your workspace failed: %v", err)
	}
	limits := tarbundle.ContentLimits
	out := map[string][]byte{}
	total := int64(0)
	prefix := dir + "/"
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Path, prefix)
		if len(out) >= limits.MaxFiles {
			return nil, 0, fmt.Errorf("%s holds more than %d files", dir, limits.MaxFiles)
		}
		if total += f.Bytes; total > int64(limits.MaxBytes) {
			return nil, 0, fmt.Errorf("%s is larger than %d bytes", dir, limits.MaxBytes)
		}
		data, err := ws.ReadFile(ctx, f.Path)
		if err != nil {
			return nil, 0, fmt.Errorf("reading %s failed: %v", f.Path, err)
		}
		out[rel] = data
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("no files under %s in your workspace (write the draft there first)", dir)
	}
	if _, ok := out["component.json"]; !ok {
		return nil, 0, fmt.Errorf("%s has no component.json", dir)
	}
	raw, err := tarbundle.PackFiles(out, limits)
	if err != nil {
		return nil, 0, fmt.Errorf("packing %s failed: %v", dir, err)
	}
	return raw, len(out), nil
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
	} `json:"component"`
}

func saveComponent(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in struct {
		Dir string `json:"dir"`
		ID  string `json:"id"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	_, grant, refused := jobGrant(env, "save_component")
	if refused != nil {
		return *refused
	}
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
	p := "/v1/content-components?" + grant.Encode()
	if id := strings.TrimSpace(in.ID); id != "" {
		p = "/v1/content-components/" + url.PathEscape(id) + "/versions?" + grant.Encode()
	}
	raw, err := env.Gateway.Do(ctx, "POST", p, RawBody{ContentType: "application/x-tar", Data: bundle})
	if err != nil {
		return refusal("saving "+dir, err)
	}
	var d componentDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		return toolError("the save answered an unreadable result: %v", err)
	}
	c := d.Component
	out := map[string]any{
		"saved": true, "id": c.ID, "kind": c.Kind, "name": c.Name, "displayName": c.DisplayName,
		"version": c.LatestVersion, "scope": c.Scope, "files": n,
		"note": fmt.Sprintf("Saved %s version %s to the Library of the person who asked you (the version is assigned on save; report this one, not the number in your draft). They can pick it in any room's composer.", c.Name, c.LatestVersion),
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
	_, grant, refused := jobGrant(env, "publish_component")
	if refused != nil {
		return *refused
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return toolError("id is required")
	}
	switch in.Scope {
	case ScopePersonal, ScopeOrg:
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
			files = append(files, file{Path: f.Path, Text: f.Text})
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
	_, grant, refused := jobGrant(env, "list_components")
	if refused != nil {
		return *refused
	}
	if in.Kind != "" {
		grant.Set("kind", in.Kind)
	}
	raw, err := env.Gateway.Do(ctx, "GET", "/v1/content-components?"+grant.Encode(), nil)
	if err != nil {
		return refusal("listing the Library", err)
	}
	var list struct {
		Components []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			Name          string `json:"name"`
			DisplayName   string `json:"displayName"`
			Description   string `json:"description"`
			Scope         string `json:"scope"`
			LatestVersion string `json:"latestVersion"`
			Mine          bool   `json:"mine"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return toolError("unreadable Library listing: %v", err)
	}
	type item struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		Name        string `json:"name"`
		DisplayName string `json:"displayName,omitempty"`
		Description string `json:"description,omitempty"`
		Version     string `json:"version"`
		Mine        bool   `json:"mine"`
		Scope       string `json:"scope"`
	}
	items := make([]item, 0, len(list.Components))
	for _, c := range list.Components {
		items = append(items, item{ID: c.ID, Kind: c.Kind, Name: c.Name, DisplayName: c.DisplayName,
			Description: truncateBytes(c.Description, 300), Version: c.LatestVersion, Mine: c.Mine, Scope: c.Scope})
	}
	return Result{Value: map[string]any{"components": items}}
}
