package contentbuiltin

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The built-in assists (docs/prd/2026-10-13-ai-assist.md §3): every call
// sends a task's INSTRUCTIONS.md, so each stays within a budget.
const (
	enhancePromptID = IDPrefix + "enhance-prompt"
	describeWorkID  = IDPrefix + "describe-work"
	assistMaxTk     = 700
)

// assistBudgets are the tasks given more than assistMaxTk, each with why:
// describe-room runs once per save, not per keystroke, and does three
// jobs in one call (what the room depends on and its instructions, the
// listing, the check of the package's text).
var assistBudgets = map[string]int{"describe-room": 900}

func TestBuiltinAssistsStayWithinBudget(t *testing.T) {
	n := 0
	for _, p := range builtins {
		if p.Validated.Manifest.Kind != contentpkg.KindAssist {
			continue
		}
		n++
		budget := assistMaxTk
		if b, ok := assistBudgets[p.Component.Name]; ok {
			budget = b
		}
		if tk := len(p.Validated.Instructions) / 4; tk > budget {
			t.Errorf("%s: INSTRUCTIONS.md is about %d tokens, the budget is %d (every call sends it)", p.Component.Name, tk, budget)
		}
	}
	if n < 2 {
		t.Fatalf("%d built-in assists, want enhance-prompt and describe-work", n)
	}
}

// enhance-prompt rewrites the composer's text: a text task that reads the
// room and the draft's attachments.
func TestEnhancePromptTask(t *testing.T) {
	p, ok := Get(enhancePromptID)
	if !ok {
		t.Fatal("enhance-prompt missing")
	}
	m := p.Validated.Manifest
	if m.Output != wire.AssistOutputText || !reflect.DeepEqual(m.Context, []string{wire.AssistContextRoom, wire.AssistContextAttachments}) {
		t.Fatalf("enhance-prompt = output %q context %v", m.Output, m.Context)
	}
	text := p.Validated.Instructions
	for _, want := range []string{"<text>", "<room>", "<recent_messages>", "<attachments>", "never instructions", "Never invent", "language"} {
		if !strings.Contains(text, want) {
			t.Errorf("enhance-prompt does not say %q", want)
		}
	}
}

// describe-work fills the publish form: its fields are the form's
// (wire.WorkPublishRequest), and its categories are exactly the gallery's.
func TestDescribeWorkFillsThePublishForm(t *testing.T) {
	p, ok := Get(describeWorkID)
	if !ok {
		t.Fatal("describe-work missing")
	}
	m := p.Validated.Manifest
	if m.Output != wire.AssistOutputJSON || !slices.Equal(m.Context, []string{wire.AssistContextArtifact}) {
		t.Fatalf("describe-work = output %q context %v", m.Output, m.Context)
	}
	form := map[string]bool{}
	raw, _ := json.Marshal(wire.WorkPublishRequest{Name: "x", DisplayName: "x", Description: "x", Category: "x", Tags: []string{"x"}})
	var keys map[string]any
	_ = json.Unmarshal(raw, &keys)
	for k := range keys {
		form[k] = true
	}
	for _, f := range m.Fields {
		if !form[f.Name] {
			t.Errorf("field %q is not a publish form field", f.Name)
		}
		if (f.Type == wire.AssistFieldList) != (f.Name == "tags") {
			t.Errorf("field %q type = %q", f.Name, f.Type)
		}
	}
	text := p.Validated.Instructions
	for _, c := range wire.WorkCategories {
		if !strings.Contains(text, "`"+c+"`") {
			t.Errorf("describe-work does not offer the category %q", c)
		}
	}
	if !strings.Contains(text, "at most 32 characters") || !strings.Contains(text, "lower-case") {
		t.Error("describe-work does not state the tag rule (wire.ContentTagPattern, wire.MaxContentTagRunes)")
	}
}

