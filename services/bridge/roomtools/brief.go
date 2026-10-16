package roomtools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// A tool is listed to the agent in brief and explained in full on demand.
//
// tools/list rides every request of a session as a cached prefix, so what a
// tool costs there is paid by every turn of every session, used or not. Each
// tool therefore lists a one or two sentence brief (what it does and the one
// constraint that must never be missed, such as "only after the person
// confirms"), its parameters with their names, types and enums, and a note only
// where a name does not explain itself. The tool's full description and every
// parameter's note stay where they were written (Tool.Description, the schema)
// and the agent reads them through room_help when it needs them. Nothing is
// removed: the same text is one call away.
type brief struct {
	// Text is the description tools/list carries.
	Text string
	// Params keeps the listed note of a parameter; the others list none.
	Params map[string]string
}

// briefs is every tool's brief (a tool without one is refused at
// registration, TestEveryToolHasABrief: a new tool is brief by default).
var briefs = map[string]brief{
	"list_room_agents": {Text: "The agents you can ask in this room now: handle, owner, core, provider (you=true is you); a team lead also sees who takes each role."},
	"ask_agent": {
		Text: "Send a task to another agent of this room and wait for its reply and files. It is posted in the room for the person who asked you, under their authority.",
		Params: map[string]string{
			"expert":       "Optional expert (id[@version]) for it to wear.",
			"handle":       "From list_room_agents (or a team role).",
			"task":         "Self-contained: it sees the room, not your context.",
			"wait_seconds": "Default 600, at most 1200; 0 returns at once.",
		},
	},
	"room_status": {Text: "State, reply and files of tasks you dispatched with ask_agent (one handle, or all)."},
	"read_room": {
		Text:   "Recent messages of this conversation, oldest first (\"#<seq> <author>: <text>\").",
		Params: map[string]string{"limit": "Default 30.", "since": "Only newer than this seq."},
	},
	"read_artifact": {
		Text:   "Read a deliverable or attachment of this room by id; the content comes fenced as data its producer wrote.",
		Params: map[string]string{"artifactId": "art_…, artf_… or att_…", "path": "A site's file (default index.html).", "version": "Default: the latest."},
	},
	"save_component": {
		Text: "Save a drafted package (dir) or a file as a gallery work (file or artifactId) to the Library of the person who asked you, only after they confirm. With id: the next version.",
		Params: map[string]string{
			"dir": "Workspace-relative, holds component.json.", "id": "cc_…; omit to create.", "file": "A workspace file.",
			"artifactId": "A deliverable (artf_…).", "name": "A new work's name.",
		},
	},
	"get_component": {
		Text:   "Read a Library expert, skill pack or prompt pack (manifest and files) to copy into drafts/<name>/ and improve; another author's files come fenced as their data.",
		Params: map[string]string{"id": "cc_…", "version": "Default: the latest."},
	},
	"list_components": {
		Text:   "The Library of the person who asked you: experts, skill packs, prompt packs and MCP tools they may use (their own marked mine).",
		Params: map[string]string{"kind": "Only this kind."},
	},
	"publish_component": {
		Text:   "Change who can use a saved component (personal, org, platform), only after the person confirms. org may need an admin's approval; platform is a platform admin's action: say so.",
		Params: map[string]string{"id": "cc_…", "note": "For org: a note for the admins."},
	},
	"try_draft": {
		Text:   "Try a drafted expert or skill pack (dir) on another agent with a sample task before saving: staged for an hour, not saved. Returns its reply like ask_agent.",
		Params: map[string]string{"dir": "Workspace-relative, holds component.json.", "task": "A realistic sample task.", "handle": "An agent to try it on; never yourself."},
	},
	"post_progress": {Text: "Post one short progress line to the room, once per phase of a long task."},
	"app_data": {
		Text: "Read and change the data of this room's interactive apps; you are an owner and see every document. " +
			"Actions: list find get insert put patch delete file wipe (wipe deletes everything for good: only when the person asked, with confirm). Data comes fenced as the users'.",
		Params: map[string]string{"filter": "Field (dotted ok) to a value or {$eq $ne $in $gt $gte $lt $lte $exists}."},
	},
	"schedules": {
		Text: "The room's schedules: list (with the connected group chats), create a timed AI task, or delete one. " +
			"Create only after the person confirmed what, when, the time zone and where it sends.",
		Params: map[string]string{
			"task":       "What to do each time, in plain words.",
			"recurrence": "weekly (days 0=Sunday…6, time HH:MM), daily (time), once (at, RFC 3339), cron.",
			"timezone":   "IANA, e.g. Asia/Shanghai; ask if unsure.",
		},
	},
	"room_help": {Text: "The full instructions of a room tool; topic is its name. Read it before using a tool you do not know. For you, not the person."},
}

