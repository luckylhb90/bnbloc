package bridge

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// testDrive is a drive past stages 1–6, for a test that calls driveClaim
// directly: the job, its owner and core, the target, and the core's
// capability snapshot.
func testDrive(b *Bridge, ref jobRef, room, owner, core, provider, target, text, channel string) *drive {
	content := ref.content
	if content == nil {
		content = &jobContent{}
	}
	d := &drive{
		job: bridgeJob{JobID: ref.ID, TenantID: ref.TenantID, RoomID: room, ChannelID: channel, MessageSeq: ref.MessageSeq,
			RequesterUserID: ref.RequesterUserID, OwnerUserID: owner, Core: core, Provider: provider, MessageText: text},
		owner: owner, core: core, text: text, target: target, content: content, run: &runningJob{},
	}
	d.caps = b.coreCaps(context.Background(), core)
	return d
}

// testWriter is a core's workspace-file writer from its capability
// snapshot, with its endpoint.
func testWriter(b *Bridge, core string) (corepkg.WorkspaceFileWriter, corepkg.Endpoint, bool) {
	entry, _ := b.coreEntry(core)
	w := b.coreCaps(context.Background(), core).writer
	return w, entry.endpoint, w != nil
}

// countingProv counts the capability verdicts asked of a sandbox-capable
// core.
type countingProv struct {
	*sandboxProv
	cmu   sync.Mutex
	asked map[corepkg.Capability]int
}

func (p *countingProv) Supports(ctx context.Context, ep corepkg.Endpoint, c corepkg.Capability) bool {
	p.cmu.Lock()
	p.asked[c]++
	p.cmu.Unlock()
	return p.sandboxProv.Supports(ctx, ep, c)
}

// A drive asks the core's capabilities once (stage 3) and every later stage
// reads that snapshot: a sandboxed borrowed drive used to ask sandboxExec
// when planning and again when creating the workspace (failing late with
// "the core no longer supports sandbox execution"), and agentProfile in
// the team check, the MCP fit and the session create.
func TestDriveCapabilitiesAskedOncePerDrive(t *testing.T) {
	gw := newSandboxGateway(t, "room-1", false)
	sbx := newFakeSandboxd(t)
	prov := &countingProv{sandboxProv: &sandboxProv{supports: true}, asked: map[corepkg.Capability]int{}}
	prov.targets = []corepkg.Target{{ID: "c", Provider: "codex"}}
	root := t.TempDir()
	b := New(Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov),
		Sandbox: SandboxConfig{Mode: SandboxBorrowed, URL: sbx.srv.URL, Token: sandboxAdminToken, VMExec: "/apps/sandbox/bnbloc-vmexec", Machine: "bnbloc", WorkspacesRoot: root}})

	b.runBridgeJob(context.Background(), job("job-1", "bob"))
	if st := gw.lastStatus(); st["status"] != "replied" {
		t.Fatalf("status = %v", st)
	}
	prov.cmu.Lock()
	defer prov.cmu.Unlock()
	for _, c := range []corepkg.Capability{corepkg.CapabilitySandboxExec, corepkg.CapabilityAgentProfile} {
		if n := prov.asked[c]; n != 1 {
			t.Errorf("%s asked %d times in one drive, want once (the stage-3 snapshot)", c, n)
		}
	}
}

// A room expert or skill pack that cannot be loaded is unmet, like an
// unattachable MCP tool: the turn still wears the chat's expert and every
// other pack, and the run report lists what is missing. Only the expert the
// chat picked is required.
func TestUnloadableSkillPackIsMissing(t *testing.T) {
	g := newContentFixtureGateway(t)
	expert := g.pkg(reviewerForm("You are the reviewer. MARKER-ALPHA"), "1.2.0")
	expert.Source = wire.ContentSourceExpert
	pack := g.pkg(lintPackForm(), "1.0.0")
	pack.Source = wire.ContentSourceRoom
	broken := g.pkg(contentFormNamed("docs-kit"), "1.0.0")
	broken.Source, broken.SHA256 = wire.ContentSourceMember, strings.Repeat("0", 64) // the pin no longer matches its bundle
	g.setEffective("job-1", wire.ContentEffective{Expert: &expert, SkillPacks: []wire.ContentRef{broken, pack}})
	prov := &profileProv{}
	b := contentBridge(g, prov)

	b.runBridgeJob(context.Background(), contentJob("job-1", expert.ComponentID))

	ps := prov.recordedProfiles()
	if len(ps) != 1 || ps[0] == nil || !strings.Contains(ps[0].Instructions, "MARKER-ALPHA") {
		t.Fatalf("profiles = %+v; want the expert worn despite the broken pack", ps)
	}
	var names []string
	for _, s := range ps[0].Skills {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "checklist,lint" {
		t.Fatalf("skills = %v; want the expert's and the loadable pack's", names)
	}
	var running map[string]any
	g.mu.Lock()
	for _, st := range g.statuses {
		if st["status"] == "running" {
			running, _ = st["diagnostics"].(map[string]any)
		}
	}
	g.mu.Unlock()
	if missing := toStrings(running["contentMissing"]); len(missing) != 1 || !strings.Contains(missing[0], "docs-kit") {
		t.Fatalf("contentMissing = %v; want the broken pack", running["contentMissing"])
	}
}

func contentFormNamed(name string) contentpkg.Form {
	f := lintPackForm()
	f.Name = name
	f.Skills[0].Name = name
	return f
}

// TestNoHandRolledAgentRefs: the bridge spells an agent reference only
// through its owners — the shared-agent target through
// shareagent.PlatformTarget, a core/provider pair through
// core.QualifyTargetID / SplitTargetID, a drive slot through driveKey — so
// the encodings cannot drift between copies.
func TestNoHandRolledAgentRefs(t *testing.T) {
	fset := token.NewFileSet()
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services/bridge"}}, func(rel string, src []byte) {
		file, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING {
					return true
				}
				v, _ := strconv.Unquote(n.Value)
				if strings.HasPrefix(v, "shared-agent:") {
					t.Errorf("%s: a hand-rolled shared-agent target (use shareagent.PlatformTarget)", fset.Position(n.Pos()))
				}
				if v == "\x00" && rel != "services/bridge/drive.go" {
					t.Errorf("%s: a hand-rolled NUL-joined key (use driveKey)", fset.Position(n.Pos()))
				}
			case *ast.BinaryExpr:
				if n.Op == token.ADD && joinsCoreAndProvider(n) {
					t.Errorf("%s: a hand-rolled core/provider join (use core.QualifyTargetID)", fset.Position(n.Pos()))
					return false
				}
			}
			return true
		})
	})
}

