package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
)

func syncSession(core, ws, sid string, startMs int64) *roomSession {
	rs := &roomSession{core: core, workspaceID: ws, sessionID: sid}
	if startMs > 0 {
		rs.markTurn(deliverableTurn{StartMs: startMs})
	}
	return rs
}

func dirsOf(m map[string]string) func(*roomSession) string {
	return func(rs *roomSession) string { return m[rs.workspaceID+"/"+rs.sessionID] }
}

// Live sync mirrors the session that ran a turn last.
func TestPickSyncSessionTakesTheLatestTurn(t *testing.T) {
	older, newer := syncSession("c", "w1", "s1", 100), syncSession("c", "w2", "s2", 200)
	rs, reason := pickSyncSession([]*roomSession{older, newer}, dirsOf(map[string]string{"w1/s1": "/a", "w2/s2": "/b"}))
	if rs != newer || reason != "" {
		t.Fatalf("picked %v (%q); want the newer", rs, reason)
	}
}

// Sessions with no turn time (restored after a restart) are a tie. A tie between sessions
// of one directory is no choice (the same directory); a tie between different directories
// is not decided by the session id: nothing is mirrored and the reason says why.
func TestPickSyncSessionTieIsDecidedOnlyByOneDirectory(t *testing.T) {
	a, b := syncSession("c", "w1", "s1", 0), syncSession("c", "w2", "s2", 0)
	rs, reason := pickSyncSession([]*roomSession{a, b}, dirsOf(map[string]string{"w1/s1": "/same", "w2/s2": "/same"}))
	if rs == nil || reason != "" {
		t.Fatalf("one directory: picked %v (%q)", rs, reason)
	}
	rs, reason = pickSyncSession([]*roomSession{a, b}, dirsOf(map[string]string{"w1/s1": "/one", "w2/s2": "/two"}))
	if rs != nil || !strings.Contains(reason, "different directories") {
		t.Fatalf("two directories: picked %v (%q); want none and the reason", rs, reason)
	}
}

func TestPickSyncSessionSaysWhyNone(t *testing.T) {
	if rs, reason := pickSyncSession(nil, nil); rs != nil || reason == "" {
		t.Fatalf("no candidates: %v %q", rs, reason)
	}
	rs, reason := pickSyncSession([]*roomSession{syncSession("c", "w", "s", 5)}, func(*roomSession) string { return "" })
	if rs != nil || !strings.Contains(reason, "does not say") {
		t.Fatalf("a core that reports no directory: %v %q", rs, reason)
	}
}

// Only the owner's own sessions on this machine's host are candidates: a session another
// member borrowed, a sandboxed one and another owner's are not, however recent.
func TestSyncCandidatesAreTheOwnersOwnHostSessions(t *testing.T) {
	b := New(Config{GatewayBaseURL: "http://gw", GatewayToken: "t", OwnerSubject: "alice"})
	own := syncSession("c", "w1", "own", 10)
	borrowed := syncSession("c", "w2", "borrowed", 999)
	sandboxed := syncSession("c", "w3", "sandboxed", 999)
	other := syncSession("c", "w4", "other", 999)
	elsewhere := syncSession("c", "w5", "elsewhere-room", 999)
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "codex"}}] = own
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "codex", requester: "bob"}}] = borrowed
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "claude", sandboxed: true}}] = sandboxed
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "opencode", owner: "carol"}}] = other
	b.sessions[sessionKey{driveKey: driveKey{room: "r2", core: "c", provider: "codex"}}] = elsewhere
	got := b.syncCandidates("r")
	if len(got) != 1 || got[0] != own {
		t.Fatalf("candidates = %v; want only the owner's own host session", got)
	}
}

