package localskills

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

func newService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	return &Service{
		Home:         home,
		SettingsFile: filepath.Join(home, "data", "local-skills.json"),
		MountsFile:   filepath.Join(home, "bridge", "mounts", "local-skills", "mounts.json"),
		MirrorRoot:   filepath.Join(home, "workspaces", ".mounts", "local-skills"),
	}, home
}

func readDescriptor(t *testing.T, s *Service) (d struct {
	Contract string            `json:"contract"`
	Mounts   []descriptorMount `json:"mounts"`
}) {
	t.Helper()
	raw, err := os.ReadFile(s.MountsFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestServiceStateOffByDefaultAndSuggestsTheCanonicalFolder(t *testing.T) {
	s, home := newService(t)
	st, err := s.State()
	if err != nil || st.Enabled || len(st.Sources) != 0 || len(st.Skills) != 0 || len(st.Suggested) != 0 {
		t.Fatalf("nothing is on or listed: %+v %v", st, err)
	}
	skill(t, filepath.Join(home, ".agents", "skills"), "alpha")
	st, _ = s.State()
	if len(st.Suggested) != 1 || st.Suggested[0] != "~/.agents/skills" || len(st.Skills) != 0 {
		t.Fatalf("the folder is offered, not added: %+v", st)
	}
}

func TestServiceApplyPublishesTheDescriptor(t *testing.T) {
	s, home := newService(t)
	canon := filepath.Join(home, ".agents", "skills")
	skill(t, canon, "alpha")
	skill(t, canon, "beta")
	st, err := s.Apply(Settings{Enabled: true, Sources: []string{"~/.agents/skills"}, Skills: map[string]Choice{
		"alpha": {VM: true},
		"beta":  {VM: true, Borrowers: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Skills) != 2 || len(st.Suggested) != 0 {
		t.Fatalf("both skills listed, the added folder no longer suggested: %+v", st)
	}
	d := readDescriptor(t, s)
	if d.Contract != MountsContract {
		t.Errorf("contract: %q", d.Contract)
	}
	kinds := map[string]string{}
	for _, m := range d.Mounts {
		kinds[filepath.Base(m.Host)] = m.For
		if !strings.HasPrefix(m.Host, s.MirrorRoot) {
			t.Errorf("a mount's host side is in the mirror, never the member's own folder: %s", m.Host)
		}
	}
	if kinds["alpha"] != "own" || kinds["beta"] != "all" {
		t.Errorf("own runs get both, borrowed runs only beta: %v", kinds)
	}

	// Off takes every mount out of the descriptor.
	if _, err := s.Apply(Settings{Sources: []string{"~/.agents/skills"}}); err != nil {
		t.Fatal(err)
	}
	if d := readDescriptor(t, s); len(d.Mounts) != 0 {
		t.Errorf("off publishes nothing: %+v", d.Mounts)
	}
}

func TestServiceShadowedAndChangedSkills(t *testing.T) {
	s, home := newService(t)
	a, b := filepath.Join(home, "a"), filepath.Join(home, "b")
	dir := skill(t, a, "same")
	skill(t, b, "same")
	skill(t, b, "other")
	st, err := s.Apply(Settings{Sources: []string{a, b}, Skills: map[string]Choice{"same": {VM: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var shadow *SkillState
	for i := range st.Skills {
		if st.Skills[i].Name == "same" && st.Skills[i].HiddenBy != "" {
			shadow = &st.Skills[i]
		}
	}
	if shadow == nil || shadow.HiddenBy != a || shadow.Choice.VM {
		t.Fatalf("the later folder's skill of the same name is hidden and carries no choice: %+v", st.Skills)
	}

	var hash string
	for _, sk := range st.Skills {
		if sk.Name == "same" && sk.HiddenBy == "" {
			hash = sk.Hash
		}
	}
	_, err = s.Apply(Settings{Sources: []string{a}, Skills: map[string]Choice{"same": {Published: &Published{Hash: hash, ComponentID: "x", Version: "1.0.0"}}}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "extra.md"), "more", 0o644)
	st, _ = s.State()
	if len(st.Skills) != 1 || !st.Skills[0].Changed {
		t.Errorf("a skill edited after it was published says so: %+v", st.Skills)
	}
}

func TestServiceValidate(t *testing.T) {
	s, _ := newService(t)
	bad := []Settings{
		{Sources: []string{"relative/dir"}},
		{AgentDirs: []string{""}},
		{PolicyFile: "policy.json"},
		{Skills: map[string]Choice{"../x": {}}},
		{Skills: map[string]Choice{"": {}}},
		{Sources: make([]string, maxSources+1)},
	}
	for i, in := range bad {
		if _, err := s.Apply(in); err == nil {
			t.Errorf("case %d: %+v should be refused", i, in)
		}
	}
	if _, err := os.Stat(s.SettingsFile); !os.IsNotExist(err) {
		t.Error("a refused update saves nothing")
	}
}

func TestServicePackFiltersAndRefuses(t *testing.T) {
	s, home := newService(t)
	src := filepath.Join(home, "src")
	dir := skill(t, src, "alpha")
	write(t, filepath.Join(dir, "scripts", "run.sh"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(dir, ".git", "config"), "x", 0o644)
	skill(t, src, "dirty")
	write(t, filepath.Join(src, "dirty", ".env"), "K=1", 0o644)
	if _, err := s.Apply(Settings{Sources: []string{src}}); err != nil {
		t.Fatal(err)
	}
	archive, hash, err := s.Pack("alpha")
	if err != nil || hash == "" {
		t.Fatalf("pack: %v", err)
	}
	got, err := tarbundle.ReadArchive(archive, tarbundle.ContentLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["alpha/SKILL.md"]; !ok || string(got["alpha/scripts/run.sh"]) != "#!/bin/sh\n" {
		t.Errorf("the skill and its script are packed: %v", got)
	}
	for name := range got {
		if strings.Contains(name, ".git") {
			t.Errorf("a file the policy leaves out is not packed: %s", name)
		}
	}
	if _, _, err := s.Pack("dirty"); err == nil {
		t.Error("a skill with a credential file is never packed")
	}
	skill(t, src, "spacey")
	write(t, filepath.Join(src, "spacey", "My Notes.md"), "x", 0o644)
	st, _ := s.Apply(Settings{Sources: []string{src}})
	for _, sk := range st.Skills {
		if sk.Name == "spacey" && (!sk.Eligible || sk.PublishProblem == "") {
			t.Errorf("a file name the library refuses blocks only the publish: %+v", sk)
		}
	}
	if _, _, err := s.Pack("spacey"); err == nil || !strings.Contains(err.Error(), "My Notes.md") {
		t.Errorf("packing it names the file: %v", err)
	}
	if _, _, err := s.Pack("nope"); err == nil {
		t.Error("an unknown skill is an error")
	}
}

func TestProviderAPI(t *testing.T) {
	s, home := newService(t)
	skill(t, filepath.Join(home, "src"), "alpha")
	srv := httptest.NewServer(s.Handler("secret"))
	defer srv.Close()
	do := func(method, path, token, body string) (*http.Response, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, raw
	}
	if res, _ := do("GET", "/v1/state", "", ""); res.StatusCode != 401 {
		t.Errorf("no bearer: %d", res.StatusCode)
	}
	if res, _ := do("GET", "/v1/state", "wrong", ""); res.StatusCode != 401 {
		t.Errorf("wrong bearer: %d", res.StatusCode)
	}
	res, raw := do("GET", "/v1/state", "secret", "")
	if res.StatusCode != 200 || !strings.Contains(string(raw), ProviderContract) {
		t.Fatalf("state: %d %s", res.StatusCode, raw)
	}
	body, _ := json.Marshal(Settings{Enabled: true, Sources: []string{filepath.Join(home, "src")}, Skills: map[string]Choice{"alpha": {VM: true}}})
	if res, raw := do("PUT", "/v1/settings", "secret", string(body)); res.StatusCode != 200 || !strings.Contains(string(raw), `"alpha"`) {
		t.Fatalf("settings: %d %s", res.StatusCode, raw)
	}
	if res, _ := do("PUT", "/v1/settings", "secret", `{"enabled":`); res.StatusCode != 400 {
		t.Errorf("malformed json is refused: %d", res.StatusCode)
	}
	if res, _ := do("PUT", "/v1/settings", "secret", `{"sources":["rel"]}`); res.StatusCode != 400 {
		t.Errorf("an invalid update is refused: %d", res.StatusCode)
	}
	if res, _ := do("PUT", "/v1/settings", "secret", strings.Repeat(" ", maxSettingsBody+10)); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized update is refused: %d", res.StatusCode)
	}
	res, raw = do("POST", "/v1/pack?name=alpha", "secret", "")
	if res.StatusCode != 200 || res.Header.Get("X-Skill-Hash") == "" || len(raw) == 0 || res.Header.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("pack: %d", res.StatusCode)
	}
	if res, _ := do("POST", "/v1/pack?name=nope", "secret", ""); res.StatusCode != 422 {
		t.Errorf("unknown skill: %d", res.StatusCode)
	}
}

func TestServiceWithoutMountsFolderPublishesNothing(t *testing.T) {
	s, home := newService(t)
	s.MountsFile, s.MirrorRoot = "", ""
	skill(t, filepath.Join(home, "src"), "alpha")
	st, err := s.Apply(Settings{Enabled: true, Sources: []string{filepath.Join(home, "src")}, Skills: map[string]Choice{"alpha": {VM: true}}})
	if err != nil || len(st.Notes) == 0 {
		t.Fatalf("it says it publishes no mounts: %+v %v", st.Notes, err)
	}
}

// A skill over the library's size is still usable in the sandbox (the policy's
// limits are larger); it only cannot be published, and is not hashed whole.
func TestBigSkillIsForTheSandboxNotTheLibrary(t *testing.T) {
	s, home := newService(t)
	src := filepath.Join(home, "src")
	dir := skill(t, src, "archify")
	blob := make([]byte, 9<<20)
	for i := range blob {
		blob[i] = byte(i%251) + 1
	}
	if err := os.WriteFile(filepath.Join(dir, "assets.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := s.Apply(Settings{Enabled: true, Sources: []string{src}, Skills: map[string]Choice{"archify": {VM: true}}})
	if err != nil {
		t.Fatal(err)
	}
	row := st.Skills[0]
	if !row.Eligible || row.Reason != "" {
		t.Fatalf("a 9 MiB skill reaches the sandbox: %+v", row)
	}
	if !strings.Contains(row.PublishProblem, "too large for the library") || row.Hash != "" {
		t.Fatalf("it cannot be published, and is not hashed: %+v", row)
	}
	if _, _, err := s.Pack("archify"); err == nil {
		t.Fatal("a skill over the library's size is not packed")
	}
	d := readDescriptor(t, s)
	if len(d.Mounts) == 0 {
		t.Fatal("and it is mounted")
	}
	if _, err := os.Stat(filepath.Join(s.MirrorRoot, "archify", "assets.bin")); err != nil {
		t.Fatalf("the copy is whole: %v", err)
	}
}

// A fixture's placeholder is not a credential; a real-looking one is, and the
// member may allow a content match (for the sandbox only) until the file changes.
func TestCredentialPlaceholdersAndAllowances(t *testing.T) {
	s, home := newService(t)
	src := filepath.Join(home, "src")
	fake := skill(t, src, "fixture")
	write(t, filepath.Join(fake, "test", "evidence.test.mjs"), "set('https://x-access-token:not-a-real-token@github.com/example/repo.git')\n", 0o644)
	real := skill(t, src, "flagged")
	realFile := filepath.Join(real, "notes.md")
	write(t, realFile, "api_key = \"q8W2k9ZxP4mN7vB3cL6tR1yU5\"\n", 0o644)
	named := skill(t, src, "named")
	write(t, filepath.Join(named, ".env"), "A=1\n", 0o644)

	find := func(st State, name string) SkillState {
		for _, sk := range st.Skills {
			if sk.Name == name {
				return sk
			}
		}
		t.Fatalf("no %s in %+v", name, st.Skills)
		return SkillState{}
	}
	st, err := s.Apply(Settings{Sources: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	if sk := find(st, "fixture"); !sk.Eligible {
		t.Fatalf("a placeholder is not a credential: %+v", sk)
	}
	flagged := find(st, "flagged")
	if flagged.Eligible || flagged.Finding == nil || !flagged.Finding.Allowable || flagged.Finding.File != "notes.md" || len(flagged.Finding.Hash) != 64 {
		t.Fatalf("a real-looking key stops the skill, and may be allowed: %+v", flagged)
	}
	if n := find(st, "named"); n.Eligible || n.Finding == nil || n.Finding.Allowable {
		t.Fatalf("a .env is never allowable: %+v", n)
	}

	allow := Settings{Enabled: true, Sources: []string{src}, Skills: map[string]Choice{"flagged": {VM: true, Allowed: []AllowedFile{{File: "notes.md", Hash: flagged.Finding.Hash}}}}}
	if st, err = s.Apply(allow); err != nil {
		t.Fatal(err)
	}
	ok := find(st, "flagged")
	if !ok.Eligible || len(ok.Allowed) != 1 || ok.PublishProblem == "" {
		t.Fatalf("allowed for the sandbox, never for the library: %+v", ok)
	}
	if _, _, err := s.Pack("flagged"); err == nil {
		t.Fatal("an allowed file is not packed for the library")
	}
	if _, err := os.Stat(filepath.Join(s.MirrorRoot, "flagged", "notes.md")); err != nil {
		t.Fatalf("it is mirrored: %v", err)
	}

	write(t, realFile, "api_key = \"Z1x2C3v4B5n6M7a8S9d0F1g2H\"\n", 0o644)
	st, _ = s.State()
	if find(st, "flagged").Eligible {
		t.Fatal("an edited file is checked again")
	}
	if _, err := s.Apply(Settings{Skills: map[string]Choice{"x": {Allowed: []AllowedFile{{File: "../up", Hash: strings.Repeat("a", 64)}}}}}); err == nil {
		t.Error("an allowance outside the skill is refused")
	}
}
