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

func TestBuiltinAssistsStayWithinBudget(t *testing.T) {
	n := 0
	for _, p := range builtins {
		if p.Validated.Manifest.Kind != contentpkg.KindAssist {
			continue
		}
		n++
		if tk := len(p.Validated.Instructions) / 4; tk > assistMaxTk {
			t.Errorf("%s: INSTRUCTIONS.md is about %d tokens, the budget is %d (every call sends it)", p.Component.Name, tk, assistMaxTk)
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