// When nothing can be mirrored the room is told, once per reason, however often the plan is
// recomputed (every reconnect does).
func TestSyncPlanTellsTheRoomOncePerReason(t *testing.T) {
	var mu sync.Mutex
	var notices []string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent-activity/messages") {
			mu.Lock()
			notices = append(notices, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"syncMode":"mutagen"}`))
	}))
	defer gw.Close()
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "alice",
		Cores: []CoreConfig{{Name: "c", Provider: &workdirProv{dirs: map[string]string{"w1/s1": "/one", "w2/s2": "/two"}}, Endpoint: corepkg.Endpoint{BaseURL: "http://c"}}}})
	for i := 0; i < 3; i++ {
		if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "" {
			t.Fatalf("no session: mirrored %q", dir)
		}
	}
	mu.Lock()
	if len(notices) != 1 {
		t.Fatalf("%d notices for one reason; want 1", len(notices))
	}
	mu.Unlock()
	// A new reason is told again.
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "a"}}] = syncSession("c", "w1", "s1", 0)
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: "b"}}] = syncSession("c", "w2", "s2", 0)
	_, _, _ = b.syncPlan(context.Background(), "r")
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 2 {
		t.Fatalf("%d notices after a second reason; want 2", len(notices))
	}
}

// syncBridge is a bridge whose room "r" is in live-sync mode, with cores that report the
// given session directories; notices collects what the room is told.
func syncBridge(t *testing.T, dirs map[string]string, stateDir string) (*Bridge, *workdirProv, *[]string) {
	t.Helper()
	var mu sync.Mutex
	notices := &[]string{}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agent-activity/messages") {
			var body struct {
				Messages []struct {
					Payload struct {
						Text string `json:"text"`
					} `json:"payload"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			for _, m := range body.Messages {
				*notices = append(*notices, m.Payload.Text)
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"syncMode":"mutagen"}`))
	}))
	t.Cleanup(gw.Close)
	prov := &workdirProv{dirs: dirs}
	b := New(Config{GatewayBaseURL: gw.URL, GatewayToken: "t", OwnerSubject: "alice", StateDir: stateDir,
		Cores: []CoreConfig{{Name: "c", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: "http://c"}}}})
	return b, prov, notices
}

func bind(b *Bridge, provider string, rs *roomSession) {
	b.sessions[sessionKey{driveKey: driveKey{room: "r", core: "c", provider: provider}}] = rs
}

// The directory chosen once is kept: another agent speaking later (a newer turn in another
// directory) does not move it, and neither does a reconnect (the plan is recomputed).
func TestSyncDirIsKeptWhenAnotherAgentSpeaksLater(t *testing.T) {
	b, _, notices := syncBridge(t, map[string]string{"w1/s1": "/proj/one", "w2/s2": "/proj/two"}, "")
	one := syncSession("c", "w1", "s1", 100)
	bind(b, "codex", one)
	if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "/proj/one" {
		t.Fatalf("first plan = %q; want /proj/one", dir)
	}
	two := syncSession("c", "w2", "s2", 900) // the other agent spoke last
	bind(b, "claude", two)
	for i := 0; i < 3; i++ {
		if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "/proj/one" {
			t.Fatalf("plan %d after another agent spoke = %q; want the kept /proj/one", i, dir)
		}
	}
	if len(*notices) != 0 {
		t.Fatalf("a kept directory is not news: %q", *notices)
	}
}

// The kept directory survives a bridge restart: a restored session has no turn time, so
// without the kept directory the two agents would tie and nothing would be shared.
func TestSyncDirSurvivesARestart(t *testing.T) {
	state := t.TempDir()
	dirs := map[string]string{"w1/s1": "/proj/one", "w2/s2": "/proj/two"}
	b, _, _ := syncBridge(t, dirs, state)
	bind(b, "codex", syncSession("c", "w1", "s1", 100))
	if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "/proj/one" {
		t.Fatalf("first plan = %q", dir)
	}
	// A new bridge on the same state: both sessions restored, neither with a turn time.
	b2, _, notices := syncBridge(t, dirs, state)
	bind(b2, "codex", syncSession("c", "w1", "s1", 0))
	bind(b2, "claude", syncSession("c", "w2", "s2", 0))
	if _, dir, _ := b2.syncPlan(context.Background(), "r"); dir != "/proj/one" {
		t.Fatalf("plan after a restart = %q; want the kept /proj/one (a tie of two directories)", dir)
	}
	if len(*notices) != 0 {
		t.Fatalf("notices after a restart: %q", *notices)
	}
}

// The kept directory is replaced only when no session of the owner works in it any more,
// by the plan's own rules, and the room is told once.
func TestSyncDirIsReplacedOnlyWhenNoSessionWorksThere(t *testing.T) {
	b, prov, notices := syncBridge(t, map[string]string{"w1/s1": "/proj/one", "w2/s2": "/proj/two"}, "")
	bind(b, "codex", syncSession("c", "w1", "s1", 100))
	if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "/proj/one" {
		t.Fatalf("first plan = %q", dir)
	}
	// The codex session moves to another directory; claude works in /proj/two.
	prov.dirs["w1/s1"] = "/proj/moved"
	bind(b, "claude", syncSession("c", "w2", "s2", 500))
	_, dir, _ := b.syncPlan(context.Background(), "r")
	if dir != "/proj/two" {
		t.Fatalf("plan after /proj/one stopped being any session's directory = %q; want /proj/two (the latest turn)", dir)
	}
	for i := 0; i < 2; i++ {
		_, _, _ = b.syncPlan(context.Background(), "r")
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "/proj/two instead of /proj/one") {
		t.Fatalf("notices = %q; want one saying the directory changed", *notices)
	}
}

// When the kept directory is no longer any session's and no new one is clear, mirroring
// goes on with what was agreed (no silent stop) and the room is told.
func TestSyncDirStaysWhenNothingClearReplacesIt(t *testing.T) {
	b, prov, notices := syncBridge(t, map[string]string{"w1/s1": "/proj/one", "w2/s2": "/proj/two"}, "")
	bind(b, "codex", syncSession("c", "w1", "s1", 100))
	_, _, _ = b.syncPlan(context.Background(), "r")
	prov.dirs["w1/s1"], prov.dirs["w2/s2"] = "/proj/x", "/proj/y"
	bind(b, "claude", syncSession("c", "w2", "s2", 100)) // ties with codex: two different directories
	if _, dir, _ := b.syncPlan(context.Background(), "r"); dir != "/proj/one" {
		t.Fatalf("plan = %q; want the kept /proj/one (nothing clear replaces it)", dir)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "keeps sharing /proj/one") {
		t.Fatalf("notices = %q", *notices)
	}
}

// A deleted room's kept directory goes with the room's last workspace.
func TestSyncDirGoesWithTheRoom(t *testing.T) {
	w := newAgentWorkspaces(t.TempDir())
	w.setSyncDir("r", "/proj/one")
	if got := w.syncDir("r"); got != "/proj/one" {
		t.Fatalf("syncDir = %q", got)
	}
	k := driveKey{room: "r", core: "c", provider: "codex"}
	w.bind(k, "ws-1")
	w.purged(k, "ws-1")
	if got := w.syncDir("r"); got != "" {
		t.Fatalf("a purged room kept its sync directory %q", got)
	}
}