// joinsCoreAndProvider reports a "+" chain that puts a "/" between an
// operand naming a core and one naming a provider.
func joinsCoreAndProvider(e *ast.BinaryExpr) bool {
	var operands []ast.Expr
	var flatten func(ast.Expr)
	flatten = func(x ast.Expr) {
		if b, ok := x.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			flatten(b.X)
			flatten(b.Y)
			return
		}
		operands = append(operands, x)
	}
	flatten(e)
	name := func(x ast.Expr) string {
		switch v := x.(type) {
		case *ast.Ident:
			return strings.ToLower(v.Name)
		case *ast.SelectorExpr:
			return strings.ToLower(v.Sel.Name)
		}
		return ""
	}
	for i := 0; i+2 < len(operands); i++ {
		lit, ok := operands[i+1].(*ast.BasicLit)
		if !ok || lit.Value != `"/"` {
			continue
		}
		a, b := name(operands[i]), name(operands[i+2])
		if strings.Contains(a, "core") && strings.Contains(b, "provider") {
			return true
		}
	}
	return false
}

// Every refusable stage ends the job final with a notice of its kind: the
// member always sees why nothing ran (TestDriveRefusalsAreFinal checks the
// kinds; this drives each stage's refusal end to end).
func TestEveryStageRefusalPostsItsNotice(t *testing.T) {
	for _, c := range []struct {
		name, stage string
		setup       func(*sandboxGateway, *Config, *bridgeJob)
		reason      string
		notice      string
	}{
		{"no text", "admit", func(_ *sandboxGateway, _ *Config, j *bridgeJob) { j.MessageText = "  " }, "invalid_job", "invalid-job"},
		{"core not run here", "admit", func(_ *sandboxGateway, _ *Config, j *bridgeJob) { j.Core = "core-z" }, "agent_not_advertised", "agent-unavailable"},
		{"provider turned off", "admit", func(_ *sandboxGateway, cfg *Config, _ *bridgeJob) {
			cfg.DisabledProviders = map[string]bool{corepkg.QualifyTargetID("core-a", "codex"): true}
		}, "agent_not_advertised", "agent-unavailable"},
		{"tenant requires the sandbox", "plan execution", func(g *sandboxGateway, _ *Config, _ *bridgeJob) { g.require = true }, "sandbox_required", "sandbox-required"},
		{"borrow denied", "authorize", func(g *sandboxGateway, _ *Config, _ *bridgeJob) { g.deny = "quota_exhausted" }, "borrow_not_authorized", "borrow-denied"},
		{"platform agent unsandboxed", "authorize", func(g *sandboxGateway, _ *Config, _ *bridgeJob) { g.platform = true }, "sandbox_required", "sandbox-required"},
	} {
		t.Run(c.stage+"/"+c.name, func(t *testing.T) {
			gw := newSandboxGateway(t, "room-1", false)
			prov := &sandboxProv{}
			cfg := Config{GatewayBaseURL: gw.srv.URL, GatewayToken: "t", OwnerSubject: "alice", Cores: newTestCore(prov)}
			j := job("job-1", "bob")
			c.setup(gw, &cfg, &j)
			New(cfg).runBridgeJob(context.Background(), j)
			if st := gw.lastStatus(); st["status"] != "failed" || st["failureReason"] != c.reason {
				t.Fatalf("status = %v, want failed:%s", st, c.reason)
			}
			if prov.turns != 0 || prov.hostWorkspace != 0 {
				t.Fatal("a refused job drove the core")
			}
			_, fresh := gw.stored()
			if len(fresh) != 1 {
				t.Fatalf("notices = %d, want one", len(fresh))
			}
			p, _ := fresh[0].Payload.(map[string]any)
			if p["notice"] != c.notice || p["failReason"] != c.reason || strings.TrimSpace(payloadText(fresh[0])) == "" {
				t.Fatalf("notice = %+v, want kind %s for %s", p, c.notice, c.reason)
			}
		})
	}
}