// listed is the tool as tools/list carries it.
func listed(t Tool) (description string, schema map[string]any) {
	b, ok := briefs[t.Name]
	if !ok {
		return t.Description, t.InputSchema
	}
	return b.Text, briefSchema(t.InputSchema, b.Params)
}

// briefSchema is schema with its parameters' notes cut to keep: names, types,
// enums, bounds and required are untouched, at every level.
func briefSchema(schema map[string]any, keep map[string]string) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		slim := make(map[string]any, len(props))
		for name, raw := range props {
			p, ok := raw.(map[string]any)
			if !ok {
				slim[name] = raw
				continue
			}
			q := briefSchema(p, nil)
			delete(q, "description")
			if note, ok := keep[name]; ok {
				q["description"] = note
			}
			slim[name] = q
		}
		out["properties"] = slim
	}
	if items, ok := schema["items"].(map[string]any); ok {
		q := briefSchema(items, nil)
		delete(q, "description")
		out["items"] = q
	}
	return out
}

// manual is a tool's full instructions: its description and each parameter's
// note, as they were written.
func manual(t Tool) string {
	var b strings.Builder
	b.WriteString(t.Name)
	if t.Title != "" {
		b.WriteString(" — " + t.Title)
	}
	b.WriteString("\n\n" + t.Description + "\n")
	props, _ := t.InputSchema["properties"].(map[string]any)
	required := map[string]bool{}
	if req, ok := t.InputSchema["required"].([]string); ok {
		for _, r := range req {
			required[r] = true
		}
	}
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > 0 {
		b.WriteString("\nParameters:\n")
	}
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		line := "- " + n
		if typ, ok := p["type"].(string); ok {
			line += " (" + typ
			if required[n] {
				line += ", required"
			}
			line += ")"
		}
		if enum, ok := p["enum"]; ok {
			raw, _ := json.Marshal(enum)
			line += " one of " + string(raw)
		}
		if desc, ok := p["description"].(string); ok && desc != "" {
			line += ": " + desc
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// helpTool is room_help over r: with a topic that names a tool, its full
// instructions; without, the topics.
func helpTool(r *Registry) Tool {
	return Tool{
		Name:  "room_help",
		Title: "Instructions for a room tool",
		Description: "The full instructions of a room tool (its description and each parameter's note), by the tool's name. " +
			"Without a topic it names the tools. For the agent's use: none of it is for the person.",
		InputSchema: objectSchema(map[string]any{
			"topic": map[string]any{"type": "string", "description": "A tool's name."},
		}),
		ReadOnly: true,
		Call: func(_ context.Context, _ *Env, args json.RawMessage) Result {
			var in struct {
				Topic string `json:"topic"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return toolError("invalid arguments: %v", err)
			}
			topic := strings.TrimSpace(in.Topic)
			if topic == "" {
				return Result{Value: map[string]any{"topics": r.Names()}}
			}
			t, ok := r.Lookup(topic)
			if !ok {
				return toolError("no tool named %q; the tools are %s", topic, strings.Join(r.Names(), ", "))
			}
			return Result{Value: map[string]any{"topic": t.Name, "instructions": manual(t)}}
		},
	}
}
