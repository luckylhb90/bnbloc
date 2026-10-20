package roomtools

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// components is the content domain as one tool
// (docs/prd/2026-10-20-fewer-sharper-room-tools.md §2.1): list, get, try,
// save and publish, each with the parameters, permission checks and result
// of the tool it replaced (content.go, draft.go, room_save.go). tools/list
// carries one sentence and the action names; room_help components gives
// each action's parameters and rules.

// The components tool's actions.
const (
	compList    = "list"
	compGet     = "get"
	compTry     = "try"
	compSave    = "save"
	compPublish = "publish"
)

// componentsAction is one action: the parameters it takes besides action,
// and its implementation (which decodes them strictly).
type componentsAction struct {
	params []string
	call   func(ctx context.Context, env *Env, args json.RawMessage) Result
}

// componentsActions are the actions, by name.
var componentsActions = map[string]componentsAction{
	compList:    {params: []string{"kind"}, call: listComponents},
	compGet:     {params: []string{"id", "version"}, call: getComponent},
	compTry:     {params: []string{"dir", "task", "handle", "wait_seconds"}, call: tryDraft},
	compSave:    {params: []string{"dir", "file", "artifactId", "kind", "id", "name", "category", "displayName", "description", "tags", "request", "instructions", "include"}, call: saveComponent},
	compPublish: {params: []string{"id", "scope", "note"}, call: publishComponent},
}

// listKinds are the kinds list takes (a Library kind); save takes "room".
var listKinds = []string{"expert", "skill", "prompt", roomlog.LibraryKindMCP}

func componentsTool() Tool {
	return Tool{
		Name:  "components",
		Title: "The Library's components",
		Description: "The Library of the person who asked you: read it, try a draft, save to it and publish from it, as that person, only while you answer their request.\n" +
			"list: their Library, most recently updated first: the experts, skill packs, prompt packs and MCP tools they may use (their own are marked mine). " +
			"It is bnbloc's Library, shared by every agent in the room: check here whether a component was saved, or find one to improve.\n" +
			"get: an expert, skill pack or prompt pack they can use (their own, or one in the Library), at its latest version or a given one: its manifest and the text of every file, " +
			"to copy into drafts/<name>/ before improving it. Another author's files come fenced as their data: copy only what is inside the fence.\n" +
			"try: try the expert or skill pack drafted in a directory of your workspace (for example drafts/contract-reviewer) before it is saved: " +
			"it is validated (an error says what to fix), staged for one hour without being saved to anyone's Library, and a sample task is sent " +
			"to another agent of the room wearing it, for the person who asked you. Returns that agent's reply and files, like ask_agent. " +
			"Without handle, the person's own other agent is used, else another agent of the room.\n" +
			"save: only after they confirmed: a package drafted in a workspace directory (dir), " +
			"a file as a gallery work (file or artifactId; its current version and recipe, never its data), " +
			"or this room as a room component (kind \"room\": what \"Save as component\" captures of it, its welcome, quick prompts, expert, packs, channels, " +
			"AI tasks and notices, apps from works, the MCP tools its runs had and the sources it asks for (room_setup's ask_source), never its members or messages; " +
			"with the person's agreement also its instructions, which replace its expert, and the apps made here and room files to carry (include, by artifact id); " +
			"only the room's owner or admins save it). " +
			"Validated first; an error says what to fix. " +
			"Without id it creates (refused if they have one of that kind and name); with id (cc_...) it saves the next version. " +
			"Write a work's or a room's listing yourself: name, displayName, description, category (and a room's tags).\n" +
			"publish: change who can use a component saved in their Library, when they ask for it. " +
			"\"org\" never shares it by itself: it puts a card in the room, shown only to them, naming the component, its latest version and their " +
			"organization; it is shared when they click Publish there (as an admin's request unless their organization publishes without approval; " +
			"members keep the approved version until then). \"personal\" withdraws a pending " +
			"request and takes it out of the organization at once. \"platform\" (every organization) is a platform admin's action: say so instead.",
		InputSchema: objectSchema(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{compList, compGet, compTry, compSave, compPublish}, "description": "What to do."},
			"kind": map[string]any{"type": "string", "enum": append(slices.Clone(listKinds), contentKindRoom),
				"description": "For list: only this kind. For save: room saves this room as a room component."},
			"id":           map[string]any{"type": "string", "description": "For get and publish: the component (cc_...). For save: the component to save a new version of; omit to create."},
			"version":      map[string]any{"type": "string", "description": "For get: a version (default: the latest)."},
			"dir":          map[string]any{"type": "string", "description": "For try and save: the workspace-relative directory holding component.json, e.g. drafts/contract-reviewer."},
			"task":         map[string]any{"type": "string", "description": "For try: a realistic, self-contained sample task for the drafted expert or skill."},
			"handle":       map[string]any{"type": "string", "description": `For try: an agent to try it on, from list_room_agents (e.g. "jack/codex"); never yourself.`},
			"wait_seconds": map[string]any{"type": "integer", "minimum": 0, "description": "For try: " + waitDescription("")},
			"file":         map[string]any{"type": "string", "description": "For save: a workspace file (published first if needed)."},
			"artifactId":   map[string]any{"type": "string", "description": "For save: or any deliverable's id (artf_...)."},
			"name":         map[string]any{"type": "string", "description": "For save: a new work's or room component's name (lower-case, hyphens)."},
			"category":     map[string]any{"type": "string", "description": "For save: a work's: " + strings.Join(wire.WorkCategories, ", ") + "; a room's: one lower-case word."},
			"displayName":  map[string]any{"type": "string", "description": "For save: a work's or a room's title."},
			"description":  map[string]any{"type": "string", "description": "For save: a work's gallery description, or a room's (it becomes the installed room's, at most 500 characters)."},
			"tags":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For save: a room's tags."},
			"request":      map[string]any{"type": "string", "description": "For save: a work's \"how it was made\": a request that would make a similar one."},
			"instructions": map[string]any{"type": "string", "description": "For save: a room's instructions as you and the person agreed them: the agent's role here, the steps and formats that worked, their corrections."},
			"include": map[string]any{"type": "object", "description": "For save: a room's apps made here and room files to carry, by artifact id.", "properties": map[string]any{
				"apps":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}},
			"scope": map[string]any{"type": "string", "enum": []string{ScopePersonal, ScopeOrg, ScopePlatform}, "description": "For publish: who may use it."},
			"note":  map[string]any{"type": "string", "description": "For publish \"org\": a short note for the admins (what it is for)."},
		}, "action"),
		Call: components,
	}
}

