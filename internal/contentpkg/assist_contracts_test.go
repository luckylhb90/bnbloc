package contentpkg

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// contractManifest is an assist package meeting task's contract: its
// fields in their types, a gate when the contract asks for one.
func contractManifest(t *testing.T, task string) map[string]any {
	t.Helper()
	c, ok := wire.AssistContractOf(task)
	if !ok {
		t.Fatalf("%s has no contract", task)
	}
	m := map[string]any{"kind": KindAssist, "name": task, "version": "1.0.0", "displayName": task, "output": c.Output}
	var fields []map[string]any
	for _, f := range c.Fields {
		fm := map[string]any{"name": f.Name, "type": f.Type}
		var items []map[string]any
		for _, it := range f.Item {
			items = append(items, map[string]any{"name": it.Name})
		}
		if len(items) > 0 {
			fm["item"] = items
		}
		fields = append(fields, fm)
	}
	if len(fields) > 0 {
		m["fields"] = fields
	}
	if c.Gate {
		m["gate"] = map[string]any{"question": "Was the request left undone?", "threshold": 0.7}
	}
	return m
}

func contractBundle(t *testing.T, m map[string]any) []byte {
	t.Helper()
	return bundle(t, map[string]string{ManifestFile: string(manifestJSON(t, m)), InstructionsFile: "Do the task.\n"})
}

// Every task the web and the gateway run by name has one contract, and a
// package built from it validates: the registry asks for nothing the
// validator refuses. Values close only text fields.
func TestAssistContractsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range wire.AssistContracts {
		if seen[c.Task] {
			t.Errorf("%s has two contracts", c.Task)
		}
		seen[c.Task] = true
		if _, err := Validate(contractBundle(t, contractManifest(t, c.Task)), "", Options{Contracts: true}); err != nil {
			t.Errorf("%s: a package meeting the contract is refused: %v", c.Task, err)
		}
		var check func(in string, fs []wire.AssistContractField)
		check = func(in string, fs []wire.AssistContractField) {
			for _, f := range fs {
				if len(f.Values) > 0 && fieldType(f.Type) != wire.AssistFieldText {
					t.Errorf("%s: %s%s closes the values of a %s field", c.Task, in, f.Name, f.Type)
				}
				check(in+f.Name+".", f.Item)
			}
		}
		check("", c.Fields)
	}
	if len(seen) != 19 {
		t.Errorf("%d contracts, want the 19 tasks the web and the gateway run", len(seen))
	}
}

