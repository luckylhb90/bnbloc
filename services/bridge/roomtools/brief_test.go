package roomtools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// stripNotes is a schema without any "description": its shape alone.
func stripNotes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k != "description" {
				out[k] = stripNotes(val)
			}
		}
		return out
	default:
		return v
	}
}

// A tool without a brief is refused: tools/list lists a tool in brief, so a new
// tool is short by default instead of long by accident.
func TestEveryToolHasABrief(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Tool{Name: "long_winded", Description: strings.Repeat("words ", 200), Call: func(context.Context, *Env, json.RawMessage) Result { return Result{} }})
	if err == nil || !strings.Contains(err.Error(), "no brief") {
		t.Fatalf("a tool with no brief registered: %v", err)
	}
	for _, name := range DefaultRegistry().Names() {
		if _, ok := briefs[name]; !ok {
			t.Errorf("%s has no brief", name)
		}
	}
	for name := range briefs {
		if _, ok := DefaultRegistry().Lookup(name); !ok {
			t.Errorf("a brief for %q, which is not a tool", name)
		}
	}
}

// The brief changes words, never the shape: the same parameters, types, enums,
// bounds and required lists as the full schema.
func TestBriefKeepsTheSchemaShape(t *testing.T) {
	reg := DefaultRegistry()
	for _, d := range reg.Descriptors() {
		name := d["name"].(string)
		tool, _ := reg.Lookup(name)
		if !reflect.DeepEqual(stripNotes(d["inputSchema"]), stripNotes(tool.InputSchema)) {
			t.Errorf("%s: the listed schema differs in shape from the full one:\n%v\n%v", name, d["inputSchema"], tool.InputSchema)
		}
		if d["description"] == tool.Description && len(tool.Description) > 200 {
			t.Errorf("%s lists its full description", name)
		}
	}
}

// What a brief must keep: the constraints a tool is never to be used without,
// and a size that stays a brief.
func TestBriefsKeepTheirConstraintsAndStayShort(t *testing.T) {
	for name, want := range map[string][]string{
		"components": {"only when they ask", "Actions: list get try save publish", "room_help components"},
		"app_data":   {"wipe", "only when the person asked", "confirm"},
		"ask_agent":  {"under their authority"},
		"room_setup": {"as the person who asked you", "only after they agree", "Actions: get set use unuse"},
	} {
		for _, s := range want {
			if !strings.Contains(briefs[name].Text, s) {
				t.Errorf("the brief of %s lost %q: %s", name, s, briefs[name].Text)
			}
		}
	}
	for name, b := range briefs {
		if n := (len(b.Text) + 3) / 4; n > 70 {
			t.Errorf("the brief of %s is %d tokens: details belong in the full description (room_help)", name, n)
		}
	}
}

// room_help gives back what the brief left out, as it was written.
func TestRoomHelp(t *testing.T) {
	reg := DefaultRegistry()
	help, _ := reg.Lookup("room_help")
	call := func(args string) Result { return help.Call(context.Background(), &Env{}, json.RawMessage(args)) }

	r := call(`{"topic":"components"}`)
	if r.Err != "" {
		t.Fatal(r.Err)
	}
	text := r.Value.(map[string]any)["instructions"].(string)
	full, _ := reg.Lookup("components")
	for _, want := range []string{full.Description, "Parameters:", "- dir (string): For try and save: the workspace-relative directory holding component.json", "- kind (string) one of [\"expert\",\"skill\",\"prompt\",\"mcp\",\"room\"]", "- action (string, required) one of [\"list\",\"get\",\"try\",\"save\",\"publish\"]"} {
		if !strings.Contains(text, want) {
			t.Errorf("the manual lacks %q:\n%s", want, text)
		}
	}
	if r := call(`{"topic":"nope"}`); !strings.Contains(r.Err, "no tool named") || !strings.Contains(r.Err, "components") {
		t.Errorf("an unknown topic names the tools: %q", r.Err)
	}
	list := call(`{}`).Value.(map[string]any)["topics"].([]string)
	if len(list) != len(reg.Names()) || list[len(list)-1] != "room_help" {
		t.Errorf("topics = %v", list)
	}
}
