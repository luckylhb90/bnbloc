package localskills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

const skillMD = "---\nname: %s\ndescription: Does %s things\n---\nBody\n"

func skill(t *testing.T, source, name string) string {
	t.Helper()
	dir := filepath.Join(source, name)
	write(t, filepath.Join(dir, "SKILL.md"), strings.NewReplacer("%s", name).Replace(skillMD), 0o644)
	return dir
}

func scanner(t *testing.T) *Scanner {
	t.Helper()
	sc, err := NewScanner(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestDefaultPolicyIsData(t *testing.T) {
	p := DefaultPolicy()
	if len(p.AgentDirs) == 0 || len(p.SecretRules) == 0 || p.Limits.Files == 0 || p.Limits.Bytes == 0 || p.Limits.MirrorBytes == 0 {
		t.Fatalf("the embedded policy is incomplete: %+v", p)
	}
	if _, err := NewScanner(p); err != nil {
		t.Fatalf("a default rule does not compile: %v", err)
	}
}

func TestPolicyFileAddsToTheDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	write(t, file, `{"agentDirs":["~/.mycli/skills","~/.claude/skills"],"exclude":["build"],"secretRules":[{"name":"a house token","pattern":"HOUSE-[0-9]{6}"}],"limits":{"files":10}}`, 0o600)
	p, err := LoadPolicy(file)
	if err != nil {
		t.Fatal(err)
	}
	base := DefaultPolicy()
	if !slices.Contains(p.AgentDirs, "~/.mycli/skills") || !slices.Contains(p.AgentDirs, "~/.agents/skills") {
		t.Errorf("agent folders are the default's plus the member's: %v", p.AgentDirs)
	}
	if len(p.AgentDirs) != len(base.AgentDirs)+1 {
		t.Errorf("a folder in both lists is once: %v", p.AgentDirs)
	}
	if !slices.Contains(p.Exclude, "build") || !slices.Contains(p.Exclude, ".git") {
		t.Errorf("excludes add: %v", p.Exclude)
	}
	if p.Limits.Files != 10 || p.Limits.Bytes != base.Limits.Bytes {
		t.Errorf("a non-zero limit replaces, a zero keeps: %+v", p.Limits)
	}
	if len(p.SecretRules) != len(base.SecretRules)+1 {
		t.Errorf("a rule adds: %d", len(p.SecretRules))
	}
	write(t, file, `{"secretRules":[{"name":"bad","pattern":"("}]}`, 0o600)
	if _, err := LoadPolicy(file); err == nil {
		t.Error("a rule that does not compile is refused, not ignored")
	}
	if _, err := LoadPolicy(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a policy file that is named but missing is an error")
	}
}

func TestInspectSaysWhyASkillCannotBeUsed(t *testing.T) {
	src := t.TempDir()
	sc := scanner(t)

	ok := skill(t, src, "good")
	write(t, filepath.Join(ok, "scripts", "run.py"), "print(1)\n", 0o644)
	write(t, filepath.Join(ok, "node_modules", "x", "index.js"), "ignored", 0o644)
	write(t, filepath.Join(ok, ".DS_Store"), "x", 0o644)
	got := sc.Inspect(src, ok)
	if !got.Eligible || got.Name != "good" || got.Description == "" {
		t.Fatalf("good: %+v", got)
	}
	if got.Files != 2 || !got.HasScripts || got.Hash == "" {
		t.Errorf("excluded names are not counted, a scripts folder is a script, a hash is set: %+v", got)
	}

	plain := skill(t, src, "plain")
	if sc.Inspect(src, plain).HasScripts {
		t.Error("a skill with only SKILL.md has no scripts")
	}

	empty := filepath.Join(src, "empty")
	write(t, filepath.Join(empty, "README.md"), "x", 0o644)
	if got := sc.Inspect(src, empty); got.Eligible || got.Reason != "no SKILL.md" {
		t.Errorf("empty: %+v", got)
	}

	nodesc := filepath.Join(src, "nodesc")
	write(t, filepath.Join(nodesc, "SKILL.md"), "---\nname: nodesc\n---\nx", 0o644)
	if got := sc.Inspect(src, nodesc); got.Eligible || !strings.Contains(got.Reason, "no description") {
		t.Errorf("nodesc: %+v", got)
	}

	badname := filepath.Join(src, "badname")
	write(t, filepath.Join(badname, "SKILL.md"), "---\nname: Bad Name\ndescription: d\n---\nx", 0o644)
	if got := sc.Inspect(src, badname); got.Eligible || !strings.Contains(got.Reason, "not lower-case") {
		t.Errorf("badname: %+v", got)
	}

	out := t.TempDir()
	write(t, filepath.Join(out, "secret.txt"), "outside", 0o644)
	escape := skill(t, src, "escape")
	if err := os.Symlink(filepath.Join(out, "secret.txt"), filepath.Join(escape, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if got := sc.Inspect(src, escape); got.Eligible || !strings.Contains(got.Reason, "leaves the skill's folder") {
		t.Errorf("a link out of the folder: %+v", got)
	}

	inner := skill(t, src, "inner")
	write(t, filepath.Join(inner, "docs", "a.md"), "doc", 0o644)
	if err := os.Symlink("docs/a.md", filepath.Join(inner, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if got := sc.Inspect(src, inner); !got.Eligible {
		t.Errorf("a link to a file inside the folder is fine: %+v", got)
	}

	dirlink := skill(t, src, "dirlink")
	write(t, filepath.Join(dirlink, "docs", "a.md"), "doc", 0o644)
	if err := os.Symlink("docs", filepath.Join(dirlink, "more")); err != nil {
		t.Fatal(err)
	}
	if got := sc.Inspect(src, dirlink); got.Eligible || !strings.Contains(got.Reason, "link to a folder") {
		t.Errorf("a link to a folder: %+v", got)
	}
}

func TestInspectStopsAtACredential(t *testing.T) {
	src := t.TempDir()
	sc := scanner(t)
	cases := map[string]string{
		".env":                    "X=1",
		"deploy.pem":              "x",
		"notes.txt":               "the key is AKIAABCDEFGHIJKLMNOP ok",
		"conf.yaml":               "password: hunter2hunter2hunter2",
		"key.txt":                 "-----BEGIN RSA PRIVATE KEY-----\nabc",
		"token.txt":               "ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"nested/credentials.json": "{}",
	}
	for file, body := range cases {
		dir := skill(t, src, "s")
		write(t, filepath.Join(dir, file), body, 0o644)
		got := sc.Inspect(src, dir)
		if got.Eligible || !strings.Contains(got.Reason, filepath.Base(file)) {
			t.Errorf("%s: %+v", file, got)
		}
		os.RemoveAll(dir)
	}
	dir := skill(t, src, "clean")
	write(t, filepath.Join(dir, "image.bin"), "AKIAABCDEFGHIJKLMNOP\x00binary", 0o644)
	write(t, filepath.Join(dir, "doc.md"), "Set PASSWORD to something long and random, never commit it.", 0o644)
	if got := sc.Inspect(src, dir); !got.Eligible {
		t.Errorf("a binary file is not read, and prose that mentions a password is not one: %+v", got)
	}
}

func TestInspectLimits(t *testing.T) {
	p := DefaultPolicy()
	p.Limits.Files, p.Limits.Bytes = 3, 100
	sc, _ := NewScanner(p)
	src := t.TempDir()
	many := skill(t, src, "many")
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(many, n), "x", 0o644)
	}
	if got := sc.Inspect(src, many); got.Eligible || !strings.Contains(got.Reason, "more than 3 files") {
		t.Errorf("many: %+v", got)
	}
	big := skill(t, src, "big")
	write(t, filepath.Join(big, "blob"), strings.Repeat("x", 200), 0o644)
	if got := sc.Inspect(src, big); got.Eligible || !strings.Contains(got.Reason, "MiB") {
		t.Errorf("big: %+v", got)
	}
}

func TestHashIsTheSkill(t *testing.T) {
	src := t.TempDir()
	sc := scanner(t)
	dir := skill(t, src, "h")
	a := sc.Inspect(src, dir).Hash
	if b := sc.Inspect(src, dir).Hash; a != b {
		t.Error("the same skill hashes the same")
	}
	write(t, filepath.Join(dir, "extra.md"), "x", 0o644)
	if b := sc.Inspect(src, dir).Hash; a == b {
		t.Error("an added file changes the hash")
	}
}

func TestScanSourceFindsSkillsAtDepthAndIsBounded(t *testing.T) {
	src := t.TempDir()
	skill(t, src, "top")
	skill(t, filepath.Join(src, "group"), "grouped")
	skill(t, filepath.Join(src, "a", "b"), "toodeep")
	os.MkdirAll(filepath.Join(src, "node_modules", "n"), 0o755)
	write(t, filepath.Join(src, "node_modules", "n", "SKILL.md"), "---\nname: n\ndescription: d\n---", 0o644)
	got, truncated, err := scanner(t).ScanSource(src)
	if err != nil || truncated {
		t.Fatal(err, truncated)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, []string{"grouped", "top"}) {
		t.Errorf("two levels down, never an excluded folder: %v", names)
	}
	p := DefaultPolicy()
	p.Limits.ScanEntries = 1
	small, _ := NewScanner(p)
	if _, truncated, _ := small.ScanSource(src); !truncated {
		t.Error("a source past the entry limit is cut and says so")
	}
	if _, _, err := scanner(t).ScanSource(filepath.Join(src, "missing")); err == nil {
		t.Error("a source that is not there is an error")
	}
}

func TestMirrorSyncIsIncrementalAndPrunes(t *testing.T) {
	src := t.TempDir()
	sc := scanner(t)
	dir := skill(t, src, "m")
	write(t, filepath.Join(dir, "scripts", "run.sh"), "#!/bin/sh\n", 0o755)
	root := t.TempDir()
	mirror := Mirror{Root: root}
	files, err := sc.Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := mirror.Sync("m", files); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(root, "m", "scripts", "run.sh")
	st, err := os.Stat(run)
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("a script keeps its executable bit: %v %v", st, err)
	}
	// An unchanged copy is left alone (same inode content, same time).
	before, _ := os.Stat(filepath.Join(root, "m", "SKILL.md"))
	time.Sleep(10 * time.Millisecond)
	if err := mirror.Sync("m", files); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(root, "m", "SKILL.md"))
	if !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(files[0].ModTime) {
		t.Error("an unchanged file keeps the source's modification time")
	}
	// An edit is copied, a removed file and its folder go.
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: m\ndescription: changed\n---\n", 0o644)
	os.Remove(filepath.Join(dir, "scripts", "run.sh"))
	os.Remove(filepath.Join(dir, "scripts"))
	files, _ = sc.Files(dir)
	if err := mirror.Sync("m", files); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "m", "SKILL.md"))
	if !strings.Contains(string(got), "changed") {
		t.Error("an edited file is copied again")
	}
	if _, err := os.Stat(filepath.Join(root, "m", "scripts")); !os.IsNotExist(err) {
		t.Error("a folder the skill no longer has is removed")
	}
	// Prune keeps only what is named.
	os.MkdirAll(filepath.Join(root, "other"), 0o755)
	if err := mirror.Prune([]string{"m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "other")); !os.IsNotExist(err) {
		t.Error("a copy that is not kept is removed")
	}
	if _, err := os.Stat(filepath.Join(root, "m")); err != nil {
		t.Error("a copy that is kept stays")
	}
	if err := (Mirror{Root: filepath.Join(root, "nowhere")}).Prune(nil); err != nil {
		t.Errorf("pruning a mirror that does not exist is nothing: %v", err)
	}
}

