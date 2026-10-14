package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// fakeCore is an in-memory session workspace: the files a fake core's
// SessionFiles capability lists and reads.
type fakeCore struct {
	mu     sync.Mutex
	files  map[string]string // rel path -> content
	stamps map[string]int64  // rel path -> fake monotonic mtime (real-scale ms)
	mtime  int64             // last stamped mtime
	reads  []string          // rel paths read, in order
}

func newFakeCore() *fakeCore {
	return &fakeCore{files: map[string]string{}, stamps: map[string]int64{}}
}

func (fc *fakeCore) set(path, content string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	// Real-scale monotonic ms so the publisher's clock-skew headroom (2s)
	// behaves as it does against a filesystem.
	if fc.mtime == 0 {
		fc.mtime = time.Now().UnixMilli()
	} else {
		fc.mtime += 60_000
	}
	fc.files[path] = content
	fc.stamps[path] = fc.mtime
}

// sessionFilesProv is a fakeCoreProvider that also offers core.SessionFiles
// over a fakeCore workspace.
type sessionFilesProv struct {
	fakeCoreProvider
	fc *fakeCore
}

func (p *sessionFilesProv) ListSessionFiles(_ context.Context, _ corepkg.Endpoint, workspaceID, sessionID string) ([]corepkg.SessionFile, error) {
	if workspaceID != "ws" || sessionID != "s" {
		return nil, fmt.Errorf("unknown session %s/%s", workspaceID, sessionID)
	}
	p.fc.mu.Lock()
	defer p.fc.mu.Unlock()
	out := make([]corepkg.SessionFile, 0, len(p.fc.files))
	for path, c := range p.fc.files {
		out = append(out, corepkg.SessionFile{Path: path, Bytes: int64(len(c)), MtimeMs: p.fc.stamps[path]})
	}
	return out, nil
}

func (p *sessionFilesProv) ReadSessionFile(_ context.Context, _ corepkg.Endpoint, _, _, relPath string) ([]byte, error) {
	p.fc.mu.Lock()
	defer p.fc.mu.Unlock()
	c, ok := p.fc.files[relPath]
	if !ok {
		return nil, fmt.Errorf("no such file %q", relPath)
	}
	p.fc.reads = append(p.fc.reads, relPath)
	return []byte(c), nil
}

// textOnlyFilesProv is a sessionFilesProv whose reads are NOT binary-safe
// (it reports sessionFiles but not sessionFileBytes), like a manifest that
// extracts text content only.
type textOnlyFilesProv struct{ sessionFilesProv }

func (p *textOnlyFilesProv) Supports(_ context.Context, _ corepkg.Endpoint, c corepkg.Capability) bool {
	return c == corepkg.CapabilitySessionFiles
}

func newSessionFilesCore(fc *fakeCore) []CoreConfig {
	return []CoreConfig{{
		Name:     "core-a",
		Provider: &sessionFilesProv{fc: fc},
		Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"},
	}}
}

func stringsHasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// captureGateway records artifact creates and version publishes. A
// multipart (blob) publish is recorded with its form fields plus "file"
// (the bytes as a string), "fileName" and "contentType".
type captureGateway struct {
	mu         sync.Mutex
	creates    []map[string]any
	publishes  []map[string]any
	currentVer map[string]int
	maxBytes   int64
	srv        *httptest.Server
}

func newCaptureGateway(t *testing.T) *captureGateway {
	t.Helper()
	cg := &captureGateway{currentVer: map[string]int{}, maxBytes: 1 << 20}
	cg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cg.mu.Lock()
		defer cg.mu.Unlock()
		if r.Method == http.MethodGet && stringsHasSuffix(r.URL.Path, "/artifacts") {
			writeTestJSON(w, map[string]any{"artifacts": []any{}, "maxBytes": cg.maxBytes})
			return
		}
		var body map[string]any
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "multipart/form-data" {
			body = map[string]any{}
			mr, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart: %v", err)
				return
			}
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				raw, _ := io.ReadAll(part)
				if part.FormName() == "file" {
					body["file"] = string(raw)
					body["fileName"] = part.FileName()
					body["contentType"] = part.Header.Get("Content-Type")
					continue
				}
				body[part.FormName()] = string(raw)
			}
		} else {
			_ = json.NewDecoder(io.LimitReader(r.Body, 1<<22)).Decode(&body)
		}
		if r.Method == http.MethodPost && stringsHasSuffix(r.URL.Path, "/artifacts") {
			cg.creates = append(cg.creates, body)
			writeTestJSON(w, map[string]any{"artifact": body})
			return
		}
		if r.Method == http.MethodPost && stringsHasSuffix(r.URL.Path, "/versions") {
			cg.publishes = append(cg.publishes, body)
			writeTestJSON(w, map[string]any{"version": map[string]any{"version": 1}})
			return
		}
		// GET artifact: report a fresh current version each time so CAS bases
		// advance.
		id := filepath.Base(filepath.Dir(r.URL.Path))
		cg.currentVer[id]++
		writeTestJSON(w, map[string]any{"artifact": map[string]any{"currentVersion": cg.currentVer[id]}})
	}))
	t.Cleanup(cg.srv.Close)
	return cg
}

