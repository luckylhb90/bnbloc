package bridge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

func (fc *fakeCore) remove(path string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	delete(fc.files, path)
	delete(fc.stamps, path)
}

func newSiteBridge(t *testing.T, fc *fakeCore) (*Bridge, *captureGateway, *roomSession) {
	t.Helper()
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: newSessionFilesCore(fc)})
	return b, cg, &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
}

// siteCreates returns the kind-site artifact creates.
func (cg *captureGateway) siteCreates() []map[string]any {
	cg.mu.Lock()
	defer cg.mu.Unlock()
	var out []map[string]any
	for _, c := range cg.creates {
		if c["kind"] == store.ArtifactKindSite {
			out = append(out, c)
		}
	}
	return out
}

// sitePublishes returns the site version publishes (multipart, site MIME).
func (cg *captureGateway) sitePublishes() []map[string]any {
	cg.mu.Lock()
	defer cg.mu.Unlock()
	var out []map[string]any
	for _, p := range cg.publishes {
		if p["contentType"] == tarbundle.SiteMIME {
			out = append(out, p)
		}
	}
	return out
}

// otherTitles lists the titles of the non-site creates, sorted.
func (cg *captureGateway) otherTitles() []string {
	cg.mu.Lock()
	defer cg.mu.Unlock()
	var out []string
	for _, c := range cg.creates {
		if c["kind"] != store.ArtifactKindSite {
			out = append(out, c["title"].(string))
		}
	}
	sort.Strings(out)
	return out
}

func bundlePaths(t *testing.T, publish map[string]any) []string {
	t.Helper()
	bundle, err := tarbundle.Parse([]byte(publish["file"].(string)), "", tarbundle.SiteLimits)
	if err != nil {
		t.Fatalf("site publish is not a site bundle: %v", err)
	}
	return bundle.Paths()
}

const plainIndex = `<!doctype html><html><head><title> Demo &amp; Co </title><link rel="stylesheet" href="style.css"></head>
<body><script src="app.js"></script></body></html>`

// A plain HTML/CSS/JS app at the workspace root publishes as one site
// snapshot (title from <title>, fileName "site", the job/message linkage);
// its web assets are folded, its README is packed AND still publishes
// individually; hidden files (.env) and dependency dirs are never packed.
func TestSitePlainRootPublishesOneSnapshot(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	fc.set("index.html", plainIndex)
	fc.set("style.css", "body{}")
	fc.set("app.js", "console.log(1)")
	fc.set("README.md", "# Demo")
	fc.set(".env", "SECRET=1")
	fc.set(".git/HEAD", "ref")
	fc.set("node_modules/lib/index.js", "x")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start, JobID: "job-1", SourceSeq: 9})

	sites := cg.siteCreates()
	if len(sites) != 1 {
		t.Fatalf("site creates = %v, want one", sites)
	}
	if sites[0]["title"] != "Demo & Co" || sites[0]["fileName"] != "site" ||
		sites[0]["artifactId"] != artifactIDForFile("ws", "s", "/") {
		t.Fatalf("site create = %v", sites[0])
	}
	pubs := cg.sitePublishes()
	if len(pubs) != 1 || pubs[0]["fileName"] != "site" || pubs[0]["sourceJobId"] != "job-1" || pubs[0]["sourceSeq"] != "9" {
		t.Fatalf("site publishes = %v", pubs)
	}
	if got := strings.Join(bundlePaths(t, pubs[0]), ","); got != "README.md,app.js,index.html,style.css" {
		t.Fatalf("bundle = %s, want every file under the root except hidden/dependency entries", got)
	}
	if got := strings.Join(cg.otherTitles(), ","); got != "README.md" {
		t.Fatalf("individual deliverables = %s, want only README.md (web assets folded)", got)
	}
}