func TestReachPathsFollowLinksToTheSkill(t *testing.T) {
	home := t.TempDir()
	canon := filepath.Join(home, ".agents", "skills")
	sk := Skill{Name: "demo", Dir: skill(t, canon, "demo")}
	// One agent links to it, one holds a different skill of the same name, one is not there.
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	if err := os.Symlink(filepath.Join(canon, "demo"), filepath.Join(home, ".claude", "skills", "demo")); err != nil {
		t.Fatal(err)
	}
	skill(t, filepath.Join(home, ".codex", "skills"), "demo")
	got := ReachPaths(sk, []string{"~/.agents/skills", "~/.claude/skills", "~/.codex/skills", "~/.gemini/skills"}, home)
	want := []string{sk.Dir, filepath.Join(home, ".claude", "skills", "demo")}
	if !slices.Equal(got, want) {
		t.Errorf("the skill and the link to it, nothing else:\n got %v\nwant %v", got, want)
	}
	if got := ReachPaths(Skill{Name: "gone", Dir: filepath.Join(home, "nowhere")}, nil, home); len(got) != 1 {
		t.Errorf("a skill whose folder is gone is only its own path: %v", got)
	}
	if ExpandHome("~", "/h") != "/h" || ExpandHome("~/x", "/h") != "/h/x" || ExpandHome("/a", "/h") != "/a" {
		t.Error("home expansion")
	}
}

