package gateway

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Org publish approval is the organization's setting
// (docs/prd/2026-10-13-app-gallery.md §11): with "required" (the default)
// a member's publish opens a request; with "none" it is published at once,
// audited, and admins can still unpublish and restore it. The same for
// every content kind.

// kindPackages is one package of every content kind, named name.
func kindPackages(name string) map[string]map[string]string {
	return map[string]map[string]string{
		"expert": draftExpert(name, nil),
		"skill": {
			"component.json":  `{"kind":"bnbloc.skill.package/v1","name":"` + name + `","version":"1.0.0","skills":["lint"]}`,
			"lint/SKILL.md":   "---\nname: lint\ndescription: Use to lint.\n---\nLint it.\n",
			"lint/checks.txt": "x",
		},
		"prompt": {
			"component.json": `{"kind":"bnbloc.prompt.package/v1","name":"` + name + `","version":"1.0.0","prompts":[{"id":"hi","title":"Hi","body":"Say hi"}]}`,
		},
		"app": {
			"component.json": `{"kind":"bnbloc.app.package/v1","name":"` + name + `","version":"1.0.0","category":"data"}`,
			"index.html":     "<p>app</p>",
			"app.js":         "1",
		},
	}
}

// setApproval makes the gateway read the organization's publish approval
// as setting ("" leaves the registry out: the defaults).
func (g *sharingGateway) setApproval(setting string) {
	if setting == "" {
		g.svc.TenantSettings = nil
		return
	}
	g.svc.TenantSettings = func(_ context.Context, tid string) (store.Tenant, error) {
		return store.Tenant{TenantID: tid, PublishApproval: setting}, nil
	}
}

func TestPublishApprovalEveryKind(t *testing.T) {
	g := newSharingGateway(t)
	for _, setting := range []string{"", wire.PublishApprovalRequired, wire.PublishApprovalNone} {
		g.setApproval(setting)
		for _, kind := range []string{"expert", "skill", "prompt", "app"} {
			name := "k-" + kind + "-" + firstNonEmpty(setting, "default")
			files := kindPackages(name)[kind]
			code, raw := g.do("maya", http.MethodPost, "/v1/content-components", "application/x-tar", packageTar(t, files))
			if code != http.StatusCreated {
				t.Fatalf("%s: create = %d %s", name, code, raw)
			}
			src := decodeDetail(t, raw).Component
			code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org", Note: "useful"})
			var pub wire.ContentPublishReply
			if code != http.StatusCreated || json.Unmarshal(raw, &pub) != nil {
				t.Fatalf("%s: publish = %d %s", name, code, raw)
			}
			org := findContent(g.library("jack", kind), name, "org")
			if setting != wire.PublishApprovalNone {
				if pub.Request == nil || pub.Request.Status != "pending" || org != nil {
					t.Fatalf("%s with approval %q: reply %+v, jack sees %+v (want a pending request, nothing published)", name, setting, pub, org)
				}
				continue
			}
			if pub.Request != nil || pub.Scope != wire.ContentScopeOrg || org == nil || org.Status != "active" || org.LatestVersion != "1.0.0" {
				t.Fatalf("%s without approval: reply %+v, jack sees %+v (want it published at once)", name, pub, org)
			}
			if !strings.Contains(g.actions(), "publish "+name+"@1.0.0") {
				t.Fatalf("%s: the publication is not audited: %s", name, g.actions())
			}
			// Admins still unpublish and restore it.
			if code, raw := g.json("ada", http.MethodPost, "/v1/tenant/content-components/"+org.ID+"/unpublish", nil); code != http.StatusOK && code != http.StatusNoContent {
				t.Fatalf("%s: unpublish = %d %s", name, code, raw)
			}
			if findContent(g.library("jack", kind), name, "org") != nil {
				t.Fatalf("%s: still listed after unpublish", name)
			}
			if code, raw := g.json("ada", http.MethodPost, "/v1/tenant/content-components/"+org.ID+"/restore", nil); code != http.StatusOK && code != http.StatusNoContent {
				t.Fatalf("%s: restore = %d %s", name, code, raw)
			}
			if findContent(g.library("jack", kind), name, "org") == nil {
				t.Fatalf("%s: not listed after restore", name)
			}
		}
	}
}

// A new version of a component the organization uses is published at once
// where no approval is required (the save says so), and supersedes a
// request still pending from when approval was required.
func TestPublishApprovalNewVersion(t *testing.T) {
	g := newSharingGateway(t)
	src := g.create("maya", expertForm("Review carefully."))
	g.setApproval(wire.PublishApprovalRequired)
	code, raw := g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/publish", wire.ContentPublishInput{Scope: "org"})
	if code != http.StatusCreated {
		t.Fatalf("publish = %d %s", code, raw)
	}
	g.setApproval(wire.PublishApprovalNone)
	form := expertForm("Review even more carefully.")
	form["baseVersion"] = "1.0.0"
	code, raw = g.json("maya", http.MethodPost, "/v1/content-components/"+src.ID+"/versions", form)
	d := decodeDetail(t, raw)
	if code != http.StatusCreated || d.OrgPublished != "1.0.1" || d.Resubmitted != nil {
		t.Fatalf("new version = %d %+v", code, d)
	}
	org := findContent(g.library("jack", "expert"), "reviewer", "org")
	if org == nil || org.LatestVersion != "1.0.1" {
		t.Fatalf("jack's org expert = %+v", org)
	}
	reqs, err := g.svc.ContentComponents.ListContentRequests(context.Background(), store.ContentRequestQuery{ComponentID: src.ID, Status: store.ContentRequestPending})
	if err != nil || len(reqs) != 0 {
		t.Fatalf("pending requests = %+v, %v (the publication supersedes them)", reqs, err)
	}
}

// Every publish path decides through contentshare.NeedsApproval, in one
// place: publishToOrg is the only caller of openPublishRequest (the only
// writer of a publish request) and, beside an admin's approval, of
// orgPublish; nothing else reads the setting.
func TestOrgPublishDecidedByNeedsApproval(t *testing.T) {
	allowed := map[string]map[string]bool{
		"NeedsApproval":        {"publishToOrg": true},
		"openPublishRequest":   {"publishToOrg": true},
		"orgPublish":           {"publishToOrg": true, "decideContent": true},
		"CreateContentRequest": {"openPublishRequest": true},
	}
	needsCalled := false
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal/contentshare", "internal/contentuse"}}, func(rel string, src []byte) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					name := ""
					switch fn := x.Fun.(type) {
					case *ast.SelectorExpr:
						name = fn.Sel.Name
					case *ast.Ident:
						name = fn.Name
					}
					if callers, ok := allowed[name]; ok && !callers[fd.Name.Name] {
						t.Errorf("%s: %s calls %s (only %v may)", fset.Position(x.Pos()), fd.Name.Name, name, keysOf(callers))
					}
					if name == "NeedsApproval" && fd.Name.Name == "publishToOrg" {
						needsCalled = true
					}
				case *ast.SelectorExpr:
					if x.Sel.Name == "PublishApproval" && !strings.HasPrefix(rel, "internal/contentshare/") && !strings.HasPrefix(rel, "services/shareagent/") {
						t.Errorf("%s: %s reads the publish approval setting: decide through contentshare.NeedsApproval", fset.Position(x.Pos()), fd.Name.Name)
					}
				}
				return true
			})
		}
	})
	if !needsCalled {
		t.Error("publishToOrg does not decide through contentshare.NeedsApproval")
	}
}

func keysOf(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