// A Vite project: the source root's index.html loads a .tsx module, so it is
// not a site; its dist/ build output is (and dist stays out of the individual
// deliverables).
func TestSiteViteSourceRootOnlyDistIsASite(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	fc.set("index.html", `<html><head><title>src</title></head><body><div id="root"></div><script type="module" src="/src/main.tsx?v=1"></script></body></html>`)
	fc.set("src/main.tsx", "import App from './App'")
	fc.set("package.json", `{"name":"x"}`)
	fc.set("node_modules/react/index.js", "x")
	fc.set("node_modules/pkg/index.html", "<html></html>")
	fc.set("dist/index.html", `<html><head><title>Vite App</title><script type="module" crossorigin src="/assets/index-abc.js"></script></head></html>`)
	fc.set("dist/assets/index-abc.js", "x()")
	fc.set("dist/assets/index-abc.css", "a{}")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})

	sites := cg.siteCreates()
	if len(sites) != 1 || sites[0]["title"] != "Vite App" || sites[0]["fileName"] != "dist" {
		t.Fatalf("site creates = %v, want only dist", sites)
	}
	pubs := cg.sitePublishes()
	if len(pubs) != 1 {
		t.Fatalf("site publishes = %v", pubs)
	}
	if got := strings.Join(bundlePaths(t, pubs[0]), ","); got != "assets/index-abc.css,assets/index-abc.js,index.html" {
		t.Fatalf("bundle = %s", got)
	}
	if got := strings.Join(cg.otherTitles(), ","); got != "index.html,package.json,src/main.tsx" {
		t.Fatalf("individual deliverables = %s, want the source files only", got)
	}
}

// A root nested in an accepted root is part of the outer site; a sibling
// root is a site of its own.
func TestSiteNestedRootsBelongToTheOuterSite(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	fc.set("web/index.html", "<html><title>Web</title></html>")
	fc.set("web/docs/index.html", "<html><title>Docs</title></html>")
	fc.set("web/docs/page.css", "p{}")
	fc.set("other/index.html", "<html>no title</html>")
	fc.set(".cache/index.html", "<html></html>")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})

	titles := map[string]map[string]any{}
	for _, c := range cg.siteCreates() {
		titles[c["title"].(string)] = c
	}
	if len(titles) != 2 || titles["Web"] == nil || titles["other"] == nil || titles["other"]["fileName"] != "other" {
		t.Fatalf("site creates = %v, want web (titled) and other (dir name)", titles)
	}
	for _, p := range cg.sitePublishes() {
		if p["fileName"] == "web" {
			if got := strings.Join(bundlePaths(t, p), ","); got != "docs/index.html,docs/page.css,index.html" {
				t.Fatalf("outer bundle = %s, want the nested root inside it", got)
			}
		}
	}
	if got := cg.otherTitles(); len(got) != 0 {
		t.Fatalf("individual deliverables = %v, want none", got)
	}
}

// Over tarbundle.SiteLimits or the room's artifact cap a site is not
// published (and its assets are still not dumped individually).
func TestSiteOverCapsIsNotPublished(t *testing.T) {
	t.Run("room cap", func(t *testing.T) {
		fc := newFakeCore()
		b, cg, rs := newSiteBridge(t, fc)
		cg.maxBytes = 4096 // the tar alone (headers) exceeds this with the asset
		start := time.Now().UnixMilli()
		fc.set("index.html", plainIndex)
		fc.set("app.js", strings.Repeat("x", 3000))
		b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
		if len(cg.siteCreates()) != 0 || len(cg.sitePublishes()) != 0 || len(cg.otherTitles()) != 0 {
			t.Fatalf("over the room cap: creates=%v publishes=%v", cg.creates, cg.publishes)
		}
	})
	t.Run("file count", func(t *testing.T) {
		fc := newFakeCore()
		b, cg, rs := newSiteBridge(t, fc)
		start := time.Now().UnixMilli()
		fc.set("index.html", plainIndex)
		for i := range tarbundle.SiteLimits.MaxFiles {
			fc.set(fmt.Sprintf("img/%04d.png", i), "p")
		}
		b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
		if len(cg.siteCreates()) != 0 || len(cg.sitePublishes()) != 0 {
			t.Fatalf("over the file cap published a site")
		}
		for _, r := range fc.reads {
			if r != "index.html" {
				t.Fatalf("over the file cap still read %s", r)
			}
		}
	})
}

