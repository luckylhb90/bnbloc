package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/digest"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/services/bridge/roomtools"
	"github.com/luckylhb90/bnbloc/services/gateway/appsdk"
)

// fsCore is a core whose workspaces are directories, as an ACP adapter's
// are: a session runs in the workspace it is created in, the agent writes
// files there during its turns, and bnbloc's workspace file writes land
// there too. It offers agentProfile and writeFile, and its target accepts
// http MCP servers.
type fsCore struct {
	root string

	mu       sync.Mutex
	created  []string
	sessions []fsSession
	// gone: workspaces the core no longer holds; stale: sessions it lost.
	gone  map[string]bool
	stale map[string]bool
	// unknown fails the capability read (a daemon whose discovery fails).
	unknown error
	// turnFiles are what the agent writes during its next turn.
	turnFiles map[string]string
	// copyWrites counts bnbloc's writes under .room/deliverables/; writes
	// every workspace file write by path.
	copyWrites []string
	writes     map[string]int
	started    bool
	// turnPolls makes every turn report busy that many polls after it
	// started; inTurn and overlaps catch a turn that starts while another
	// one runs in the same core.
	turnPolls int
	busy      int
	inTurn    bool
	overlaps  int
	// maxBody refuses a file write over that many bytes, as a core whose
	// server caps request bodies does, for attachments (0 = none); maxInput is the limit it
	// states (core.InputLimiter, 0 = none).
	maxBody  int
	maxInput int64
}

func (c *fsCore) MaxInputBytes(context.Context, corepkg.Endpoint) int64 { return c.maxInput }

type fsSession struct {
	id, workspace string
	profile       *corepkg.AgentProfile
	// files is what the workspace held when the session (and its harness)
	// started.
	files []string
	// turns are the messages the session received: its initial prompt and
	// every input (its conversation).
	turns []string
}

func newFSCore(t *testing.T) *fsCore {
	return &fsCore{root: t.TempDir(), gone: map[string]bool{}, stale: map[string]bool{}}
}

func (c *fsCore) Name() string { return "core-a" }

func (c *fsCore) dir(ws string) string { return filepath.Join(c.root, ws) }

func (c *fsCore) CreateWorkspace(context.Context, corepkg.Endpoint) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := fmt.Sprintf("ws%d", len(c.created)+1)
	c.created = append(c.created, id)
	return id, os.MkdirAll(c.dir(id), 0o700)
}

// held reports whether the core holds workspace ws. Callers hold mu.
func (c *fsCore) held(ws string) bool {
	if c.gone[ws] {
		return false
	}
	_, err := os.Stat(c.dir(ws))
	return err == nil
}

func notFound(path string) error {
	return &corepkg.HTTPError{Core: "core-a", Method: http.MethodPost, Path: path, Status: http.StatusNotFound}
}

func (c *fsCore) CreateSession(_ context.Context, _ corepkg.Endpoint, req corepkg.SessionRequest) (corepkg.SessionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held(req.WorkspaceID) {
		return corepkg.SessionResult{}, notFound("/workspaces/" + req.WorkspaceID + "/sessions")
	}
	s := fsSession{id: fmt.Sprintf("s%d", len(c.sessions)+1), workspace: req.WorkspaceID, profile: req.Profile, files: c.listLocked(req.WorkspaceID), turns: []string{req.Prompt}}
	c.sessions = append(c.sessions, s)
	c.workLocked(req.WorkspaceID)
	return corepkg.SessionResult{SessionID: s.id}, nil
}

func (c *fsCore) SendInput(_ context.Context, _ corepkg.Endpoint, ws, sess, text string, _ corepkg.SessionOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.sessions {
		if c.sessions[i].id == sess {
			c.sessions[i].turns = append(c.sessions[i].turns, text)
		}
	}
	c.workLocked(ws)
	return nil
}