// A package under a task's name that does not meet its contract is
// refused at save and install (Options.Contracts), never when stored
// content is read; any other name is free.
func TestAssistContractRefusals(t *testing.T) {
	cases := []struct {
		name, task, want string
		change           func(m map[string]any)
	}{
		{"text for a JSON task", wire.AssistTaskDescribeWork, "answers json", func(m map[string]any) {
			m["output"] = wire.AssistOutputText
			delete(m, "fields")
		}},
		{"a field left out", wire.AssistTaskExplainSourceUse, `"mismatch"`, func(m map[string]any) {
			m["fields"] = m["fields"].([]map[string]any)[:2]
		}},
		{"a field of another type", wire.AssistTaskExplainSourceUse, `"uses" is list, not text`, func(m map[string]any) {
			m["fields"].([]map[string]any)[1]["type"] = ""
		}},
		{"an item field left out", wire.AssistTaskFindAWay, `"fixes.why"`, func(m map[string]any) {
			f := m["fields"].([]map[string]any)[2]
			f["item"] = f["item"].([]map[string]any)[:2]
		}},
		{"no gate for a gated task", wire.AssistTaskFindAWay, "declares a gate", func(m map[string]any) { delete(m, "gate") }},
	}
	for _, c := range cases {
		m := contractManifest(t, c.task)
		c.change(m)
		raw := contractBundle(t, m)
		if _, err := Validate(raw, "", Options{Contracts: true}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if _, err := Validate(raw, "", Options{}); err != nil {
			t.Errorf("%s: stored content read refused: %v", c.name, err)
		}
	}
	// More fields than the contract's are allowed; another name is free.
	m := contractManifest(t, wire.AssistTaskExplainSourceUse)
	m["fields"] = append(m["fields"].([]map[string]any), map[string]any{"name": "confidence"})
	if _, err := Validate(contractBundle(t, m), "", Options{Contracts: true}); err != nil {
		t.Errorf("an extra field: %v", err)
	}
	free := contractManifest(t, wire.AssistTaskDescribeWork)
	free["name"], free["output"] = "my-task", wire.AssistOutputText
	delete(free, "fields")
	if _, err := Validate(contractBundle(t, free), "", Options{Contracts: true}); err != nil {
		t.Errorf("an assist of another name: %v", err)
	}
}

// jsonKeys are the JSON keys of v's struct fields (their tags).
func jsonKeys(v any) map[string]bool {
	out := map[string]bool{}
	ty := reflect.TypeOf(v)
	for i := 0; i < ty.NumField(); i++ {
		name, _, _ := strings.Cut(ty.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// contractFor is the contract of task, by name.
func contractFor(t *testing.T, task string) wire.AssistContract {
	t.Helper()
	c, ok := wire.AssistContractOf(task)
	if !ok {
		t.Fatalf("%s has no contract", task)
	}
	return c
}

// fillsForm checks that a contract's fields are a form's (items' fields
// too, skip aside), tags the one list.
func fillsForm(t *testing.T, task string, form map[string]bool, items map[string]map[string]bool, skip ...string) {
	t.Helper()
	for _, f := range contractFor(t, task).Fields {
		if slices.Contains(skip, f.Name) {
			continue
		}
		if !form[f.Name] {
			t.Errorf("%s: field %q is not a form field", task, f.Name)
		}
		if (f.Type == wire.AssistFieldList) != (f.Name == "tags") {
			t.Errorf("%s: field %q type = %q", task, f.Name, f.Type)
		}
		if f.Type != wire.AssistFieldItems {
			continue
		}
		want, ok := items[f.Name]
		if !ok {
			t.Errorf("%s: %q is not a list of objects in the form", task, f.Name)
			continue
		}
		for _, it := range f.Item {
			if !want[it.Name] {
				t.Errorf("%s: %s item field %q is not the form's", task, f.Name, it.Name)
			}
		}
	}
}

// The contracts' fields are the shapes the callers fill: the publish form
// (wire.WorkPublishRequest) and its categories, the content form
// (contentpkg.Form: an expert's quick prompts, a skill pack's skills, a
// prompt pack's prompts; a room's save dialog), a schedule's recurrence
// (wire.ScheduleRecurrence) and its kinds.
func TestAssistContractsFillTheirForms(t *testing.T) {
	raw, _ := json.Marshal(wire.WorkPublishRequest{Name: "x", DisplayName: "x", Description: "x", Category: "x", Tags: []string{"x"}})
	var publish map[string]any
	_ = json.Unmarshal(raw, &publish)
	keys := map[string]bool{}
	for k := range publish {
		keys[k] = true
	}
	fillsForm(t, wire.AssistTaskDescribeWork, keys, nil)
	for _, f := range contractFor(t, wire.AssistTaskDescribeWork).Fields {
		if f.Name == "category" && !slices.Equal(f.Values, wire.WorkCategories) {
			t.Errorf("describe-work's categories %v are not the gallery's %v", f.Values, wire.WorkCategories)
		}
	}

	form := jsonKeys(Form{})
	fillsForm(t, wire.AssistTaskDraftExpert, form, map[string]map[string]bool{"quickPrompts": jsonKeys(QuickPrompt{})})
	fillsForm(t, wire.AssistTaskDraftSkillPack, form, map[string]map[string]bool{"skills": jsonKeys(FormSkill{})})
	fillsForm(t, wire.AssistTaskDraftPromptPack, form, map[string]map[string]bool{"prompts": jsonKeys(Prompt{})})
	fillsForm(t, wire.AssistTaskDescribeRoom, form, nil, "dependsOn", "findings")

	rec := jsonKeys(wire.ScheduleRecurrence{})
	for _, f := range contractFor(t, wire.AssistTaskDraftSchedule).Fields {
		switch f.Name {
		case "text", "at":
		default:
			if !rec[f.Name] {
				t.Errorf("draft-schedule's field %q is not a recurrence field", f.Name)
			}
		}
		if (f.Type == wire.AssistFieldList) != (f.Name == "days") {
			t.Errorf("draft-schedule's field %q type = %q", f.Name, f.Type)
		}
		if f.Name == "kind" && !slices.Equal(f.Values, []string{wire.RecurOnce, wire.RecurDaily, wire.RecurWeekly, wire.RecurCron}) {
			t.Errorf("draft-schedule's kinds = %v", f.Values)
		}
	}
}

// everyRoom is a prompt pack's alone, from a package and from the form.
func TestEveryRoomOnlyOnPromptPacks(t *testing.T) {
	pack := map[string]any{"kind": KindPrompt, "name": "starters", "version": "1.0.0", "everyRoom": true,
		"prompts": []map[string]any{{"id": "plan", "title": "Plan", "body": "Plan a launch.", "starter": true}}}
	p, err := Validate(bundle(t, map[string]string{ManifestFile: string(manifestJSON(t, pack))}), "", Options{})
	if err != nil || !p.Manifest.EveryRoom {
		t.Fatalf("a pack offered in every room = %+v, %v", p, err)
	}
	expert := expertFiles(t, map[string]any{"everyRoom": true})
	if _, err := Validate(bundle(t, expert), "", Options{}); err == nil || !strings.Contains(err.Error(), "everyRoom") {
		t.Fatalf("an expert offered in every room = %v", err)
	}
	built, _, err := Build(Form{Kind: "prompt", Name: "starters", EveryRoom: true, Prompts: []Prompt{{ID: "plan", Title: "Plan", Body: "Plan a launch.", Starter: true}}}, "1.0.0", Options{}, nil)
	if err != nil || !built.Manifest.EveryRoom {
		t.Fatalf("a form's pack = %+v, %v", built, err)
	}
	if _, _, err := Build(Form{Kind: "expert", Name: "x", Instructions: "x", EveryRoom: true}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("an expert form offered in every room")
	}
}

// The limits file a room's records hold states every bound of the
// validator's table, with its unit.
func TestLimitsDocStatesEveryLimit(t *testing.T) {
	doc := LimitsDoc()
	for _, l := range Limits() {
		if row := "| " + l.Applies + " | " + l.Field + " | " + limitAmount(l) + " |"; !strings.Contains(doc, row) {
			t.Errorf("the limits file lacks %q", row)
		}
	}
	if limitAmount(Limit{Max: 8 << 20, Unit: UnitBytes}) != "8 MiB" || limitAmount(Limit{Max: 16 << 10, Unit: UnitBytes}) != "16 KiB" || limitAmount(Limit{Max: 120, Unit: UnitChars}) != "120 characters" {
		t.Error("amounts are not stated in their units")
	}
}
