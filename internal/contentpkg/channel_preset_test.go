package contentpkg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func presetBundle(t *testing.T, channel map[string]any, extra map[string]any, files map[string]string) []byte {
	t.Helper()
	m := map[string]any{"kind": KindChannelPreset, "name": "help-desk", "version": "1.0.0", "displayName": "Help desk",
		"description": "Questions answered first by the channel's agent.", "channel": channel}
	for k, v := range extra {
		m[k] = v
	}
	all := map[string][]byte{ManifestFile: manifestJSON(t, m)}
	for k, v := range files {
		all[k] = []byte(v)
	}
	raw, err := tarbundle.PackFiles(all, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func deskChannel() map[string]any {
	return map[string]any{"purpose": "Ask anything about the product", "rules": "One question per thread.",
		"settings": map[string]any{"agentParticipation": wire.ChannelAgentsQuestions, "slowModeSeconds": 10, "postingStyle": "threads",
			"tags":         []map[string]any{{"id": "billing", "name": "Billing", "color": "#3b82f6"}, {"id": "bug", "name": "Bug"}},
			"statuses":     []map[string]any{{"id": "open", "name": "Open"}, {"id": "answered", "name": "Answered"}},
			"postTemplate": "What did you try?\nWhat happened?"}}
}

// A preset holds a channel's purpose, rules and settings, the room service's
// keys checked by its rules (the known ones) and by shape (the coming ones),
// and optionally an expert.
func TestChannelPresetValidation(t *testing.T) {
	p, err := Validate(presetBundle(t, deskChannel(), map[string]any{"requires": map[string]any{"expert": "support-lead@^1"}}, map[string]string{ReadmeFile: "# Help desk\n"}), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := p.Manifest.Channel
	var s map[string]any
	if c == nil || c.Purpose != "Ask anything about the product" || json.Unmarshal(c.Settings, &s) != nil || s["agentParticipation"] != wire.ChannelAgentsQuestions || p.Manifest.Requires.Expert != "support-lead@^1" {
		t.Fatalf("preset = %+v (settings %s)", c, c.Settings)
	}
	bad := func(what string, mutate func(map[string]any), want string) {
		t.Helper()
		ch := deskChannel()
		mutate(ch)
		_, err := Validate(presetBundle(t, ch, nil, nil), "", Options{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", what, err, want)
		}
	}
	set := func(k string, v any) func(map[string]any) {
		return func(ch map[string]any) { ch["settings"].(map[string]any)[k] = v }
	}
	bad("a purpose on two lines", func(ch map[string]any) { ch["purpose"] = "a\nb" }, "one line")
	bad("a long purpose", func(ch map[string]any) { ch["purpose"] = strings.Repeat("p", wire.MaxChannelPurpose+1) }, "longer than")
	bad("an unknown whoPosts", set("whoPosts", "owners"), "whoPosts")
	bad("an unknown participation", set("agentParticipation", "always"), "agentParticipation")
	bad("a negative slow mode", set("slowModeSeconds", -1), "slowModeSeconds")
	bad("an unknown posting style", set("postingStyle", "wiki"), "postingStyle")
	bad("a default agent (a person's)", set("defaultAgent", map[string]any{"ownerId": "maya", "provider": "codex"}), "defaultAgent")
	bad("an unknown key", set("archive", true), "settings.archive")
	bad("a tag id twice", set("tags", []map[string]any{{"id": "a", "name": "A"}, {"id": "a", "name": "B"}}), "listed twice")
	bad("a tag without a name", set("tags", []map[string]any{{"id": "a", "name": " "}}), "needs a name")
	bad("a status id that is no name", set("statuses", []map[string]any{{"id": "In Progress", "name": "In progress"}}), "lower-case")
	bad("too many statuses", set("statuses", func() []map[string]any {
		var out []map[string]any
		for _, n := range names("s", maxPresetStatuses+1) {
			out = append(out, map[string]any{"id": n, "name": n})
		}
		return out
	}()), "at most")
	bad("a long template", set("postTemplate", strings.Repeat("t", maxPostTemplate+1)), "longer than")
	if _, err := Validate(presetBundle(t, map[string]any{"purpose": "Notes"}, nil, nil), "", Options{}); err != nil {
		t.Errorf("a preset of a purpose alone: %v", err)
	}
	for what, extra := range map[string]map[string]any{
		"a skill pack":  {"requires": map[string]any{"skills": []string{"charts@^1"}}},
		"quick prompts": {"quickPrompts": []map[string]any{{"title": "Go", "prompt": "Go."}}},
		"no channel":    {"channel": nil},
	} {
		if _, err := Validate(presetBundle(t, deskChannel(), extra, nil), "", Options{}); err == nil {
			t.Errorf("a preset with %s validated", what)
		}
	}
	if _, err := Validate(presetBundle(t, deskChannel(), nil, map[string]string{"notes.txt": "x"}), "", Options{}); err == nil {
		t.Error("a preset with an extra file validated")
	}
	// The channel belongs to presets only; an expert to a room and a preset.
	if _, err := Validate(assistBundle(t, map[string]any{"channel": deskChannel()}, nil), "", Options{}); err == nil || !strings.Contains(err.Error(), "channel preset") {
		t.Errorf("an assist with a channel: err = %v", err)
	}
}

// The form lays a preset out as its manifest alone; it is no content use.
func TestChannelPresetFromForm(t *testing.T) {
	settings, _ := json.Marshal(map[string]any{"whoPosts": wire.ChannelPostersAdmins})
	f := Form{Kind: "channel-preset", Name: "announcements", Channel: &wire.ChannelPreset{Purpose: " News only ", Rules: " Admins post. ", Settings: settings}}
	p, _, err := Build(f, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Bundle.Paths(); len(got) != 1 || got[0] != ManifestFile {
		t.Fatalf("files = %v", got)
	}
	if c := p.Manifest.Channel; c.Purpose != "News only" || c.Rules != "Admins post." {
		t.Fatalf("channel = %+v", c)
	}
	if spec, _ := Lookup("channel-preset"); spec.Use || spec.MemberUse {
		t.Error("a channel preset is no content use")
	}
	f.Instructions = "Be helpful."
	if _, _, err := Build(f, "1.0.0", Options{}, nil); err == nil {
		t.Error("a preset form with instructions saved")
	}
	if _, _, err := Build(Form{Kind: "expert", Name: "x", Instructions: "Go.", Channel: f.Channel}, "1.0.0", Options{}, nil); err == nil {
		t.Error("an expert form with a channel saved")
	}
}