// The first observed turn only snapshots (no backfill dump); the second turn
// publishes exactly the changed files as artifact versions.
func TestPublishDeliverablesPublishesChangedFilesOnly(t *testing.T) {
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	b := New(Config{
		GatewayBaseURL: cg.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          newSessionFilesCore(fc),
	})

	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}

	// Turn 1 (cold): spec.md existed BEFORE the turn; main.go is written
	// during it. Only main.go must publish — a cold start must not dump the
	// session's older history into Docs, but this turn's own output must
	// surface immediately (regression: a first-turn file never published at
	// all, so a member's "write X" produced nothing in Docs).
	fc.set("docs/spec.md", "v1 spec")
	// Turn starts midway between the pre-turn write and the in-turn write, so
	// the 2s skew headroom cannot classify the old file as this turn's.
	start := fc.mtime + 30_000
	fc.set("src/main.go", "package main")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.creates) != 1 || len(cg.publishes) != 1 {
		t.Fatalf("cold start: creates=%d publishes=%d, want 1/1", len(cg.creates), len(cg.publishes))
	}
	if got := cg.creates[0]["title"]; got != "src/main.go" {
		t.Fatalf("cold start published %v, want only this turn's src/main.go", got)
	}
	// Content comes from the core's SessionFiles capability, read only for
	// the file being published.
	if len(fc.reads) != 1 || fc.reads[0] != "src/main.go" || cg.publishes[0]["content"] != "package main" {
		t.Fatalf("reads=%v publish=%v; want one capability read of src/main.go", fc.reads, cg.publishes[0])
	}

	// Turn 2 (warm): one new file, one unchanged file.
	start2 := fc.mtime + 30_000
	fc.set("docs/report.md", "the report")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start2})
	if len(cg.creates) != 2 || len(cg.publishes) != 2 {
		t.Fatalf("changed publish: creates=%d publishes=%d, want 2/2 (main.go + report.md)", len(cg.creates), len(cg.publishes))
	}
	created := cg.creates[1]
	if created["title"] != "docs/report.md" || created["kind"] != "markdown" {
		t.Fatalf("created = %v", created)
	}
	if created["artifactId"] == "" {
		t.Fatal("bridge must derive a stable artifactId")
	}
	// Turn 3 (warm): same-length rewrite of spec.md — only mtime catches it.
	start3 := fc.mtime + 30_000
	fc.set("docs/spec.md", "v2 spec")
	nCreates, nPublishes := len(cg.creates), len(cg.publishes)
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start3})
	if len(cg.creates) != nCreates+1 || len(cg.publishes) != nPublishes+1 {
		t.Fatalf("spec change publish: creates=%d publishes=%d", len(cg.creates), len(cg.publishes))
	}
	if got := cg.creates[len(cg.creates)-1]["artifactId"]; got == created["artifactId"] {
		_ = got // different file => different id; same-file follow-ups go through versions
	}
}

