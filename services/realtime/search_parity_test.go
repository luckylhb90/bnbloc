package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/retention"
	"github.com/luckylhb90/bnbloc/internal/search"
	"github.com/luckylhb90/bnbloc/internal/search/searchtest"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Search parity (docs/prd/2026-10-10-enterprise-scale.md §4): the same
// queries return the same visible results through the store's index and
// through OpenSearch, including the membership filter (only the caller's
// human memberships; an agent host's rooms are not searchable by it) and
// the people chat (visible to the room's humans, labeled as such).
//
// The store is the memory store, or MySQL with BNBLOC_TEST_MYSQL_DSN. The
// OpenSearch side is the in-process fake, or a real cluster with
// BNBLOC_TEST_OPENSEARCH_URL (e.g. opensearchproject/opensearch:2,
// single-node, security plugin disabled). Every write goes through the
// OpenSearch-backed service's real path (group commit hook, edit and delete
// handlers, updater); the store-backed service reads the same rows.

type parityRig struct {
	t        *testing.T
	tenantID string
	run      string
	backends *store.Backends
	idx      *search.OpenSearch
	updates  *search.Updater
	osURL    string
	fake     *searchtest.Fake
	writer   *httptest.Server // OpenSearch-backed
	reader   *httptest.Server // store-backed
	enqueued int64
}