func TestSettingsRoundTripAndDefaultOff(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sub", "local-skills.json")
	s, err := LoadSettings(file)
	if err != nil || s.Enabled || len(s.Sources) != 0 || len(s.Skills) != 0 {
		t.Fatalf("no file is off, nothing ticked: %+v %v", s, err)
	}
	s = Settings{Enabled: true, Sources: []string{"~/.agents/skills"}, Skills: map[string]Choice{"a": {VM: true}}}
	if err := s.Save(file); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("settings are owner-only: %v", st.Mode())
	}
	back, err := LoadSettings(file)
	if err != nil || !back.Enabled || !back.Skills["a"].VM || back.Skills["a"].Borrowers {
		t.Errorf("round trip: %+v %v", back, err)
	}
	write(t, file, "{", 0o600)
	if _, err := LoadSettings(file); err == nil {
		t.Error("a settings file that does not parse is an error, not 'off'")
	}
}

func TestBuildPlan(t *testing.T) {
	home := t.TempDir()
	canon := filepath.Join(home, ".agents", "skills")
	skill(t, canon, "own")
	skill(t, canon, "shared")
	skill(t, canon, "dirty")
	write(t, filepath.Join(canon, "dirty", ".env"), "X=1", 0o644)
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	os.Symlink(filepath.Join(canon, "shared"), filepath.Join(home, ".claude", "skills", "shared"))
	in := PlanInput{
		Settings: Settings{Enabled: true, Sources: []string{"~/.agents/skills"}, Skills: map[string]Choice{
			"own":      {VM: true},
			"shared":   {VM: true, Borrowers: true},
			"dirty":    {VM: true},
			"missing":  {VM: true},
			"unticked": {VM: false},
		}},
		Policy:     DefaultPolicy(),
		Home:       home,
		MirrorRoot: filepath.Join(home, "mirror"),
	}
	hosts := func(p Plan) (m []string) {
		for _, x := range p.Mounts {
			m = append(m, filepath.Base(x.Host)+"@"+strings.TrimPrefix(x.Guest, home))
		}
		return
	}

	own, err := BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	wantOwn := []string{"own@/.agents/skills/own", "shared@/.agents/skills/shared", "shared@/.claude/skills/shared"}
	if !slices.Equal(hosts(own), wantOwn) {
		t.Errorf("own run: the ticked skills, each at every path it is reached by:\n got %v\nwant %v", hosts(own), wantOwn)
	}
	if len(own.Notes) != 2 || !strings.Contains(strings.Join(own.Notes, "\n"), ".env") || !strings.Contains(strings.Join(own.Notes, "\n"), "missing") {
		t.Errorf("a credential file and a skill that is not found are said: %v", own.Notes)
	}
	if _, err := os.Stat(filepath.Join(home, "mirror", "own", "SKILL.md")); err != nil {
		t.Error("the mirror holds the copy")
	}
	if _, err := os.Stat(filepath.Join(home, "mirror", "dirty")); !os.IsNotExist(err) {
		t.Error("a skill that stopped at a credential is not copied at all")
	}

	in.Borrowed = true
	borrowed, _ := BuildPlan(in)
	if !slices.Equal(hosts(borrowed), []string{"shared@/.agents/skills/shared", "shared@/.claude/skills/shared"}) {
		t.Errorf("a borrowed run gets only the skills ticked for borrowers: %v", hosts(borrowed))
	}
	if _, err := os.Stat(filepath.Join(home, "mirror", "own")); err != nil {
		t.Error("a borrowed turn does not remove the copies an own turn uses")
	}

	in.Settings.Skills["own"] = Choice{}
	in.Borrowed = false
	BuildPlan(in)
	if _, err := os.Stat(filepath.Join(home, "mirror", "own")); !os.IsNotExist(err) {
		t.Error("a skill that is no longer ticked loses its copy")
	}

	in.Settings.Enabled = false
	off, _ := BuildPlan(in)
	if len(off.Mounts) != 0 || len(off.Notes) != 0 {
		t.Errorf("off is nothing: %+v", off)
	}
	if _, err := os.Stat(filepath.Join(home, "mirror", "shared")); err != nil {
		t.Error("off touches nothing (the mirror is left as it was)")
	}
}