// A binary deliverable publishes as a blob version (multipart) with its
// sniffed MIME type and the job + prompting message it came from; text stays
// inline and carries the same source link.
func TestPublishBinaryDeliverableAsBlob(t *testing.T) {
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: newSessionFilesCore(fc)})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR" + strings.Repeat("\x00", 32)
	start := time.Now().UnixMilli()
	fc.set("assets/chart.png", png)
	fc.set("data.bin", "\x00\x01\x02 opaque")
	fc.set("notes.md", "text")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start, JobID: "job-9", SourceSeq: 42})
	byTitle := map[string]map[string]any{}
	for _, c := range cg.creates {
		byTitle[c["title"].(string)] = c
	}
	if byTitle["assets/chart.png"]["kind"] != store.ArtifactKindImage || byTitle["assets/chart.png"]["fileName"] != "chart.png" {
		t.Fatalf("png create = %v, want kind image + fileName", byTitle["assets/chart.png"])
	}
	if byTitle["data.bin"]["kind"] != store.ArtifactKindFile {
		t.Fatalf("bin create = %v, want kind file", byTitle["data.bin"])
	}
	var sawPNG, sawMD bool
	for _, p := range cg.publishes {
		switch {
		case p["fileName"] == "chart.png":
			sawPNG = true
			if p["contentType"] != "image/png" || p["file"] != png || p["sourceJobId"] != "job-9" || p["sourceSeq"] != "42" {
				t.Fatalf("png publish = %v", p)
			}
		case p["content"] == "text":
			sawMD = true
			if p["sourceJobId"] != "job-9" || p["sourceSeq"] != float64(42) {
				t.Fatalf("md publish lost its source link: %v", p)
			}
		}
	}
	if !sawPNG || !sawMD || len(cg.publishes) != 3 {
		t.Fatalf("publishes = %v", cg.publishes)
	}
}

// Ignore rules (dependency dirs, build output, hidden paths such as .git /
// .room / .env) and the room's size cap keep non-deliverables out; a core
// whose reads are text-only publishes no binaries (their bytes would be
// mangled).
func TestPublishSkipsIgnoredOversizeAndUnreadableBinaries(t *testing.T) {
	fc := newFakeCore()
	cg := newCaptureGateway(t)
	cg.maxBytes = 64
	b := New(Config{GatewayBaseURL: cg.srv.URL, GatewayToken: "t", OwnerSubject: "maya", Cores: []CoreConfig{{
		Name: "core-a", Provider: &textOnlyFilesProv{sessionFilesProv{fc: fc}}, Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"},
	}}})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
	start := time.Now().UnixMilli()
	for _, p := range []string{"node_modules/x/index.js", ".git/HEAD", ".room/live.md", ".env", "dist/app.js", "src/.cache/x.md"} {
		fc.set(p, "x")
	}
	fc.set("big.md", strings.Repeat("y", 65))
	fc.set("logo.png", "\x89PNG binaryish")
	fc.set("notes.md", "text")
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: start})
	if len(cg.creates) != 1 || cg.creates[0]["title"] != "notes.md" {
		t.Fatalf("creates = %v, want only notes.md", cg.creates)
	}
}

func TestIgnoredDeliverable(t *testing.T) {
	for p, want := range map[string]bool{
		"src/main.go": false, "docs/build-notes.md": false, "build.md": false,
		"build/out.js": true, "dist/index.html": true, "web/out/app.css": true, "dist.md": false, "a/node_modules/b.js": true, ".venv/x.py": true, "target/debug/app": true,
		".env": true, "src/.DS_Store": true, "__pycache__/m.pyc": true, "lib/x.o": true, "Main.class": true,
		// What an agent makes in an output directory is a deliverable.
		"work/out/deck.pptx": false, "out/report.pdf": false, "dist/chart.png": false, "build/data.csv": false,
		"out/assets/manifest.json": true, "dist/main.js.map": true,
	} {
		if got := ignoredDeliverable(p); got != want {
			t.Errorf("ignoredDeliverable(%q) = %v, want %v", p, got, want)
		}
	}
}

// A core without the SessionFiles capability publishes nothing: the bridge
// skips deliverables instead of guessing a core's REST shape (BR-15).
func TestPublishDeliverablesSkipsCoreWithoutSessionFiles(t *testing.T) {
	cg := newCaptureGateway(t)
	b := New(Config{
		GatewayBaseURL: cg.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores: []CoreConfig{{
			Name:     "core-a",
			Provider: &fakeCoreProvider{},
			Endpoint: corepkg.Endpoint{BaseURL: "http://core-a"},
		}},
	})
	rs := &roomSession{core: "core-a", workspaceID: "ws", sessionID: "s"}
	b.publishDeliverables(context.Background(), "room-1", "ch-1", rs, deliverableTurn{StartMs: 0})
	if len(cg.creates) != 0 || len(cg.publishes) != 0 {
		t.Fatalf("core without SessionFiles published: creates=%d publishes=%d", len(cg.creates), len(cg.publishes))
	}
	if rs.shared().published != nil {
		t.Fatal("skipped publish must not record a snapshot")
	}
}