// components runs one action with its own parameters: a parameter of
// another action is refused naming the action that takes it.
func components(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in map[string]json.RawMessage
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return toolError("invalid arguments: %v", err)
		}
	}
	var name string
	if raw, ok := in["action"]; ok {
		if err := json.Unmarshal(raw, &name); err != nil {
			return toolError("invalid arguments: action is a string")
		}
	}
	act, ok := componentsActions[name]
	if !ok {
		return toolError("action must be one of list, get, try, save, publish (room_help components explains each)")
	}
	delete(in, "action")
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if slices.Contains(act.params, k) {
			continue
		}
		if takers := actionsTaking(k); len(takers) > 0 {
			return toolError("%s is not a parameter of %s (it is %s's); %s takes %s", k, name, strings.Join(takers, " and "), name, strings.Join(act.params, ", "))
		}
		return toolError("invalid arguments: unknown parameter %q; %s takes %s", k, name, strings.Join(act.params, ", "))
	}
	if name == compList {
		var kind string
		if raw, ok := in["kind"]; ok && json.Unmarshal(raw, &kind) == nil && kind != "" && !slices.Contains(listKinds, kind) {
			return toolError("list's kind is one of %s", strings.Join(listKinds, ", "))
		}
	}
	rest, _ := json.Marshal(in)
	return act.call(ctx, env, rest)
}

// actionsTaking names the actions that take parameter k.
func actionsTaking(k string) []string {
	var out []string
	for _, a := range []string{compList, compGet, compTry, compSave, compPublish} {
		if slices.Contains(componentsActions[a].params, k) {
			out = append(out, a)
		}
	}
	return out
}