func TestBuildPlanLimits(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "skills")
	for _, n := range []string{"a", "b", "c"} {
		d := skill(t, src, n)
		write(t, filepath.Join(d, "blob"), strings.Repeat("x", 500), 0o644)
	}
	p := DefaultPolicy()
	p.Limits.Skills = 2
	in := PlanInput{Settings: Settings{Enabled: true, Sources: []string{src}, Skills: map[string]Choice{"a": {VM: true}, "b": {VM: true}, "c": {VM: true}}}, Policy: p, Home: home, MirrorRoot: filepath.Join(home, "m")}
	plan, _ := BuildPlan(in)
	if len(plan.Mounts) != 2 || len(plan.Notes) != 1 || !strings.Contains(plan.Notes[0], "at most 2 skills") {
		t.Errorf("past the skill limit: %+v", plan)
	}
	p.Limits.Skills = 64
	p.Limits.MirrorBytes = 700
	in.Policy = p
	plan, _ = BuildPlan(in)
	if len(plan.Mounts) != 1 || len(plan.Notes) != 2 || !strings.Contains(plan.Notes[0], "copies would pass") {
		t.Errorf("past the mirror limit: %+v", plan)
	}
	in.Settings.Sources = []string{filepath.Join(home, "nowhere"), src}
	plan, _ = BuildPlan(in)
	if !strings.Contains(strings.Join(plan.Notes, "\n"), "cannot be read") {
		t.Errorf("a source that cannot be read is said, the others still used: %+v", plan)
	}
}