func newParityRig(t *testing.T) *parityRig {
	t.Helper()
	backends, err := store.NewBackends(os.Getenv("BNBLOC_TEST_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if backends.DB != nil {
		t.Cleanup(func() { _ = backends.DB.Close() })
	}
	r := &parityRig{t: t, backends: backends, run: strconv.FormatInt(time.Now().UnixNano(), 36)}
	r.tenantID = "acme-" + r.run
	r.osURL = os.Getenv("BNBLOC_TEST_OPENSEARCH_URL")
	if r.osURL == "" {
		r.fake = searchtest.New(t)
		r.osURL = r.fake.URL
	}
	r.idx, err = search.NewOpenSearch(search.OpenSearchConfig{URL: r.osURL, IndexPrefix: "bnbloc-test-" + r.run}, backends.Chat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.idx.EnsureIndex(context.Background()); err != nil {
		t.Fatalf("ensure index: %v", err)
	}
	if r.fake == nil {
		t.Cleanup(func() {
			req, _ := http.NewRequest(http.MethodDelete, r.osURL+"/"+r.idx.IndexName(), nil)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		})
	}
	r.updates = search.NewUpdater(r.idx, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.updates.Run(ctx)
	return r
}

func (r *parityRig) room(name string) string { return name + "-" + r.run }

func (r *parityRig) ctx() context.Context {
	return tenant.NewContext(context.Background(), r.tenantID)
}

// serve starts the two services over the shared stores.
func (r *parityRig) serve(mem *membership.Static) {
	writerHub := realtime.NewHub(64, 16)
	writerHub.Committed = r.updates.Committed
	writer := &Service{Hub: writerHub, Membership: mem, Rooms: mem, Chat: r.backends.Chat, Channels: r.backends.Channel,
		Search: r.idx, SearchUpdates: r.updates}
	reader := &Service{Hub: realtime.NewHub(64, 16), Membership: mem, Rooms: mem, Chat: r.backends.Chat, Channels: r.backends.Channel,
		Search: search.NewStore(r.backends.Chat)}
	r.writer = httptest.NewServer(writer.Handler())
	r.reader = httptest.NewServer(reader.Handler())
	r.t.Cleanup(r.writer.Close)
	r.t.Cleanup(r.reader.Close)
}

func (r *parityRig) do(srv *httptest.Server, user, method, path string, body any) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("X-Bnbloc-User", user)
	req.Header.Set("X-Bnbloc-Tenant", r.tenantID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// chat posts through the OpenSearch-backed service and returns the seq.
func (r *parityRig) chat(user, room, channel, text string, at int64) int64 {
	r.t.Helper()
	code, body := r.do(r.writer, user, http.MethodPost, "/v1/rooms/"+room+"/chat",
		map[string]any{"text": text, "channelId": channel, "atUnixMs": at})
	if code != http.StatusOK {
		r.t.Fatalf("chat %q = %d (%s)", text, code, body)
	}
	r.enqueued++
	var out struct{ Sequence int64 }
	_ = json.Unmarshal(body, &out)
	return out.Sequence
}

// agentReply publishes an agent reply the way activity does.
func (r *parityRig) agentReply(room, channel, text string, at int64) {
	r.t.Helper()
	code, body := r.do(r.writer, svcauth.ServiceSubject("activity"), http.MethodPost, "/internal/v1/rooms/"+room+"/publish", map[string]any{
		"kind": "agent_messages", "channelId": channel, "atUnixMs": at,
		"payload": map[string]any{"messages": []any{map[string]any{"payload": map[string]any{"text": text, "ownerUserId": "jack", "provider": "codex"}}}},
	})
	if code != http.StatusOK {
		r.t.Fatalf("agent reply = %d (%s)", code, body)
	}
	r.enqueued++
}

// settle waits until every queued update is applied and visible.
func (r *parityRig) settle() {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st := r.updates.Stats()
		if st.Failed > 0 || st.Dropped > 0 {
			r.t.Fatalf("index updates failed: %+v", st)
		}
		if st.Queued == 0 && st.Applied >= r.enqueued {
			break
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("index never caught up: %+v, want %d applied", st, r.enqueued)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.idx.Refresh(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

// search runs one query on both services, fails on any difference, and
// returns the (shared) answer.
func (r *parityRig) search(user, query string) wire.SearchResponse {
	r.t.Helper()
	var answers [2]wire.SearchResponse
	for i, srv := range []*httptest.Server{r.reader, r.writer} {
		code, body := r.do(srv, user, http.MethodGet, "/v1/search?"+query, nil)
		if code != http.StatusOK {
			r.t.Fatalf("search %q as %s on %s = %d (%s)", query, user, []string{"store", "opensearch"}[i], code, body)
		}
		if err := json.Unmarshal(body, &answers[i]); err != nil {
			r.t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(answers[0], answers[1]) {
		r.t.Fatalf("search %q as %s differs:\n store:      %+v\n opensearch: %+v", query, user, answers[0], answers[1])
	}
	return answers[0]
}

func snippets(res wire.SearchResponse) []string {
	out := []string{}
	for _, h := range res.Results {
		out = append(out, h.Snippet)
	}
	return out
}

func TestSearchParityStoreAndOpenSearch(t *testing.T) {
	r := newParityRig(t)
	main, other, hosted := r.room("sp-main"), r.room("sp-other"), r.room("sp-hosted")
	mem := membership.NewStatic()
	mem.Add(main, "maya")
	mem.Add(main, "jack")
	mem.Add(other, "jack")
	mem.Add(hosted, "maya")
	// host-1 serves a platform agent in main and hosted: an agent-host
	// membership, never a human one, so search never covers those rooms.
	_ = mem.PutAgentHost(r.ctx(), r.tenantID, main, "host-1")
	_ = mem.PutAgentHost(r.ctx(), r.tenantID, hosted, "host-1")
	ctx := r.ctx()
	for _, room := range []string{main, other, hosted} {
		if err := r.backends.Channel.PutChannel(ctx, store.RoomChannel{ChannelID: "ch-" + room, TenantID: r.tenantID, RoomID: room,
			IsDefault: true, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	people, err := r.backends.Channel.EnsurePeopleChannel(ctx, main, "maya", 2)
	if err != nil {
		t.Fatal(err)
	}
	r.serve(mem)

	// Old enough for the retention step below (31 days is the minimum).
	base := time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
	at := func(i int) int64 { return base + int64(i)*1000 }
	r.chat("maya", main, "", "The deploy window is Friday", at(1))
	r.chat("jack", main, "", "部署窗口在周五", at(2))
	r.chat("maya", main, people.ChannelID, "people-only deploy plan", at(3))
	r.agentReply(main, "", "I updated the deploy script", at(4))
	r.chat("jack", other, "", "deploy freeze in room two", at(5))
	draft := r.chat("maya", main, "", "deploy draft to delete", at(6))
	typo := r.chat("maya", main, "", "deploy notse", at(7))
	r.chat("maya", main, "", "data pipeline is ready", at(8))
	for i := 0; i < 3; i++ {
		r.chat("jack", main, "", fmt.Sprintf("deploy batch %d", i), at(9+i))
	}
	r.chat("maya", hosted, "", "deploy in the hosted room", at(12))
	if code, body := r.do(r.writer, "maya", http.MethodDelete, "/v1/rooms/"+main+"/messages/"+strconv.FormatInt(draft, 10), nil); code != http.StatusOK {
		t.Fatalf("delete = %d (%s)", code, body)
	}
	if code, body := r.do(r.writer, "maya", http.MethodPatch, "/v1/rooms/"+main+"/messages/"+strconv.FormatInt(typo, 10), map[string]any{"text": "deploy notes, final"}); code != http.StatusOK {
		t.Fatalf("edit = %d (%s)", code, body)
	}
	r.enqueued += 2
	r.settle()

	res := r.search("maya", "q=deploy")
	var people1, agent, foreign int
	for _, h := range res.Results {
		switch {
		case h.RoomID == other:
			foreign++
		case h.ChannelKind == "people":
			people1++
		case h.Author == "jack/codex":
			agent++
		}
		if strings.Contains(h.Snippet, "draft") || strings.Contains(h.Snippet, "notse") {
			t.Fatalf("deleted or pre-edit text found: %q", h.Snippet)
		}
	}
	if len(res.Results) != 8 || people1 != 1 || agent != 1 || foreign != 0 {
		t.Fatalf("maya deploy = %v", snippets(res))
	}
	if got := r.search("jack", "q=deploy"); len(got.Results) != 8 {
		t.Fatalf("jack deploy = %v (main without the hosted room, plus other)", snippets(got))
	}
	for _, q := range []struct{ user, query string }{
		{"maya", "q=deploy&rooms=" + other}, // the filter only narrows
		{"host-1", "q=deploy"},              // agent-host memberships are not searchable
		{"zed", "q=deploy"},                 // not a member anywhere
		{"maya", "q=draft"},                 // deleted
		{"maya", "q=notse"},                 // edited away
	} {
		if got := r.search(q.user, q.query); len(got.Results) != 0 {
			t.Fatalf("%s %q = %v, want nothing", q.user, q.query, snippets(got))
		}
	}
	if got := r.search("maya", "q="+url.QueryEscape("窗口")); len(got.Results) != 1 || got.Results[0].Author != "jack" {
		t.Fatalf("CJK = %+v", got.Results)
	}
	if got := r.search("maya", "q=final"); len(got.Results) != 1 || got.Results[0].Snippet != "deploy notes, final" {
		t.Fatalf("edited = %+v", got.Results)
	}
	if got := r.search("jack", "q=people-only"); len(got.Results) != 1 || got.Results[0].ChannelKind != "people" {
		t.Fatalf("people chat hit = %+v", got.Results)
	}
	r.search("maya", "q=data")
	r.search("maya", "q=deploy&rooms="+main+","+hosted)
	// Paging walks the same pages on both backends.
	pages, before := 0, int64(0)
	for {
		q := "q=deploy&limit=2"
		if before > 0 {
			q += "&before=" + strconv.FormatInt(before, 10)
		}
		page := r.search("maya", q)
		pages++
		if page.NextBefore == 0 {
			break
		}
		before = page.NextBefore
		if pages > 10 {
			t.Fatal("paging never ended")
		}
	}
	if pages != 5 {
		t.Fatalf("pages = %d, want 5 (8 hits by 2, then an empty page)", pages)
	}

	// Retention deletes the rows and the documents together.
	if err := r.backends.Tenant.PutTenant(context.Background(), store.Tenant{TenantID: r.tenantID, Name: "Acme", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	thirtyOne := 31
	if _, err := r.backends.Tenant.SetTenantRetention(context.Background(), r.tenantID, wire.RetentionPolicy{MessagesDays: &thirtyOne}); err != nil {
		t.Fatal(err)
	}
	w := &retention.Worker{Store: store.NewRetention(r.backends), Tenants: r.backends.Tenant, Settings: r.backends.Settings, Search: r.idx}
	var rep retention.Report
	for i := 0; i < 50 && !rep.Ran; i++ { // another test may hold the lock for a moment
		if rep, err = w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !rep.Ran {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !rep.Ran || rep.Deleted[store.PurgedSearchDocuments] == 0 {
		t.Fatalf("retention = %+v", rep)
	}
	if err := r.idx.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.search("maya", "q=deploy"); len(got.Results) != 0 {
		t.Fatalf("after retention = %v", snippets(got))
	}
	if n := r.tenantDocs(); n != 0 {
		t.Fatalf("%d documents of the purged tenant are left in the index", n)
	}
}

// tenantDocs counts the rig tenant's documents in the index, bypassing the
// store re-check.
func (r *parityRig) tenantDocs() int64 {
	r.t.Helper()
	body, _ := json.Marshal(map[string]any{"query": map[string]any{"term": map[string]any{"tenant": r.tenantID}}})
	if r.fake != nil {
		return int64(r.fake.DocCount(r.idx.IndexName()))
	}
	resp, err := http.Post(r.osURL+"/"+r.idx.IndexName()+"/_count", "application/json", bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct{ Count int64 }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Count
}

// An unusable index never touches the write path: chat, edits and deletes
// succeed while every index call fails (counted), and the search route
// reports the failure as a logged 500 instead of an empty answer.
func TestSearchIndexFailureNeverFailsTheWrite(t *testing.T) {
	fake := searchtest.New(t)
	chat := store.NewMemoryChat()
	idx, err := search.NewOpenSearch(search.OpenSearchConfig{URL: fake.URL}, chat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.EnsureIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.FailWith(func(*http.Request) int { return http.StatusInternalServerError })
	updates := search.NewUpdater(idx, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go updates.Run(ctx)
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	hub := realtime.NewHub(16, 8)
	hub.Committed = updates.Committed
	svc := &Service{Hub: hub, Membership: mem, Rooms: mem, Chat: chat, Search: idx, SearchUpdates: updates}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	f := &teamFixture{srv: srv, hub: hub, chat: chat}

	start := time.Now()
	seq := f.chatAs(t, "maya", "room-1", "deploy while the index is down")
	if code, _ := f.do(t, "maya", http.MethodPatch, "/v1/rooms/room-1/messages/"+itoaTest(seq), map[string]any{"text": "edited while down"}); code != http.StatusOK {
		t.Fatalf("edit = %d", code)
	}
	if code, _ := f.do(t, "maya", http.MethodDelete, "/v1/rooms/room-1/messages/"+itoaTest(seq), nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("writes took %s with the index down", d)
	}
	deadline := time.Now().Add(15 * time.Second)
	for updates.Stats().Failed == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("failures not counted: %+v", updates.Stats())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _ := f.do(t, "maya", http.MethodGet, "/v1/search?q=deploy", nil); code != http.StatusInternalServerError {
		t.Fatalf("search with the index down = %d, want 500", code)
	}
	code, body := f.do(t, "root", http.MethodGet, "/v1/platform/search/status", nil)
	var st wire.SearchStatus
	_ = json.Unmarshal(body, &st)
	if code != http.StatusOK || st.Ready || st.Error == "" || st.Updates == nil || st.Updates.Failed == 0 {
		t.Fatalf("status = %d %+v", code, st)
	}
}
