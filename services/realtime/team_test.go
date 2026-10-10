package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// teamFixture is a realtime service with in-memory team stores. alice and bob
// share room-1; only carol is in room-2.
type teamFixture struct {
	srv  *httptest.Server
	hub  *realtime.Hub
	chat *store.MemoryChat
}

func newTeamFixture(t *testing.T) *teamFixture {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "alice")
	mem.Add("room-1", "bob")
	mem.Add("room-2", "carol")
	hub := realtime.NewHub(64, 16)
	chat := store.NewMemoryChat()
	svc := &Service{
		Hub:        hub,
		Membership: mem,
		Chat:       chat,
		Prompts:    store.NewMemoryPrompts(),
		Tasks:      store.NewMemoryMessageTasks(),
		Messages:   chat,
		Artifacts:  store.NewMemoryArtifacts(),
		Rooms:      mem,
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return &teamFixture{srv: srv, hub: hub, chat: chat}
}

func (f *teamFixture) do(t *testing.T, user, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	if user != "" {
		req.Header.Set("X-Bnbloc-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// chat posts a chat message and returns its sequence.
func (f *teamFixture) chatAs(t *testing.T, user, room, text string) int64 {
	t.Helper()
	code, body := f.do(t, user, http.MethodPost, "/v1/rooms/"+room+"/chat", map[string]any{"text": text})
	if code != http.StatusOK {
		t.Fatalf("chat status = %d (%s)", code, body)
	}
	var out struct{ Sequence int64 }
	_ = json.Unmarshal(body, &out)
	return out.Sequence
}

func TestPromptAuthorOnlyEditsAndVisibility(t *testing.T) {
	f := newTeamFixture(t)
	code, body := f.do(t, "alice", http.MethodPost, "/v1/prompts", map[string]any{
		"title": "Release notes", "body": "Summarise the diff", "tags": []string{" Writing ", "writing", "ops"}, "visibility": "tenant",
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", code, body)
	}
	var shared wire.Prompt
	_ = json.Unmarshal(body, &shared)
	if len(shared.Tags) != 2 || shared.Tags[0] != "writing" || shared.Author != "alice" {
		t.Fatalf("created prompt = %+v, want normalized tags and author alice", shared)
	}
	code, body = f.do(t, "alice", http.MethodPost, "/v1/prompts", map[string]any{"title": "Mine", "body": "secret"})
	if code != http.StatusCreated {
		t.Fatalf("create private = %d (%s)", code, body)
	}
	var private wire.Prompt
	_ = json.Unmarshal(body, &private)
	if private.Visibility != wire.PromptPrivate {
		t.Fatalf("default visibility = %q, want private", private.Visibility)
	}

	// bob sees the tenant prompt only.
	code, body = f.do(t, "bob", http.MethodGet, "/v1/prompts", nil)
	var list struct{ Prompts []wire.Prompt }
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Prompts) != 1 || list.Prompts[0].PromptID != shared.PromptID {
		t.Fatalf("bob list = %d %+v, want only the tenant prompt", code, list.Prompts)
	}
	// Tag + mine filters.
	_, body = f.do(t, "alice", http.MethodGet, "/v1/prompts?tag=ops", nil)
	_ = json.Unmarshal(body, &list)
	if len(list.Prompts) != 1 || list.Prompts[0].PromptID != shared.PromptID {
		t.Fatalf("tag filter = %+v", list.Prompts)
	}
	_, body = f.do(t, "bob", http.MethodGet, "/v1/prompts?mine=1", nil)
	_ = json.Unmarshal(body, &list)
	if len(list.Prompts) != 0 {
		t.Fatalf("bob mine = %+v, want none", list.Prompts)
	}

	// Non-authors cannot edit or delete; another member's private prompt is
	// indistinguishable from a missing one.
	if code, _ := f.do(t, "bob", http.MethodPatch, "/v1/prompts/"+shared.PromptID, map[string]any{"title": "hijack"}); code != http.StatusForbidden {
		t.Fatalf("bob patch = %d, want 403", code)
	}
	if code, _ := f.do(t, "bob", http.MethodDelete, "/v1/prompts/"+shared.PromptID, nil); code != http.StatusForbidden {
		t.Fatalf("bob delete = %d, want 403", code)
	}
	if code, _ := f.do(t, "bob", http.MethodPatch, "/v1/prompts/"+private.PromptID, map[string]any{"title": "x"}); code != http.StatusNotFound {
		t.Fatalf("bob patch private = %d, want 404", code)
	}
	if code, _ := f.do(t, "bob", http.MethodPost, "/v1/prompts/"+private.PromptID+"/use", nil); code != http.StatusNotFound {
		t.Fatalf("bob use private = %d, want 404", code)
	}
	// The author edits; anyone who sees it may use it.
	code, body = f.do(t, "alice", http.MethodPatch, "/v1/prompts/"+shared.PromptID, map[string]any{"title": "Release notes v2"})
	if code != http.StatusOK || !strings.Contains(string(body), "Release notes v2") {
		t.Fatalf("alice patch = %d (%s)", code, body)
	}
	code, body = f.do(t, "bob", http.MethodPost, "/v1/prompts/"+shared.PromptID+"/use", nil)
	var used wire.Prompt
	_ = json.Unmarshal(body, &used)
	if code != http.StatusOK || used.UseCount != 1 {
		t.Fatalf("bob use = %d %+v, want useCount 1", code, used)
	}
	// Org-admin moderation (gateway-gated /v1/tenant/*) removes tenant prompts
	// but never another member's private one.
	if code, _ := f.do(t, "bob", http.MethodDelete, "/v1/tenant/prompts/"+private.PromptID, nil); code != http.StatusNotFound {
		t.Fatalf("moderate private = %d, want 404", code)
	}
	if code, _ := f.do(t, "bob", http.MethodDelete, "/v1/tenant/prompts/"+shared.PromptID, nil); code != http.StatusNoContent {
		t.Fatalf("moderate tenant prompt = %d, want 204", code)
	}
}

func TestPromptValidationAndSourceRoomMembership(t *testing.T) {
	f := newTeamFixture(t)
	for name, body := range map[string]map[string]any{
		"no title":       {"title": " ", "body": "x"},
		"no body":        {"title": "t", "body": "  "},
		"bad visibility": {"title": "t", "body": "x", "visibility": "public"},
	} {
		if code, _ := f.do(t, "alice", http.MethodPost, "/v1/prompts", body); code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, code)
		}
	}
	if code, _ := f.do(t, "alice", http.MethodPost, "/v1/prompts", map[string]any{"title": "t", "body": "x", "sourceRoomId": "room-2", "sourceSeq": 1}); code != http.StatusForbidden {
		t.Fatalf("foreign source room = %d, want 403", code)
	}
	if code, _ := f.do(t, "", http.MethodGet, "/v1/prompts", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", code)
	}
}

func TestTasksMembershipToggleAndTransientEvent(t *testing.T) {
	f := newTeamFixture(t)
	seq := f.chatAs(t, "alice", "room-1", "Ship the release\nwith notes")
	path := "/v1/rooms/room-1/tasks/" + itoaTest(seq)

	// A non-member can neither read nor mark.
	if code, _ := f.do(t, "carol", http.MethodGet, "/v1/rooms/room-1/tasks", nil); code != http.StatusForbidden {
		t.Fatalf("non-member list = %d, want 403", code)
	}
	if code, _ := f.do(t, "carol", http.MethodPut, path, map[string]any{"status": "open"}); code != http.StatusForbidden {
		t.Fatalf("non-member mark = %d, want 403", code)
	}
	// Unknown message and bad status.
	if code, _ := f.do(t, "bob", http.MethodPut, "/v1/rooms/room-1/tasks/999", map[string]any{"status": "open"}); code != http.StatusNotFound {
		t.Fatalf("missing message = %d, want 404", code)
	}
	if code, _ := f.do(t, "bob", http.MethodPut, path, map[string]any{"status": "later"}); code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, want 400", code)
	}

	sub := f.hub.SubscribeLive("room-1")
	defer f.hub.Unsubscribe("room-1", sub)

	// Any member marks (bob marks alice's message), then toggles done.
	code, body := f.do(t, "bob", http.MethodPut, path, map[string]any{"status": "open"})
	var task wire.MessageTask
	_ = json.Unmarshal(body, &task)
	if code != http.StatusOK || task.Title != "Ship the release" || task.MessageAuthor != "alice" || task.CreatedBy != "bob" || task.Status != "open" {
		t.Fatalf("mark = %d %+v", code, task)
	}
	code, body = f.do(t, "alice", http.MethodPut, path, map[string]any{"status": "done"})
	_ = json.Unmarshal(body, &task)
	if code != http.StatusOK || task.Status != "done" || task.DoneBy != "alice" || task.CreatedBy != "bob" {
		t.Fatalf("toggle done = %d %+v (creation snapshot must be kept)", code, task)
	}
	code, body = f.do(t, "bob", http.MethodGet, "/v1/rooms/room-1/tasks", nil)
	var list struct{ Tasks []wire.MessageTask }
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Tasks) != 1 || list.Tasks[0].Status != "done" {
		t.Fatalf("list = %d %+v", code, list.Tasks)
	}
	if code, _ := f.do(t, "bob", http.MethodDelete, path, nil); code != http.StatusNoContent {
		t.Fatalf("unmark = %d, want 204", code)
	}
	if code, _ := f.do(t, "bob", http.MethodDelete, path, nil); code != http.StatusNotFound {
		t.Fatalf("unmark again = %d, want 404", code)
	}

	// Three task_updated events reached live subscribers ...
	for i := 0; i < 3; i++ {
		ev := <-sub.Events()
		if ev.Kind != wire.EventTaskUpdated {
			t.Fatalf("event %d kind = %q, want task_updated", i, ev.Kind)
		}
	}
	// ... and none of them entered history.
	msgs, _ := f.chat.RoomHistory(tenantCtx(), "room-1", "", 0, 50)
	for _, m := range msgs {
		if m.Kind == wire.EventTaskUpdated {
			t.Fatal("task_updated must not be persisted")
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("history = %d rows, want only the chat message", len(msgs))
	}
}