// workLocked is the agent's turn: it writes turnFiles into ws.
func (c *fsCore) workLocked(ws string) {
	for rel, content := range c.turnFiles {
		full := filepath.Join(c.dir(ws), filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		_ = os.WriteFile(full, []byte(content), 0o600)
	}
	c.turnFiles = nil
	c.started = true
	if c.inTurn {
		c.overlaps++
	}
	c.inTurn, c.busy = true, c.turnPolls
}

// listLocked lists ws's files outside .room/ (the agent's own).
func (c *fsCore) listLocked(ws string) []string {
	var out []string
	var list func(rel string)
	list = func(rel string) {
		entries, _ := os.ReadDir(filepath.Join(c.dir(ws), filepath.FromSlash(rel)))
		for _, e := range entries {
			p := path.Join(rel, e.Name())
			switch {
			case p == ".room":
			case e.IsDir():
				list(p)
			default:
				out = append(out, p)
			}
		}
	}
	list("")
	sort.Strings(out)
	return out
}

func (c *fsCore) ListTargets(context.Context, corepkg.Endpoint) ([]corepkg.Target, error) {
	return []corepkg.Target{{ID: "t", Provider: "agent-a", MCPTransports: []string{corepkg.MCPStdio, corepkg.MCPHTTP}}}, nil
}

func (c *fsCore) Messages(context.Context, corepkg.Endpoint, string, string, int64) ([]corepkg.Message, int64, error) {
	return nil, 0, nil
}

func (c *fsCore) PollTurn(_ context.Context, _ corepkg.Endpoint, _, sess string) (corepkg.TurnStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stale[sess] {
		return corepkg.TurnStatus{}, errors.New("session_not_found")
	}
	if c.started {
		c.started = false
		return corepkg.TurnStatus{}, nil
	}
	if c.busy > 0 {
		c.busy--
		return corepkg.TurnStatus{}, nil
	}
	c.inTurn = false
	return corepkg.TurnStatus{Idle: true}, nil
}

func (c *fsCore) Supports(_ context.Context, _ corepkg.Endpoint, capability corepkg.Capability) bool {
	return capability == corepkg.CapabilityAgentProfile || capability == corepkg.CapabilityWriteFile
}

func (c *fsCore) CapabilitiesKnown(context.Context, corepkg.Endpoint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.unknown
}

func (c *fsCore) WriteWorkspaceFile(ctx context.Context, ep corepkg.Endpoint, ws, rel, content string) error {
	return c.WriteWorkspaceFileBytes(ctx, ep, ws, rel, []byte(content))
}

func (c *fsCore) WriteWorkspaceFileBytes(_ context.Context, _ corepkg.Endpoint, ws, rel string, data []byte) error {
	if err := corepkg.CheckWorkspaceFile(rel); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held(ws) {
		return notFound("/workspaces/" + ws + "/files")
	}
	if c.maxBody > 0 && len(data) > c.maxBody && strings.HasPrefix(rel, ".room/attachments/") {
		return &corepkg.HTTPError{Core: "core-a", Method: http.MethodPut, Path: "/workspaces/" + ws + "/files", Status: http.StatusBadRequest, Body: []byte("invalid json body")}
	}
	if strings.HasPrefix(rel, roomlog.DeliverablesDir) {
		c.copyWrites = append(c.copyWrites, rel)
	}
	if c.writes == nil {
		c.writes = map[string]int{}
	}
	c.writes[rel]++
	full := filepath.Join(c.dir(ws), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0o600)
}

func (c *fsCore) snapshot() (created []string, sessions []fsSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.created...), append([]fsSession(nil), c.sessions...)
}

func (c *fsCore) agentWrites(files map[string]string) {
	c.mu.Lock()
	c.turnFiles = files
	c.mu.Unlock()
}