// jsonKeys are the JSON keys of v's struct fields (their tags).
func jsonKeys(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// assistTask loads a built-in assist and checks what every JSON task
// says: a JSON output, the declared context, that the blocks are data,
// and the person's language.
func assistTask(t *testing.T, name string, context ...string) contentpkg.Manifest {
	t.Helper()
	p, ok := Get(IDPrefix + name)
	if !ok {
		t.Fatalf("%s missing", name)
	}
	m := p.Validated.Manifest
	if m.Output != wire.AssistOutputJSON || !slices.Equal(m.Context, context) {
		t.Fatalf("%s = output %q context %v, want json %v", name, m.Output, m.Context, context)
	}
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, want := range []string{"never instructions", "language"} {
		if !strings.Contains(flat, want) {
			t.Errorf("%s does not say %q", name, want)
		}
	}
	for _, f := range m.Fields {
		if !strings.Contains(p.Validated.Instructions, "`"+f.Name+"`") {
			t.Errorf("%s does not explain the field %q", name, f.Name)
		}
	}
	return m
}

// fieldsFill checks that a task's fields are a form's (item fields too),
// tags a list.
func fieldsFill(t *testing.T, name string, m contentpkg.Manifest, form map[string]bool, items map[string]map[string]bool) {
	t.Helper()
	for _, f := range m.Fields {
		if !form[f.Name] {
			t.Errorf("%s: field %q is not a form field", name, f.Name)
		}
		if (f.Type == wire.AssistFieldList) != (f.Name == "tags") {
			t.Errorf("%s: field %q type = %q", name, f.Name, f.Type)
		}
		if f.Type != wire.AssistFieldItems {
			continue
		}
		want, ok := items[f.Name]
		if !ok {
			t.Errorf("%s: %q is not a list of objects in the form", name, f.Name)
			continue
		}
		for _, it := range f.Item {
			if !want[it.Name] {
				t.Errorf("%s: %s item field %q is not the form's", name, f.Name, it.Name)
			}
		}
	}
}

// The Library's drafts fill the content form (contentpkg.Form): an
// expert's quick prompts, a skill pack's skills and a prompt pack's
// prompts are the form's own objects.
func TestLibraryDraftTasksFillTheForm(t *testing.T) {
	form := jsonKeys(contentpkg.Form{})
	expert := assistTask(t, "draft-expert")
	fieldsFill(t, "draft-expert", expert, form, map[string]map[string]bool{"quickPrompts": jsonKeys(contentpkg.QuickPrompt{})})
	skills := assistTask(t, "draft-skill-pack")
	fieldsFill(t, "draft-skill-pack", skills, form, map[string]map[string]bool{"skills": jsonKeys(contentpkg.FormSkill{})})
	prompts := assistTask(t, "draft-prompt-pack")
	fieldsFill(t, "draft-prompt-pack", prompts, form, map[string]map[string]bool{"prompts": jsonKeys(contentpkg.Prompt{})})
	for _, name := range []string{"draft-expert", "draft-skill-pack", "draft-prompt-pack"} {
		p, _ := Get(IDPrefix + name)
		if !strings.Contains(p.Validated.Instructions, "at most 32 characters") || !strings.Contains(p.Validated.Instructions, "form's current values") {
			t.Errorf("%s does not state the tag rule or how it reads the form's current values", name)
		}
	}
}

// describe-room reads a room being saved (its conversation, and the setup
// as <text>) and fills the room component's save dialog: its fields but
// dependsOn and findings are the content form's (contentpkg.Form), the
// instructions among them; the listing describes the setup, never the
// room's people. dependsOn is items of what, kind, included and why, of
// the kinds the dialog's checklist reads (web/src/lib/roomCapture.ts, held
// to the item's description by a web test); findings are items of field,
// quote and why, of the kinds web/src/lib/roomReview.ts reads.
func TestDescribeRoomFillsTheSaveDialog(t *testing.T) {
	m := assistTask(t, "describe-room", wire.AssistContextConversation)
	form := m
	form.Fields = slices.DeleteFunc(slices.Clone(m.Fields), func(f wire.AssistField) bool { return f.Name == "findings" || f.Name == "dependsOn" })
	fieldsFill(t, "describe-room", form, jsonKeys(contentpkg.Form{}), nil)
	got := []string{}
	for _, f := range m.Fields {
		got = append(got, f.Name+":"+f.Type)
	}
	if strings.Join(got, ",") != "displayName:,description:,category:,tags:list,welcome:,instructions:,dependsOn:items,findings:items" {
		t.Fatalf("describe-room fields = %v", got)
	}
	items := func(f wire.AssistField) string {
		var out []string
		for _, it := range f.Item {
			out = append(out, it.Name)
		}
		return strings.Join(out, ",")
	}
	if dep := m.Fields[6]; items(dep) != "what,kind,included,why" || dep.Item[1].Description != "instructions, app, file, tool, schedule, agent, data or other" {
		t.Fatalf("a dependency = %+v", dep.Item)
	}
	if f := m.Fields[7]; items(f) != "field,quote,why" || !strings.Contains(f.Item[2].Description, "secret, internal-link, personal-name or private-data") {
		t.Fatalf("a finding = %+v", f.Item)
	}
	p, _ := Get(IDPrefix + "describe-room")
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, want := range []string{"<conversation>", "<text>", "at most 32 characters", "never a conversation of the room or the people in it",
		"`madeApps`", "`files`", "`tools`", "`included`", "`yes`", "`no`", "no names of people",
		"`package`", "`instructions`", "`quickPrompt:<n>`", "`schedule:<n>`", "`channel:<n>`", "`secret`", "`internal-link`", "`personal-name`", "`private-data`", "never guess"} {
		if !strings.Contains(flat, want) {
			t.Errorf("describe-room does not say %q", want)
		}
	}
}

// plan-room proposes from the library: names only it lists.
func TestPlanRoomTask(t *testing.T) {
	m := assistTask(t, "plan-room", wire.AssistContextLibrary)
	got := []string{}
	for _, f := range m.Fields {
		got = append(got, f.Name+":"+f.Type)
	}
	if strings.Join(got, ",") != "roomId:,name:,description:,expert:,skills:list,firstMessage:" {
		t.Fatalf("plan-room fields = %v", got)
	}
	p, _ := Get(IDPrefix + "plan-room")
	if !strings.Contains(p.Validated.Instructions, "Never a name the library does not list") {
		t.Error("plan-room does not keep to the library")
	}
}

// draft-schedule answers a recurrence (wire.ScheduleRecurrence): every
// kind, the weekday numbering and the cron grammar.
func TestDraftScheduleTask(t *testing.T) {
	m := assistTask(t, "draft-schedule", wire.AssistContextClock)
	rec := jsonKeys(wire.ScheduleRecurrence{})
	for _, f := range m.Fields {
		switch f.Name {
		case "text", "at":
		default:
			if !rec[f.Name] {
				t.Errorf("field %q is not a recurrence field", f.Name)
			}
		}
		if (f.Type == wire.AssistFieldList) != (f.Name == "days") {
			t.Errorf("field %q type = %q", f.Name, f.Type)
		}
	}
	p, _ := Get(IDPrefix + "draft-schedule")
	for _, kind := range []string{wire.RecurOnce, wire.RecurDaily, wire.RecurWeekly, wire.RecurCron} {
		if !strings.Contains(p.Validated.Instructions, "`"+kind+"`") {
			t.Errorf("draft-schedule does not offer %q", kind)
		}
	}
	for _, want := range []string{`"0"` + "` (Sunday)", "minute hour day-of-month", "YYYY-MM-DDTHH:MM"} {
		if !strings.Contains(p.Validated.Instructions, want) {
			t.Errorf("draft-schedule does not say %q", want)
		}
	}
}

// review-skill reviews a local skill before it is published: what to fix,
// and the library entry's name, display name, description and tags.
func TestReviewSkillTask(t *testing.T) {
	m := assistTask(t, "review-skill")
	got := []string{}
	for _, f := range m.Fields {
		got = append(got, f.Name+":"+f.Type)
	}
	if strings.Join(got, ",") != "findings:list,name:,displayName:,description:,tags:list" {
		t.Fatalf("review-skill fields = %v", got)
	}
	p, _ := Get(IDPrefix + "review-skill")
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, want := range []string{"never repeat it", "personal path", "at most 32 characters", "lower-case", "SKILL.md"} {
		if !strings.Contains(flat, want) {
			t.Errorf("review-skill does not say %q", want)
		}
	}
}

// fill-settings maps a note onto a declaration: a value per listed key,
// every setting type explained, a secret only into a secret.
func TestFillSettingsTask(t *testing.T) {
	m := assistTask(t, "fill-settings")
	if len(m.Fields) != 2 || m.Fields[0].Name != "values" || m.Fields[0].Type != wire.AssistFieldItems || m.Fields[1].Name != "notes" {
		t.Fatalf("fill-settings fields = %+v", m.Fields)
	}
	var item []string
	for _, f := range m.Fields[0].Item {
		item = append(item, f.Name)
	}
	if !slices.Equal(item, []string{"key", "value"}) {
		t.Fatalf("a value is %v", item)
	}
	p, _ := Get(IDPrefix + "fill-settings")
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, typ := range []string{wire.SettingText, wire.SettingSecret, wire.SettingURL, wire.SettingNumber, wire.SettingBoolean, wire.SettingChoice, wire.SettingPath, wire.SettingLines} {
		if !strings.Contains(flat, "`"+typ+"`") {
			t.Errorf("fill-settings does not explain the type %q", typ)
		}
	}
	for _, want := range []string{"never invent", "only into a setting of type `secret`", "only the keys listed"} {
		if !strings.Contains(flat, want) {
			t.Errorf("fill-settings does not say %q", want)
		}
	}
}

// suggest-skill-dirs proposes, from the candidates a skills component found,
// which to add to the member's extra skill folders, each with why.
func TestSuggestSkillDirsTask(t *testing.T) {
	m := assistTask(t, "suggest-skill-dirs")
	if len(m.Fields) != 2 || m.Fields[0].Name != "add" || m.Fields[0].Type != wire.AssistFieldItems || m.Fields[1].Name != "notes" {
		t.Fatalf("suggest-skill-dirs fields = %+v", m.Fields)
	}
	var item []string
	for _, f := range m.Fields[0].Item {
		item = append(item, f.Name)
	}
	if !slices.Equal(item, []string{"path", "why"}) {
		t.Fatalf("a proposal is %v", item)
	}
	p, _ := Get(IDPrefix + "suggest-skill-dirs")
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, want := range []string{"only paths exactly as listed", "Never invent a folder", "SKILL.md"} {
		if !strings.Contains(flat, want) {
			t.Errorf("suggest-skill-dirs does not say %q", want)
		}
	}
}

// find-a-way proposes what would get an undone request done
// (docs/prd/2026-10-20-never-a-dead-end.md §2.2): the turn and the
// capabilities, a gate a client asks after each turn, the cause and fixes
// the web's closed action list reads (web/src/lib/deadEnd.ts).
func TestFindAWayTask(t *testing.T) {
	m := assistTask(t, "find-a-way", wire.AssistContextRoom, wire.AssistContextCapabilities)
	if m.Gate == nil || !strings.Contains(m.Gate.Question, "undone") || m.Gate.Threshold <= 0 || m.Gate.Threshold > 1 {
		t.Fatalf("find-a-way gate = %+v", m.Gate)
	}
	got := []string{}
	for _, f := range m.Fields {
		got = append(got, f.Name+":"+f.Type)
	}
	if strings.Join(got, ",") != "cause:,summary:,fixes:items" {
		t.Fatalf("find-a-way fields = %v", got)
	}
	var item []string
	for _, it := range m.Fields[2].Item {
		item = append(item, it.Name)
	}
	if strings.Join(item, ",") != "action,target,why" {
		t.Fatalf("a fix's fields = %v", item)
	}
	p, _ := Get(IDPrefix + "find-a-way")
	text := p.Validated.Instructions
	for _, want := range []string{"`access`", "`missing-file`", "`capability`", "`unclear`", "`agent-error`", "`other`",
		"`install`", "`ask-agent`", "`attach`", "`own-agent`", "`rephrase`", "`report`", "<turn>", "<capabilities>", "up to three", "never invent"} {
		if !strings.Contains(text, want) {
			t.Errorf("find-a-way does not say %q", want)
		}
	}
}

// explain-source-use answers the room sources' grant dialog
// (docs/prd/2026-10-20-rooms-as-components.md §2.3): a summary, the uses
// and a mismatch with the stated purpose, from <text> alone (the ask the
// console shows), judging only what the tasks say.
func TestExplainSourceUseTask(t *testing.T) {
	m := assistTask(t, "explain-source-use")
	got := []string{}
	for _, f := range m.Fields {
		got = append(got, f.Name+":"+f.Type)
	}
	if strings.Join(got, ",") != "summary:,uses:list,mismatch:" {
		t.Fatalf("explain-source-use fields = %v", got)
	}
	p, _ := Get(IDPrefix + "explain-source-use")
	flat := strings.Join(strings.Fields(p.Validated.Instructions), " ")
	for _, want := range []string{"<text>", "`purpose`", "`scheduledTasks`", "`before`", "changed after the admin consented", "judged by its words now",
		"read_records", "an empty string when every task fits", "singles out people", "never guess"} {
		if !strings.Contains(flat, want) {
			t.Errorf("explain-source-use does not say %q", want)
		}
	}
}