// A pass without changes under a root publishes no site; a pass that
// rewrites a file with identical bytes packs the identical archive (the
// room answers unchanged); an edit or a removal republishes.
func TestSiteChangeDetectionAndDeterministicPack(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	fc.set("web/index.html", plainIndex)
	fc.set("web/app.js", "v1")
	fc.set("web/notes.md", "n")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.sitePublishes()) != 1 {
		t.Fatalf("first pass site publishes = %d", len(cg.sitePublishes()))
	}
	first := cg.sitePublishes()[0]["file"]

	// Nothing under the root changed (a file beside it did).
	start = fc.mtime + 30_000
	fc.set("other.md", "o")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.sitePublishes()) != 1 {
		t.Fatalf("unchanged root republished: %d", len(cg.sitePublishes()))
	}

	// Same bytes, new mtime: published, byte-identical archive.
	start = fc.mtime + 30_000
	fc.set("web/app.js", "v1")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	pubs := cg.sitePublishes()
	if len(pubs) != 2 || pubs[1]["file"] != first {
		t.Fatalf("touched site: publishes=%d identical=%v", len(pubs), len(pubs) == 2 && pubs[1]["file"] == first)
	}

	// Removal of a site file republishes without it.
	start = fc.mtime + 30_000
	fc.remove("web/notes.md")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	pubs = cg.sitePublishes()
	if len(pubs) != 3 || strings.Join(bundlePaths(t, pubs[2]), ",") != "app.js,index.html" {
		t.Fatalf("after removal: publishes=%d", len(pubs))
	}
}

// Files whose paths the bundle rules refuse are left out (logged); the rest
// of the site still publishes.
func TestSiteSkipsInvalidBundlePaths(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	fc.set("index.html", plainIndex)
	fc.set("app.js", "x")
	fc.set("my page.html", "<html></html>")
	fc.set("img/logo (1).png", "p")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	pubs := cg.sitePublishes()
	if len(pubs) != 1 || strings.Join(bundlePaths(t, pubs[0]), ",") != "app.js,index.html" {
		t.Fatalf("site publishes = %v", pubs)
	}
}

// More than maxSitesPerPass changed roots: the most recent publish now, the
// rest on the next pass.
func TestSitePerPassCapDefersTheRest(t *testing.T) {
	fc := newFakeCore()
	b, cg, rs := newSiteBridge(t, fc)
	start := time.Now().UnixMilli()
	for _, d := range []string{"a", "b", "c", "d"} {
		fc.set(d+"/index.html", "<html><title>"+d+"</title></html>")
	}
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	var got []string
	for _, p := range cg.sitePublishes() {
		got = append(got, p["fileName"].(string))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "b,c,d" {
		t.Fatalf("first pass sites = %v, want the 3 most recent", got)
	}
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: fc.mtime + 30_000})
	pubs := cg.sitePublishes()
	if len(pubs) != 4 || pubs[3]["fileName"] != "a" {
		t.Fatalf("deferred site not published on the next pass: %v", pubs)
	}
}

// A core whose reads are not binary-safe produces no sites: the files keep
// publishing individually as before.
func TestSiteSkippedOnTextOnlyCore(t *testing.T) {
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: []CoreConfig{{
		Name: "core-a", Provider: &textOnlyFilesProv{sessionFilesProv{fc: fc}}, Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"},
	}}})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
	start := time.Now().UnixMilli()
	fc.set("index.html", plainIndex)
	fc.set("app.js", "x")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.siteCreates()) != 0 {
		t.Fatalf("text-only core published a site: %v", cg.creates)
	}
	if got := strings.Join(cg.otherTitles(), ","); got != "app.js,index.html" {
		t.Fatalf("individual deliverables = %s", got)
	}
}

func TestSiteNeedsBuildAndTitle(t *testing.T) {
	for html, want := range map[string]bool{
		`<script type="module" src="/src/main.tsx"></script>`: true,
		`<script src='./main.ts?x'></script>`:                 true,
		`<SCRIPT SRC=app.jsx></SCRIPT>`:                       true,
		`<script src="App.vue"></script>`:                     true,
		`<script src="x.svelte#h"></script>`:                  true,
		`<script src="/assets/index-1.js"></script>`:          false,
		`<script>import("./a.ts")</script>`:                   false,
		`<link href="main.ts">`:                               false,
	} {
		if got := siteNeedsBuild(html); got != want {
			t.Errorf("siteNeedsBuild(%q) = %v, want %v", html, got, want)
		}
	}
	if got := siteTitle("out", "<title>\n</title>"); got != "out" {
		t.Errorf("blank title = %q, want the dir name", got)
	}
	if got := siteTitle("", "<html>"); got != "site" {
		t.Errorf("root without title = %q, want site", got)
	}
	if got := siteTitle("x", "<title>"+strings.Repeat("é", 300)+"</title>"); len([]rune(got)) != maxSiteTitleRunes {
		t.Errorf("long title kept %d runes", len([]rune(got)))
	}
}