// fakeCoreProvider satisfies the provider surface enough for coreEntry, and
// doubles as the pull-context test double: it records session prompts and
// workspace file writes.
type fakeCoreProvider struct {
	url string

	mu      sync.Mutex
	prompts []string
	writes  []string // rel paths, in write order
	// roomWrites records full (path, content) for record-content assertions.
	roomWrites []struct {
		path    string
		content string
	}
	// failWrites makes WriteWorkspaceFile fail, exercising the push fallback.
	failWrites bool
}

func (p *fakeCoreProvider) Name() string { return "core-a" }
func (p *fakeCoreProvider) CreateWorkspace(ctx context.Context, ep corepkg.Endpoint) (string, error) {
	dir, err := os.MkdirTemp("", "fkw")
	return filepath.Base(dir), err
}
func (p *fakeCoreProvider) CreateSession(ctx context.Context, ep corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	p.mu.Lock()
	p.prompts = append(p.prompts, req.Prompt)
	p.mu.Unlock()
	return corepkg.SessionResult{SessionID: "s"}, nil
}

// WriteWorkspaceFile records the write order (the pull contract writes INDEX
// last).
func (p *fakeCoreProvider) WriteWorkspaceFile(ctx context.Context, ep corepkg.Endpoint, workspaceID, relPath, content string) error {
	if p.failWrites {
		return fmt.Errorf("writer unavailable")
	}
	p.mu.Lock()
	p.writes = append(p.writes, relPath)
	p.roomWrites = append(p.roomWrites, struct{ path, content string }{relPath, content})
	p.mu.Unlock()
	return nil
}

func (p *fakeCoreProvider) roomFileWrites() []struct {
	path    string
	content string
} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]struct {
		path    string
		content string
	}{}, p.roomWrites...)
}

func (p *fakeCoreProvider) recordedPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

func (p *fakeCoreProvider) recordedWrites() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.writes...)
}
func (p *fakeCoreProvider) SendInput(ctx context.Context, ep corepkg.Endpoint, w, s, text string, opts corepkg.SessionOptions) error {
	return nil
}
func (p *fakeCoreProvider) ListTargets(ctx context.Context, ep corepkg.Endpoint) ([]corepkg.Target, error) {
	return []corepkg.Target{{ID: "core-a/agent-a", Provider: "agent-a"}}, nil
}
func (p *fakeCoreProvider) SessionIdle(ctx context.Context, ep corepkg.Endpoint, w, s string) (bool, error) {
	return true, nil
}
func (p *fakeCoreProvider) LatestSession(ctx context.Context, ep corepkg.Endpoint, w string) (string, error) {
	return "s", nil
}
func (p *fakeCoreProvider) Messages(ctx context.Context, ep corepkg.Endpoint, w, s string, after int64) ([]corepkg.Message, int64, error) {
	return nil, 0, nil
}
func (p *fakeCoreProvider) PollTurn(ctx context.Context, ep corepkg.Endpoint, w, s string) (corepkg.TurnStatus, error) {
	return corepkg.TurnStatus{Idle: true}, nil
}

// Kind mapping is content-aware for html: a script-carrying page is a program
// (app), scriptless html is a document, and the extension must survive the
// mapping at all — a regression once shipped .html as "code", which made the
// app upgrade unreachable.
func TestArtifactKindForContent(t *testing.T) {
	if k := artifactKindForContent("dash.html", "<html><script>x()</script></html>"); k != store.ArtifactKindApp {
		t.Fatalf("script html = %q, want app", k)
	}
	if k := artifactKindForContent("dash.html", "<html><body>doc</body></html>"); k != store.ArtifactKindHTML {
		t.Fatalf("scriptless html = %q, want html", k)
	}
	if k := artifactKindForContent("note.md", "# hi"); k != store.ArtifactKindMarkdown {
		t.Fatalf("md = %q, want markdown", k)
	}
	if k := artifactKindForContent("main.go", "package x"); k != store.ArtifactKindCode {
		t.Fatalf("go = %q, want code", k)
	}
}
