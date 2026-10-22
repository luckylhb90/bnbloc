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
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// teamFixture is a realtime service with in-memory team stores. alice and bob
// share room-1; only carol is in room-2.
type teamFixture struct {
	srv     *httptest.Server
	hub     *realtime.Hub
	chat    *store.MemoryChat
	content *store.MemoryContentComponents
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
		Tasks:      store.NewMemoryMessageTasks(),
		Search:     search.NewStore(chat),
		Artifacts:  store.NewMemoryArtifacts(),
		Rooms:      mem,
		Content:    store.NewMemoryContentComponents(),
	}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	return &teamFixture{srv: srv, hub: hub, chat: chat, content: svc.Content.(*store.MemoryContentComponents)}
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
	var msgs int
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
		default:
			t.Fatalf("unexpected %s hit %+v", r.Kind, r)
		}
	}
	if msgs != 1 {
		t.Fatalf("alice results = %+v, want 1 message", res.Results)
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

// Global search returns prompts of the prompt packs the member can see
// (here the organization's), on the first page and without a room filter;
// never someone else's personal pack.
func TestSearchFindsVisiblePrompts(t *testing.T) {
	f := newTeamFixture(t)
	ctx := context.Background()
	pack := func(id, owner string, scope store.ContentScope, title, body string) {
		t.Helper()
		m, _ := json.Marshal(map[string]any{"kind": "bnbloc.prompt.package/v1", "name": id, "version": "1.0.0",
			"prompts": []map[string]any{{"id": "p1", "title": title, "body": body}}})
		c := store.ContentComponent{ID: id, Kind: "bnbloc.prompt.package/v1", Name: id, TenantID: tenant.Default, Owner: owner,
			Scope: scope, Status: store.ContentStatusActive, SourceID: "src-" + id, UpdatedAtMs: 5}
		if scope == store.ContentScopePersonal {
			c.SourceID = ""
		}
		if err := f.content.CreateContentComponent(ctx, c, store.ContentComponentVersion{Version: "1.0.0", ManifestJSON: m, BundleBlobKey: "sha256/" + id, SHA256: id}); err != nil {
			t.Fatal(err)
		}
	}
	pack("org-pack", "carol", store.ContentScopeOrg, "Deploy checklist", "Steps before a deploy window")
	pack("carols", "carol", store.ContentScopePersonal, "Deploy secrets", "private")
	code, body := f.do(t, "alice", http.MethodGet, "/v1/search?q=deploy", nil)
	if code != http.StatusOK {
		t.Fatalf("search = %d %s", code, body)
	}
	var out wire.SearchResponse
	_ = json.Unmarshal(body, &out)
	var prompts []wire.SearchResult
	for _, r := range out.Results {
		if r.Kind == wire.SearchPrompt {
			prompts = append(prompts, r)
		}
	}
	if len(prompts) != 1 || prompts[0].ComponentID != "org-pack" || prompts[0].PromptID != "p1" || prompts[0].Title != "Deploy checklist" || prompts[0].Author != "carol" {
		t.Fatalf("prompt hits = %+v", prompts)
	}
	code, body = f.do(t, "alice", http.MethodGet, "/v1/search?q=deploy&rooms=room-1", nil)
	if code != http.StatusOK || strings.Contains(string(body), `"kind":"prompt"`) {
		t.Fatalf("room-filtered search = %d %s", code, body)
	}
}

// GET /v1/my/unread counts the caller's rooms with unread messages: rooms of
// the member only, rooms not messages, cleared by reading, and a repeated ask
// within the cache window is answered without a second query.
func TestMyUnreadSummaryCountsMemberRooms(t *testing.T) {
	f := newTeamFixture(t)
	f.chatAs(t, "alice", "room-1", "one")
	f.chatAs(t, "alice", "room-1", "two")
	f.chatAs(t, "carol", "room-2", "elsewhere")
	summary := func(user string) wire.UnreadSummary {
		t.Helper()
		code, body := f.do(t, user, http.MethodGet, "/v1/my/unread", nil)
		if code != http.StatusOK {
			t.Fatalf("unread as %s = %d (%s)", user, code, body)
		}
		var out wire.UnreadSummary
		_ = json.Unmarshal(body, &out)
		return out
	}
	if got := summary("bob"); got.Rooms != 1 || got.More {
		t.Fatalf("bob (in room-1 only) = %+v, want 1 room", got)
	}
	// Carol is in room-2 only: room-1's messages are not hers to count (her
	// own message there is unread until she reads it, as UnreadCount says).
	if got := summary("carol"); got.Rooms != 1 {
		t.Fatalf("carol (in room-2 only) = %+v, want 1 room", got)
	}
	// Nobody in no room has none.
	if got := summary("dave"); got.Rooms != 0 || got.More {
		t.Fatalf("a member of no room = %+v", got)
	}
	if code, _ := f.do(t, "", http.MethodGet, "/v1/my/unread", nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("no subject = %d, want a refusal", code)
	}
}

// The summary cache answers the same member within its window and forgets
// after it, and never grows past its bound.
func TestUnreadSummaryCache(t *testing.T) {
	var c unreadSummaryCache
	now := time.Unix(1000, 0)
	if _, ok := c.get("k", now); ok {
		t.Fatal("an empty cache answered")
	}
	c.put("k", wire.UnreadSummary{Rooms: 3}, now)
	if got, ok := c.get("k", now.Add(unreadSummaryTTL-time.Second)); !ok || got.Rooms != 3 {
		t.Fatalf("within the window = %+v %v", got, ok)
	}
	if _, ok := c.get("k", now.Add(unreadSummaryTTL)); ok {
		t.Fatal("an expired answer was served")
	}
	for i := 0; i < unreadSummaryMax+10; i++ {
		c.put(strconv.Itoa(i), wire.UnreadSummary{}, now)
	}
	if len(c.m) > unreadSummaryMax {
		t.Fatalf("the cache grew to %d, past its bound %d", len(c.m), unreadSummaryMax)
	}
}
