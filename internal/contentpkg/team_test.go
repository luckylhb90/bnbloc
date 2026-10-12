package contentpkg

import (
	"strings"
	"testing"
)

func teamBlock(members ...map[string]any) map[string]any {
	return map[string]any{"team": map[string]any{"lead": "coordinator", "members": members}}
}

func role(name, expert, agent string) map[string]any {
	return map[string]any{"role": name, "expert": expert, "defaultAgent": agent}
}

func TestValidateTeam(t *testing.T) {
	p, err := Validate(bundle(t, expertFiles(t, teamBlock(
		role("reviewer", "code-reviewer@^1", "requester"),
		role("tester", "tester", "platform"),
		map[string]any{"role": "writer", "expert": "doc-writer@~1.2", "defaultAgent": "member:jack/codex", "description": "Writes the release notes."},
	))), "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	team := p.Manifest.Team
	if team == nil || team.LeadRole() != "coordinator" || len(team.Members) != 3 {
		t.Fatalf("team = %+v", team)
	}
	if m, ok := team.Member("writer"); !ok || m.Description != "Writes the release notes." {
		t.Fatalf("writer = %+v %v", m, ok)
	}

	nine := make([]map[string]any, 9)
	for i := range nine {
		nine[i] = role("r"+string(rune('a'+i)), "x", "requester")
	}
	bad := map[string]map[string]any{
		"no members":       teamBlock(),
		"too many":         teamBlock(nine...),
		"bad role":         teamBlock(role("Reviewer", "x", "requester")),
		"duplicate role":   teamBlock(role("a", "x", "requester"), role("a", "y", "platform")),
		"lead's role":      teamBlock(role("coordinator", "x", "requester")),
		"bad expert":       teamBlock(role("a", "x@^banana", "requester")),
		"no expert":        teamBlock(role("a", "", "requester")),
		"bad agent":        teamBlock(role("a", "x", "anyone")),
		"empty member":     teamBlock(role("a", "x", "member:")),
		"member slash":     teamBlock(role("a", "x", "member:jack/")),
		"multiline lead":   {"team": map[string]any{"lead": "a\nb", "members": []map[string]any{role("a", "x", "platform")}}},
		"unknown field":    teamBlock(map[string]any{"role": "a", "expert": "x", "defaultAgent": "platform", "model": "y"}),
		"long description": teamBlock(map[string]any{"role": "a", "expert": "x", "defaultAgent": "platform", "description": strings.Repeat("x", 301)}),
	}
	for name, extra := range bad {
		if _, err := Validate(bundle(t, expertFiles(t, extra)), "", Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The default lead title is "lead", so a role may not take it.
	if _, err := Validate(bundle(t, expertFiles(t, map[string]any{"team": map[string]any{"members": []map[string]any{role("lead", "x", "platform")}}})), "", Options{}); err == nil {
		t.Error("role named after the default lead accepted")
	}
}

func TestTeamOnlyOnExperts(t *testing.T) {
	files := map[string]string{
		ManifestFile:      string(manifestJSON(t, map[string]any{"kind": KindSkill, "name": "kit", "version": "1.0.0", "skills": []string{"review"}, "team": map[string]any{"members": []map[string]any{role("a", "x", "platform")}}})),
		"review/SKILL.md": reviewSkill,
	}
	if _, err := Validate(bundle(t, files), "", Options{}); err == nil {
		t.Fatal("a skill pack with a team accepted")
	}
	if _, _, err := Build(Form{Kind: "skill", Name: "pack", Skills: []FormSkill{{Name: "review", Description: "d", Body: "b"}}, Team: &TeamSpec{Members: []TeamMember{{Role: "a", Expert: "x", DefaultAgent: "platform"}}}}, "1.0.0", Options{}, nil); err == nil {
		t.Fatal("a skill pack form with a team accepted")
	}
}

func TestBuildTeamFromForm(t *testing.T) {
	f := Form{Kind: "expert", Name: "release-team", Instructions: "Plan, delegate, merge.",
		Team: &TeamSpec{Members: []TeamMember{{Role: "reviewer", Expert: "code-reviewer@^1", DefaultAgent: "requester"}}}}
	p, _, err := Build(f, "1.0.0", Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Team == nil || p.Manifest.Team.LeadRole() != DefaultLeadRole || p.Manifest.Team.Members[0].Role != "reviewer" {
		t.Fatalf("team = %+v", p.Manifest.Team)
	}
}

func TestParseDefaultAgent(t *testing.T) {
	cases := map[string]DefaultAgent{
		"requester":         {Kind: AgentRequester},
		"platform":          {Kind: AgentPlatform},
		"member:jack":       {Kind: AgentMember, Owner: "jack"},
		"member:jack/codex": {Kind: AgentMember, Owner: "jack", Provider: "codex"},
	}
	for in, want := range cases {
		got, err := ParseDefaultAgent(in)
		if err != nil || got != want {
			t.Errorf("%s = %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "me", "member:", "member:/codex", "member:jack/", "member:ja ck"} {
		if _, err := ParseDefaultAgent(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}