// fsBridge is a bridge driving c for maya in room r1, its state in dir
// ("" = memory only), with the room tools enabled.
func fsBridge(t *testing.T, g *pullGateway, c *fsCore, dir string) *Bridge {
	t.Helper()
	b := New(Config{
		GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya", StateDir: dir,
		Cores: []CoreConfig{{Name: "core-a", Provider: c, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	stop, err := b.EnableRoomTools("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	ts, _ := c.ListTargets(context.Background(), corepkg.Endpoint{})
	b.cacheTargets("core-a", ts)
	return b
}

// appBuilder is the content of a turn wearing an expert.
func appBuilder() *jobContent {
	return &jobContent{instructions: "You build interactive apps.", skills: []corepkg.ProfileSkill{{Name: "interactive-apps", Files: []corepkg.ProfileFile{{Path: "SKILL.md", Content: []byte("---\nname: interactive-apps\n---\nThe SDK.")}}}}}
}

func fsDrive(t *testing.T, b *Bridge, id string, content *jobContent) {
	t.Helper()
	d := testDrive(b, jobRef{ID: id, RequesterUserID: "maya", content: content}, "r1", "maya", "core-a", "agent-a", "t", "go "+id, "general")
	if _, err := b.driveClaim(context.Background(), d, nil); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
}

func hasRoomTools(p *corepkg.AgentProfile) bool {
	if p == nil {
		return false
	}
	for _, s := range p.MCPServers {
		if s.Name == roomtools.ServerName {
			return true
		}
	}
	return false
}

// The QA bug (docs/bugs/2026-10-13-agent-loses-its-workspace.md): a turn
// wearing an expert builds an app, a plain follow-up without the expert
// starts a new session — in the SAME workspace, where the app's files still
// are — and that session has the room tools too.
func TestContentChangeKeepsTheWorkspace(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	c.agentWrites(map[string]string{"app/index.html": "<div id=app></div>", "app/app.js": "render()"})
	fsDrive(t, b, "job-1", appBuilder())
	fsDrive(t, b, "job-2", nil)

	created, sessions := c.snapshot()
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want a new one for the changed content", len(sessions))
	}
	if len(created) != 1 || sessions[0].workspace != sessions[1].workspace {
		t.Fatalf("workspaces created %v, sessions in %s and %s; want one workspace", created, sessions[0].workspace, sessions[1].workspace)
	}
	if got := strings.Join(sessions[1].files, ","); got != "app/app.js,app/index.html" {
		t.Fatalf("the second session started with %q; want the first turn's files", got)
	}
	for i, s := range sessions {
		if !hasRoomTools(s.profile) {
			t.Fatalf("session %d has no room tools: %+v", i+1, s.profile)
		}
		if facts, _, _ := cutGuidance(s.profile.Instructions); !facts.HasTools {
			t.Fatalf("session %d's guidance does not mention the room tools", i+1)
		}
	}
	if profileContent(t, sessions[1].profile) != "" {
		t.Fatalf("the plain follow-up wears content: %q", sessions[1].profile.Instructions)
	}
}

// A session the core lost (a stale binding rebinds) and a bridge restart
// both start a new session in the agent's workspace; the bindings persist
// in the state directory, owner-only.
func TestRebindAndRestartKeepTheWorkspace(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	state := t.TempDir()
	b := fsBridge(t, g, c, state)
	c.agentWrites(map[string]string{"notes.md": "turn 1"})
	fsDrive(t, b, "job-1", nil)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.stale[sessions[0].id] = true
	c.mu.Unlock()
	fsDrive(t, b, "job-2", nil)

	restarted := fsBridge(t, g, c, state)
	fsDrive(t, restarted, "job-3", nil)

	created, sessions := c.snapshot()
	if len(sessions) != 3 || len(created) != 1 {
		t.Fatalf("sessions %d, workspaces %v; want three sessions in one workspace", len(sessions), created)
	}
	for i, s := range sessions[1:] {
		if s.workspace != created[0] || strings.Join(s.files, ",") != "notes.md" {
			t.Fatalf("session %d in %s with %v; want the agent's workspace and its file", i+2, s.workspace, s.files)
		}
	}
	info, err := os.Stat(filepath.Join(state, workspacesFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("bindings file: %v %v", info, err)
	}
}

// A workspace the core no longer holds is replaced (once), the new one
// bound; a bridge with no state directory starts in a new workspace after
// a restart (memory only).
func TestGoneWorkspaceIsReplaced(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	fsDrive(t, b, "job-1", nil)
	_, sessions := c.snapshot()
	c.mu.Lock()
	c.stale[sessions[0].id] = true
	c.gone[sessions[0].workspace] = true
	c.mu.Unlock()
	fsDrive(t, b, "job-2", nil)
	fsDrive(t, b, "job-3", appBuilder())
	created, sessions := c.snapshot()
	if len(created) != 2 || len(sessions) != 3 || sessions[1].workspace != "ws2" || sessions[2].workspace != "ws2" {
		t.Fatalf("workspaces %v, sessions %+v; want ws2 created once and kept", created, sessions)
	}

	fresh := fsBridge(t, g, c, "")
	fsDrive(t, fresh, "job-4", nil)
	if created, _ := c.snapshot(); len(created) != 3 {
		t.Fatalf("a bridge without state reused a workspace: %v", created)
	}
}

// A core whose capabilities cannot be read is not driven in a degraded
// session (no profile, no room tools): the drive fails retryably before
// anything reaches the core.
func TestUnknownCapabilitiesFailRetryably(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	c.unknown = errors.New("discover core protocol: connection refused")
	b := fsBridge(t, g, c, "")
	d := testDrive(b, jobRef{ID: "job-1", RequesterUserID: "maya"}, "r1", "maya", "core-a", "agent-a", "t", "go", "general")
	_, err := b.driveClaim(context.Background(), d, nil)
	if reason := driveFailureReason(err); reason != store.FailureCoreUnavailable {
		t.Fatalf("reason = %q (%v), want core_unavailable", reason, err)
	}
	if created, sessions := c.snapshot(); len(created) != 0 || len(sessions) != 0 {
		t.Fatalf("a drive with unknown capabilities reached the core: %v %v", created, sessions)
	}
}

// A published site's files appear under .room/deliverables/<id>/ on the
// next turn; an unchanged deliverable is not written again, even by a new
// session in the workspace; a new version is.
func TestDeliverableCopiesInTheWorkspace(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	publishSite := func(v int, js string) {
		bundle, err := tarbundle.PackFiles(map[string][]byte{"index.html": []byte("<script src=app.js></script>"), "app.js": []byte(js)}, tarbundle.SiteLimits)
		if err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		g.artifacts = fmt.Sprintf(`{"maxBytes":1048576,"artifacts":[{"artifactId":"art_site","title":"poll","kind":"site","fileName":"poll","currentVersion":%d,"createdBy":"maya","updatedAtMs":1}]}`, v)
		g.versions[fmt.Sprintf("art_site/%d", v)] = fmt.Sprintf(`{"version":{"artifactId":"art_site","version":%d,"storage":"blob","sizeBytes":%d,"contentHash":%q,"authorUserId":"maya"}}`, v, len(bundle), digest.Of(bundle))
		g.contents[fmt.Sprintf("art_site/%d", v)] = string(bundle)
		g.mu.Unlock()
	}
	publishSite(1, "render(1)")
	fsDrive(t, b, "job-1", appBuilder())
	ws := filepath.Join(c.root, "ws1")
	if got, err := os.ReadFile(filepath.Join(ws, ".room/deliverables/art_site/app.js")); err != nil || string(got) != "render(1)" {
		t.Fatalf("site copy = %q, %v", got, err)
	}
	artifacts, _ := os.ReadFile(filepath.Join(ws, ".room/artifacts.md"))
	if !strings.Contains(string(artifacts), "copy: .room/deliverables/art_site/") {
		t.Fatalf("artifacts.md:\n%s", artifacts)
	}
	c.mu.Lock()
	first := len(c.copyWrites)
	c.mu.Unlock()
	if first != 2 {
		t.Fatalf("first pass copied %d files, want 2", first)
	}

	// A new session (no expert) in the same workspace: nothing to copy.
	fsDrive(t, b, "job-2", nil)
	c.mu.Lock()
	again := len(c.copyWrites)
	c.mu.Unlock()
	if again != first {
		t.Fatalf("an unchanged deliverable was copied again (%d writes)", again-first)
	}

	publishSite(2, "render(2)")
	fsDrive(t, b, "job-3", nil)
	if got, _ := os.ReadFile(filepath.Join(ws, ".room/deliverables/art_site/app.js")); string(got) != "render(2)" {
		t.Fatalf("the new version was not copied: %q", got)
	}
}

// A pull workspace holds the app SDK in demo mode for an agent's self-test
// of an app (docs/prd/2026-10-13-interactive-apps-self-test.md): written
// at the workspace's first pass, not again while it is unchanged, whichever
// session drives.
func TestWorkspaceHoldsTheDemoAppSDK(t *testing.T) {
	g := newPullGateway(t)
	c := newFSCore(t)
	b := fsBridge(t, g, c, "")
	fsDrive(t, b, "job-1", appBuilder())
	fsDrive(t, b, "job-2", nil)
	fsDrive(t, b, "job-3", nil)
	got, err := os.ReadFile(filepath.Join(c.root, "ws1", filepath.FromSlash(roomlog.AppSDKPath)))
	if err != nil || string(got) != string(appsdk.DemoJS) {
		t.Fatalf("the workspace's SDK is not the demo-mode SDK (%v)", err)
	}
	c.mu.Lock()
	n := c.writes[roomlog.AppSDKPath]
	c.mu.Unlock()
	if n != 1 {
		t.Fatalf("the SDK was written %d times over three turns of two sessions, want once", n)
	}
}
