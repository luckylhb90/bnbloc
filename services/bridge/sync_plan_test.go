package bridge

import (
	"context"
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
