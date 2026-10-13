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
		if f.List != (f.Name == "tags") {
			t.Errorf("field %q list = %v", f.Name, f.List)
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