func TestSearchScopesToMemberRooms(t *testing.T) {
	f := newTeamFixture(t)
	f.chatAs(t, "alice", "room-1", "deploy window is Friday")
	f.chatAs(t, "carol", "room-2", "deploy freeze in room two")
	deleted := f.chatAs(t, "alice", "room-1", "deploy draft to delete")
	if code, _ := f.do(t, "alice", http.MethodDelete, "/v1/rooms/room-1/messages/"+itoaTest(deleted), nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	f.chatAs(t, "bob", "room-1", "部署窗口在周五")
	f.do(t, "alice", http.MethodPost, "/v1/prompts", map[string]any{"title": "Deploy checklist", "body": "steps", "visibility": "tenant"})

	search := func(user, query string) wire.SearchResponse {
		t.Helper()
		code, body := f.do(t, user, http.MethodGet, "/v1/search?q="+query, nil)
		if code != http.StatusOK {
			t.Fatalf("search %q as %s = %d (%s)", query, user, code, body)
		}
		var out wire.SearchResponse
		_ = json.Unmarshal(body, &out)
		return out
	}
	res := search("alice", "DEPLOY")
	var msgs, prompts int
	for _, r := range res.Results {
		switch r.Kind {
		case wire.SearchMessage:
			msgs++
			if r.RoomID != "room-1" {
				t.Fatalf("alice got a hit from %s (not a member)", r.RoomID)
			}
			if strings.Contains(r.Snippet, "delete") {
				t.Fatal("soft-deleted message must not be searchable")
			}
			// The chat carried no client time: the server stamped one.
			if r.AtMs <= 0 {
				t.Fatalf("message hit atMs = %d, want the server-stamped time", r.AtMs)
			}
		case wire.SearchPrompt:
			prompts++
		}
	}
	if msgs != 1 || prompts != 1 {
		t.Fatalf("alice results = %+v, want 1 message + 1 prompt", res.Results)
	}
	// The rooms param only narrows: naming a foreign room yields nothing.
	code, body := f.do(t, "alice", http.MethodGet, "/v1/search?q=deploy&rooms=room-2", nil)
	var narrowed wire.SearchResponse
	_ = json.Unmarshal(body, &narrowed)
	if code != http.StatusOK || len(narrowed.Results) != 0 {
		t.Fatalf("foreign rooms filter = %d %+v, want no results", code, narrowed.Results)
	}
	// CJK substring.
	if res := search("alice", "%E7%AA%97%E5%8F%A3"); len(res.Results) != 1 || res.Results[0].Author != "bob" {
		t.Fatalf("CJK search = %+v", res.Results)
	}
	// Too-short queries are rejected.
	if code, _ := f.do(t, "alice", http.MethodGet, "/v1/search?q=d", nil); code != http.StatusBadRequest {
		t.Fatalf("short query = %d, want 400", code)
	}
}

func TestSnippetAround(t *testing.T) {
	long := strings.Repeat("x ", 100) + "needle" + strings.Repeat(" y", 100)
	got := snippetAround(long, "NEEDLE")
	if !strings.Contains(got, "needle") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet = %q", got)
	}
	if got := snippetAround("line one\nline two", "absent"); got != "line one line two" {
		t.Fatalf("no-match snippet = %q", got)
	}
}

func tenantCtx() context.Context { return tenant.NewContext(context.Background(), tenant.Default) }

func itoaTest(n int64) string { return strconv.FormatInt(n, 10) }
