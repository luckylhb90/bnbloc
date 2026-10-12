package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// contentFixtureGateway serves what a content-bearing drive fetches: the
// job's effective content, its bundles (only under the job's grant), the
// room context routes, job status reports and notices.
type contentFixtureGateway struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	effective map[string]wire.ContentEffective // job id -> content
	bundles   map[string][]byte                // bundle key -> archive
	statuses  []map[string]any
	notices   []string
}

func newContentFixtureGateway(t *testing.T) *contentFixtureGateway {
	t.Helper()
	g := &contentFixtureGateway{t: t, effective: map[string]wire.ContentEffective{}, bundles: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/content-components/effective", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		eff, ok := g.effective[r.URL.Query().Get("job")]
		g.mu.Unlock()
		if !ok || r.URL.Query().Get("room") != "r1" {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(eff)
	})
	mux.HandleFunc("GET /v1/artifacts/{key...}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		raw, ok := g.bundles[r.PathValue("key")]
		g.mu.Unlock()
		if !ok || r.URL.Query().Get("job") == "" || r.URL.Query().Get("room") != "r1" {
			http.Error(w, "artifact not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("GET /v1/rooms/r1/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"channels":[{"channelId":"general"}]}`)
	})
	mux.HandleFunc("GET /v1/rooms/r1/history", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"messages":[]}`)
	})
	mux.HandleFunc("GET /v1/rooms/r1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"artifacts":[]}`)
	})
	mux.HandleFunc("POST /v1/rooms/r1/agent-activity/session-state", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("POST /v1/rooms/r1/agent-activity/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Payload map[string]any `json:"payload"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		for _, m := range body.Messages {
			if text, _ := m.Payload["text"].(string); text != "" {
				g.notices = append(g.notices, text)
			}
		}
		g.mu.Unlock()
	})
	mux.HandleFunc("POST /v1/bridge/jobs/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.statuses = append(g.statuses, body)
		g.mu.Unlock()
	})
	mux.HandleFunc("GET /v1/my/tenant-policy", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"requireSandboxForBorrow":false}`)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// pkg builds a package with the shared builder and serves its bundle.
func (g *contentFixtureGateway) pkg(form contentpkg.Form, version string) wire.ContentRef {
	g.t.Helper()
	p, raw, err := contentpkg.Build(form, version, contentpkg.Options{}, nil)
	if err != nil {
		g.t.Fatal(err)
	}
	key := "sha256/" + p.SHA256
	g.mu.Lock()
	g.bundles[key] = raw
	g.mu.Unlock()
	return wire.ContentRef{ComponentID: "cc_" + form.Name, Kind: form.Kind, Name: form.Name, DisplayName: form.DisplayName, Version: version,
		SHA256: p.SHA256, BundleKey: key, Owner: "maya", Permissions: p.Manifest.Permissions(), NeedsSandbox: p.Manifest.NeedsSandbox()}
}

func (g *contentFixtureGateway) setEffective(job string, e wire.ContentEffective) {
	g.mu.Lock()
	g.effective[job] = e
	g.mu.Unlock()
}

func (g *contentFixtureGateway) lastStatus() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.statuses) == 0 {
		return nil
	}
	return g.statuses[len(g.statuses)-1]
}

func (g *contentFixtureGateway) allNotices() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.notices, "\n")
}

func reviewerForm(instructions string) contentpkg.Form {
	return contentpkg.Form{Kind: "expert", Name: "reviewer", DisplayName: "Reviewer", Instructions: instructions,
		Skills: []contentpkg.FormSkill{{Name: "checklist", Description: "Use when reviewing a diff", Body: "1. Run the tests."}}}
}

func lintPackForm() contentpkg.Form {
	return contentpkg.Form{Kind: "skill", Name: "lint-kit", Skills: []contentpkg.FormSkill{{Name: "lint", Description: "Use when asked to lint", Body: "Run the linter."}}}
}

// bytesProfileProv is a profileProv that also accepts binary workspace
// files (the fallback writes skill files through it).
type bytesProfileProv struct {
	profileProv
	bmu    sync.Mutex
	binary map[string]string
}

func (p *bytesProfileProv) WriteWorkspaceFileBytes(_ context.Context, _ corepkg.Endpoint, _, relPath string, content []byte) error {
	if !corepkg.WorkspaceFileAllowed(relPath) {
		return fmt.Errorf("path %q not allowed", relPath)
	}
	p.bmu.Lock()
	if p.binary == nil {
		p.binary = map[string]string{}
	}
	p.binary[relPath] = string(content)
	p.bmu.Unlock()
	return nil
}

func contentJob(id, expertID string) bridgeJob {
	return bridgeJob{JobID: id, RoomID: "r1", ChannelID: "general", RequesterUserID: "maya", OwnerUserID: "maya", Core: "core-a", Provider: "agent-a", MessageText: "review my change", ExpertID: expertID}
}

func contentBridge(g *contentFixtureGateway, prov corepkg.Provider) *Bridge {
	return New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya",
		Cores: []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}}})
}

// An agentProfile core receives the expert's instructions and every skill
// in effect (the expert's own and the packs'), verified from their bundles;
// the run report names the expert.
func TestContentProfileReachesProfileCore(t *testing.T) {
	g := newContentFixtureGateway(t)
	expert := g.pkg(reviewerForm("You are the reviewer. MARKER-ALPHA"), "1.2.0")
	expert.Source = wire.ContentSourceExpert
	pack := g.pkg(lintPackForm(), "1.0.0")
	pack.Source = wire.ContentSourceRoom
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, SkillPacks: []wire.ContentRef{pack}})
	prov := &profileProv{}
	b := contentBridge(g, prov)

	b.runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))

	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil {
		t.Fatalf("profiles = %+v", ps)
	}
	p := ps[0]
	if strings.TrimSpace(p.Instructions) != "You are the reviewer. MARKER-ALPHA" {
		t.Fatalf("instructions = %q", p.Instructions)
	}
	var names []string
	for _, s := range p.Skills {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "checklist,lint" || !strings.Contains(string(p.Skills[0].Files[0].Content), "Run the tests") {
		t.Fatalf("skills = %+v", p.Skills)
	}
	// No prompt prefix for a core that wears the profile natively.
	if prompts := prov.recordedPrompts(); len(prompts) != 1 || strings.Contains(prompts[0], "MARKER-ALPHA") {
		t.Fatalf("prompts = %q", prompts)
	}
	var running map[string]any
	g.mu.Lock()
	for _, st := range g.statuses {
		if st["status"] == "running" {
			running, _ = st["diagnostics"].(map[string]any)
		}
	}
	g.mu.Unlock()
	if running["expert"] != "Reviewer (v1.2.0)" {
		t.Fatalf("running diagnostics = %+v", running)
	}
}

// A core without agentProfile gets the fallback: the instructions and the
// skill summary prefixed to the first prompt, the skill files written under
// .agents/skills/ (a core that cannot take files gets the bodies inlined).
func TestContentFallbackPrefixesThePrompt(t *testing.T) {
	g := newContentFixtureGateway(t)
	expert := g.pkg(reviewerForm("You are the reviewer. MARKER-ALPHA"), "1.0.0")
	expert.Source = wire.ContentSourceExpert
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, SkillPacks: []wire.ContentRef{}})
	prov := &bytesProfileProv{profileProv: profileProv{noProfile: true}}
	b := contentBridge(g, prov)

	b.runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))

	prompts := prov.recordedPrompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %q", prompts)
	}
	pr := prompts[0]
	if !strings.HasPrefix(pr, "[Expert: Reviewer (v1.0.0).") || !strings.Contains(pr, "MARKER-ALPHA") || !strings.Contains(pr, ".agents/skills/<name>/") ||
		!strings.Contains(pr, "- checklist: Use when reviewing a diff") || !strings.Contains(pr, "review my change") {
		t.Fatalf("prompt = %s", pr)
	}
	if ps := prov.recordedProfiles(); len(ps) != 1 || ps[0] != nil {
		t.Fatalf("a core without agentProfile got a profile: %+v", ps)
	}
	prov.bmu.Lock()
	skill := prov.binary[".agents/skills/checklist/SKILL.md"]
	prov.bmu.Unlock()
	if !strings.Contains(skill, "name: checklist") {
		t.Fatalf("skill files = %+v", prov.binary)
	}

	// A core without workspace files gets the skill bodies inline.
	inline := (&jobContent{instructions: "x", summaries: []skillSummary{{name: "checklist", description: "d", body: "Run the tests."}}, skills: []corepkg.ProfileSkill{{Name: "checklist"}}}).promptPrefix(false)
	if !strings.Contains(inline, "## Skill checklist") || !strings.Contains(inline, "Run the tests.") {
		t.Fatalf("inline prefix = %s", inline)
	}
}

// A session is reused while the content stays the same and replaced when it
// changes (a new expert version re-materializes the profile).
func TestContentChangeStartsANewSession(t *testing.T) {
	g := newContentFixtureGateway(t)
	v1 := g.pkg(reviewerForm("v1 rules"), "1.0.0")
	v1.Source = wire.ContentSourceExpert
	v2 := g.pkg(reviewerForm("v2 rules"), "1.1.0")
	v2.Source = wire.ContentSourceExpert
	g.setEffective("job-1", wire.ContentEffective{Expert: &v1})
	g.setEffective("job-2", wire.ContentEffective{Expert: &v1})
	g.setEffective("job-3", wire.ContentEffective{Expert: &v2})
	g.setEffective("job-4", wire.ContentEffective{})
	prov := &profileProv{}
	b := contentBridge(g, prov)
	for _, id := range []string{"job-1", "job-2", "job-3", "job-4"} {
		j := contentJob(id, v1.ComponentID)
		if id == "job-4" {
			j.ExpertID = ""
		}
		b.runBridgeJob(context.Background(), j)
	}
	ps := prov.recordedProfiles()
	if len(ps) != 3 || ps[0].Instructions != "v1 rules\n" || ps[1].Instructions != "v2 rules\n" || ps[2] != nil {
		t.Fatalf("sessions = %d %+v", len(ps), ps)
	}
	if in := prov.recordedInputs(); len(in) != 1 {
		t.Fatalf("follow-up inputs = %q", in)
	}
}

// A job whose expert no longer resolves is refused with a notice; nothing
// is driven.
func TestContentUnavailableExpertRefuses(t *testing.T) {
	g := newContentFixtureGateway(t)
	g.setEffective("job-1", wire.ContentEffective{Missing: []string{"expert Reviewer"}})
	prov := &profileProv{}
	b := contentBridge(g, prov)
	j := contentJob("job-1", "cc_gone")
	j.ExpertName = "Reviewer"
	b.runBridgeJob(context.Background(), j)
	if len(prov.recordedProfiles()) != 0 {
		t.Fatal("drove a job whose expert is gone")
	}
	if st := g.lastStatus(); st["status"] != "failed" || st["failureReason"] != "content_unavailable" {
		t.Fatalf("status = %+v", st)
	}
	if n := g.allNotices(); !strings.Contains(n, "The expert Reviewer is no longer available") {
		t.Fatalf("notices = %q", n)
	}
}

// Someone else's script-bearing content forces the sandbox: with none
// available the turn is refused before anything runs.
func TestForeignScriptContentRequiresTheSandbox(t *testing.T) {
	g := newContentFixtureGateway(t)
	form := lintPackForm()
	form.Requires.Permissions = []string{contentpkg.PermScripts}
	pack := g.pkg(form, "1.0.0")
	pack.Source, pack.Owner = wire.ContentSourceRoom, "kim"
	if !pack.NeedsSandbox {
		t.Fatal("fixture pack does not need the sandbox")
	}
	g.setEffective("job-1", wire.ContentEffective{SkillPacks: []wire.ContentRef{pack}})
	prov := &profileProv{}
	b := contentBridge(g, prov)
	b.runBridgeJob(context.Background(), contentJob("job-1", ""))
	if len(prov.recordedProfiles()) != 0 {
		t.Fatal("foreign script content ran on the host")
	}
	st := g.lastStatus()
	if st["status"] != "failed" || st["failureReason"] != "sandbox_required" {
		t.Fatalf("status = %+v", st)
	}
	if n := g.allNotices(); !strings.Contains(n, "This turn uses lint-kit from someone else") {
		t.Fatalf("notices = %q", n)
	}

	// The same pack authored by the agent owner runs on the host.
	pack.Owner = "maya"
	g.setEffective("job-2", wire.ContentEffective{SkillPacks: []wire.ContentRef{pack}})
	b.runBridgeJob(context.Background(), contentJob("job-2", ""))
	if ps := prov.recordedProfiles(); len(ps) != 1 || len(ps[0].Skills) != 1 {
		t.Fatalf("own pack = %+v", ps)
	}
}
