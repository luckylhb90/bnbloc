package search

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/search/searchtest"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

const acme = "acme"

func acmeCtx() context.Context { return tenant.NewContext(context.Background(), acme) }

func chatRow(t *testing.T, chat *store.MemoryChat, tenantID, room string, seq int64, author, text string, at int64) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"userId": author, "text": text})
	if err := chat.AppendRoomMessages(context.Background(), []store.RoomMessage{{
		TenantID: tenantID, RoomID: room, Seq: seq, Kind: "chat_message", Author: author, Payload: payload, CreatedAtMs: at,
	}}); err != nil {
		t.Fatal(err)
	}
}

func newTestIndex(t *testing.T, fake *searchtest.Fake, chat store.ChatIndex, cfg OpenSearchConfig) *OpenSearch {
	t.Helper()
	cfg.URL = fake.URL
	o, err := NewOpenSearch(cfg, chat, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestNewOpenSearchValidatesConfig(t *testing.T) {
	chat := store.NewMemoryChat()
	for _, tc := range []struct {
		name string
		cfg  OpenSearchConfig
		want string
	}{
		{"scheme", OpenSearchConfig{URL: "ftp://search:9200"}, "not an http(s) URL"},
		{"no host", OpenSearchConfig{URL: "http://"}, "not an http(s) URL"},
		{"credentials in URL", OpenSearchConfig{URL: "https://maya:pw@search:9200"}, "not in the URL"},
		{"query", OpenSearchConfig{URL: "https://search:9200/?x=1"}, "query or fragment"},
		{"half credentials", OpenSearchConfig{URL: "https://search:9200", Username: "maya"}, "both a username and a password"},
		{"bad prefix", OpenSearchConfig{URL: "https://search:9200", IndexPrefix: "Bad*Prefix"}, "index prefix"},
	} {
		if _, err := NewOpenSearch(tc.cfg, chat, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	o, err := NewOpenSearch(OpenSearchConfig{URL: "https://search:9200/proxy/"}, chat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.IndexName() != "bnbloc-room-messages-v1" || o.base.Path != "/proxy" {
		t.Fatalf("index = %q, base path = %q", o.IndexName(), o.base.Path)
	}
	if o, _ := NewOpenSearch(OpenSearchConfig{URL: "http://s:9200", IndexPrefix: "acme-prod"}, chat, nil); o.IndexName() != "acme-prod-room-messages-v1" {
		t.Fatalf("prefixed index = %q", o.IndexName())
	}
}

// The index is created once with the explicit analysis and strict mapping,
// with basic auth on every request; an existing compatible index is reused.
func TestEnsureIndexCreatesExplicitMapping(t *testing.T) {
	fake := searchtest.New(t)
	fake.RequireAuth("svc-search", "s3cret")
	o := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{Username: "svc-search", Password: "s3cret", IndexPrefix: "t1"})
	if err := o.EnsureIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		if r.User != "svc-search" || r.Pass != "s3cret" {
			t.Fatalf("%s %s sent credentials %q/%q", r.Method, r.Path, r.User, r.Pass)
		}
	}
	var def struct {
		Settings struct {
			Analysis struct {
				Tokenizer map[string]struct {
					Type       string   `json:"type"`
					MinGram    int      `json:"min_gram"`
					MaxGram    int      `json:"max_gram"`
					TokenChars []string `json:"token_chars"`
				} `json:"tokenizer"`
				Analyzer map[string]struct {
					Tokenizer string   `json:"tokenizer"`
					Filter    []string `json:"filter"`
				} `json:"analyzer"`
			} `json:"analysis"`
		} `json:"settings"`
		Mappings struct {
			Dynamic    string `json:"dynamic"`
			Properties map[string]struct {
				Type     string `json:"type"`
				Analyzer string `json:"analyzer"`
				Format   string `json:"format"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(fake.Definition("t1-room-messages-v1"), &def); err != nil {
		t.Fatalf("definition: %v", err)
	}
	an := def.Settings.Analysis.Analyzer[textAnalyzer]
	tok := def.Settings.Analysis.Tokenizer[an.Tokenizer]
	if tok.Type != "ngram" || tok.MinGram != 2 || tok.MaxGram != 2 || strings.Join(tok.TokenChars, ",") != "letter,digit,punctuation,symbol" {
		t.Fatalf("tokenizer = %+v", tok)
	}
	if strings.Join(an.Filter, ",") != "cjk_width,lowercase,asciifolding" {
		t.Fatalf("filters = %v", an.Filter)
	}
	if def.Mappings.Dynamic != "strict" {
		t.Fatalf("dynamic = %q, want strict", def.Mappings.Dynamic)
	}
	for field, typ := range requiredFields {
		if got := def.Mappings.Properties[field].Type; got != typ {
			t.Errorf("field %s type = %q, want %q", field, got, typ)
		}
	}
	if p := def.Mappings.Properties["text"]; p.Analyzer != textAnalyzer {
		t.Fatalf("text analyzer = %q", p.Analyzer)
	}
	if p := def.Mappings.Properties["createdAt"]; p.Format != "epoch_millis" {
		t.Fatalf("createdAt format = %q", p.Format)
	}

	// A second instance (another replica) finds the index and creates nothing.
	fake.ResetRequests()
	o2 := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{Username: "svc-search", Password: "s3cret", IndexPrefix: "t1"})
	if err := o2.EnsureIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		if r.Method == http.MethodPut {
			t.Fatalf("existing index re-created: %+v", r)
		}
	}
}

func TestEnsureIndexRefusals(t *testing.T) {
	fake := searchtest.New(t)
	fake.RequireAuth("svc-search", "s3cret")
	wrong := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{Username: "svc-search", Password: "nope"})
	if err := wrong.EnsureIndex(context.Background()); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("wrong credentials: err = %v", err)
	}
	fake.RequireAuth("", "")
	fake.CreateIndexWithMapping("old-room-messages-v1", map[string]any{
		"text": map[string]any{"type": "text", "analyzer": "standard"},
	})
	old := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{IndexPrefix: "old"})
	if err := old.EnsureIndex(context.Background()); err == nil || !strings.Contains(err.Error(), "bnbloc search reindex") {
		t.Fatalf("incompatible mapping: err = %v", err)
	}
	// Not ready means every call re-probes, and fails without touching data.
	if _, err := old.Search(acmeCtx(), store.MessageSearch{RoomIDs: []string{"r"}, Text: "deploy"}); err == nil {
		t.Fatal("search on an unusable index succeeded")
	}
	down := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{})
	fake.FailWith(func(*http.Request) int { return http.StatusServiceUnavailable })
	if err := down.EnsureIndex(context.Background()); err == nil {
		t.Fatal("unreachable cluster: no error")
	}
	fake.FailWith(nil)
	if err := down.EnsureIndex(context.Background()); err != nil {
		t.Fatalf("recovered cluster: %v", err)
	}
}

// Apply writes each message's current store state: a document with the
// scope fields, deleted=true without text once soft-deleted, and a delete
// for a message gone from the store.
func TestApplyWritesStoreState(t *testing.T) {
	fake := searchtest.New(t)
	chat := store.NewMemoryChat()
	o := newTestIndex(t, fake, chat, OpenSearchConfig{})
	ctx := acmeCtx()
	chatRow(t, chat, acme, "room-1", 1, "maya", "The deploy window is Friday", 100)
	agent, _ := json.Marshal(map[string]any{"messages": []any{
		map[string]any{"payload": map[string]any{"text": "deploy script updated", "ownerUserId": "jack", "provider": "codex"}},
	}})
	if err := chat.AppendRoomMessages(ctx, []store.RoomMessage{{TenantID: acme, RoomID: "room-1", ChannelID: "ch-1", Seq: 2, Kind: "agent_messages", Payload: agent, CreatedAtMs: 200}}); err != nil {
		t.Fatal(err)
	}
	chatRow(t, chat, acme, "room-1", 3, "maya", "", 300) // attachment-only: no text
	keys := []store.MessageKey{{TenantID: acme, RoomID: "room-1", Seq: 1}, {TenantID: acme, RoomID: "room-1", Seq: 2}, {TenantID: acme, RoomID: "room-1", Seq: 3}, {TenantID: acme, RoomID: "room-1", Seq: 9}}
	if err := o.Apply(ctx, keys); err != nil {
		t.Fatal(err)
	}
	idx := o.IndexName()
	d := fake.Doc(idx, "acme/room-1/1")
	if d == nil || d["text"] != "The deploy window is Friday" || d["tenant"] != acme || d["room"] != "room-1" ||
		d["author"] != "maya" || d["kind"] != "chat_message" || d["deleted"] != false || d["createdAt"] != float64(100) || d["seq"] != float64(1) {
		t.Fatalf("doc 1 = %v", d)
	}
	if d := fake.Doc(idx, "acme/room-1/2"); d == nil || d["author"] != "jack/codex" || d["channel"] != "ch-1" || d["text"] != "deploy script updated" {
		t.Fatalf("agent doc = %v", d)
	}
	if fake.Doc(idx, "acme/room-1/3") != nil || fake.DocCount(idx) != 2 {
		t.Fatalf("a message without text got a document (%d docs)", fake.DocCount(idx))
	}

	if ok, err := chat.EditRoomMessage(ctx, "room-1", 1, "maya", "window moved to Monday", 400); err != nil || !ok {
		t.Fatalf("edit = %v %v", ok, err)
	}
	if err := o.Apply(ctx, keys[:1]); err != nil {
		t.Fatal(err)
	}
	if d := fake.Doc(idx, "acme/room-1/1"); d["text"] != "window moved to Monday" || d["editedAt"] != float64(400) {
		t.Fatalf("edited doc = %v", d)
	}
	if ok, err := chat.DeleteRoomMessage(ctx, "room-1", 1, "maya", 500); err != nil || !ok {
		t.Fatalf("delete = %v %v", ok, err)
	}
	if err := o.Apply(ctx, keys[:1]); err != nil {
		t.Fatal(err)
	}
	if d := fake.Doc(idx, "acme/room-1/1"); d["deleted"] != true || d["text"] != nil {
		t.Fatalf("soft-deleted doc = %v, want deleted=true and no text", d)
	}
	// Gone from the store (retention): the document is deleted.
	fake.PutDoc(idx, "acme/room-1/9", map[string]any{"tenant": acme, "room": "room-1", "seq": 9, "text": "ghost"})
	if err := o.Apply(ctx, keys[3:]); err != nil {
		t.Fatal(err)
	}
	if fake.Doc(idx, "acme/room-1/9") != nil {
		t.Fatal("document of a missing message survived")
	}
}

// The query carries every scope filter and the phrase match, newest first.
func TestSearchQueryShape(t *testing.T) {
	fake := searchtest.New(t)
	o := newTestIndex(t, fake, store.NewMemoryChat(), OpenSearchConfig{})
	if _, err := o.Search(acmeCtx(), store.MessageSearch{RoomIDs: []string{"room-1", "room-2"}, Text: "  deploy  ", BeforeMs: 900, Limit: 7}); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, r := range fake.Requests() {
		if strings.HasSuffix(r.Path, "/_search") {
			body = string(r.Body)
		}
	}
	var q map[string]any
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatalf("search body %q: %v", body, err)
	}
	want := `{"_source":["tenant","room","seq"],"query":{"bool":{"filter":[{"term":{"tenant":"acme"}},{"terms":{"room":["room-1","room-2"]}},{"term":{"deleted":false}},{"range":{"createdAt":{"lt":900}}}],"must":[{"match_phrase":{"text":"deploy"}}]}},"size":7,"sort":[{"createdAt":{"order":"desc"}},{"seq":{"order":"desc"}}],"track_total_hits":false}`
	if got, _ := json.Marshal(q); string(got) != want {
		t.Fatalf("search body:\n got %s\nwant %s", got, want)
	}
	// Unsearchable queries never reach the index; a missing tenant is an error.
	fake.ResetRequests()
	if got, err := o.Search(acmeCtx(), store.MessageSearch{RoomIDs: []string{"room-1"}, Text: "d"}); err != nil || got != nil {
		t.Fatalf("short query = %v %v", got, err)
	}
	if got, err := o.Search(acmeCtx(), store.MessageSearch{Text: "deploy"}); err != nil || got != nil {
		t.Fatalf("no rooms = %v %v", got, err)
	}
	if len(fake.Requests()) != 0 {
		t.Fatalf("unsearchable queries reached the index: %+v", fake.Requests())
	}
	if _, err := o.Search(context.Background(), store.MessageSearch{RoomIDs: []string{"room-1"}, Text: "deploy"}); err == nil {
		t.Fatal("search without a tenant succeeded")
	}
}

// The store stays the authority: even an index that ignores every scope
// filter (and holds stale documents) yields only rows the MySQL query
// would — the caller's tenant and rooms, live, older than the cursor — and
// a page thinned by stale candidates is refilled from the next page.
func TestSearchRechecksEveryCandidateInTheStore(t *testing.T) {
	fake := searchtest.New(t)
	chat := store.NewMemoryChat()
	m := obs.NewMetrics("test")
	o, err := NewOpenSearch(OpenSearchConfig{URL: fake.URL}, chat, m)
	if err != nil {
		t.Fatal(err)
	}
	ctx := acmeCtx()
	var keys []store.MessageKey
	add := func(tenantID, room string, seq int64, text string, at int64) {
		chatRow(t, chat, tenantID, room, seq, "maya", text, at)
		keys = append(keys, store.MessageKey{TenantID: tenantID, RoomID: room, Seq: seq})
	}
	add(acme, "room-1", 1, "deploy one", 100)
	add(acme, "room-1", 2, "deploy two", 200)
	add(acme, "room-2", 1, "deploy in a room maya left", 300) // not in the caller's rooms
	add("globex", "room-g", 1, "deploy at another tenant", 400)
	add(acme, "room-1", 3, "deploy three", 500)
	add(acme, "room-1", 4, "deploy four (deleted)", 600)
	add(acme, "room-1", 5, "deploy five (newest)", 700)
	if err := o.Apply(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	// Stale index: the delete and an edit never reached it.
	if ok, _ := chat.DeleteRoomMessage(ctx, "room-1", 4, "maya", 800); !ok {
		t.Fatal("delete failed")
	}
	if ok, _ := chat.EditRoomMessage(ctx, "room-1", 5, "maya", "deploy five, edited", 810); !ok {
		t.Fatal("edit failed")
	}
	fake.IgnoreFilters(true)

	seqs := func(q store.MessageSearch) []string {
		t.Helper()
		got, err := o.Search(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, m := range got {
			if m.TenantID != "" {
				t.Fatalf("result carries tenant %q (store reads leave it empty)", m.TenantID)
			}
			out = append(out, m.RoomID+"/"+itoa(m.Seq))
		}
		return out
	}
	if got := seqs(store.MessageSearch{RoomIDs: []string{"room-1"}, Text: "deploy", Limit: 3}); strings.Join(got, ",") != "room-1/5,room-1/3,room-1/2" {
		t.Fatalf("page 1 = %v, want the three newest admitted rows of room-1", got)
	}
	if got := seqs(store.MessageSearch{RoomIDs: []string{"room-1"}, Text: "deploy", BeforeMs: 500, Limit: 3}); strings.Join(got, ",") != "room-1/2,room-1/1" {
		t.Fatalf("before cursor = %v", got)
	}
	// The edit is served from the store row (the index text is stale).
	got, _ := o.Search(ctx, store.MessageSearch{RoomIDs: []string{"room-1"}, Text: "five", Limit: 1})
	if len(got) != 1 || store.MessageText(got[0].Kind, got[0].Payload) != "deploy five, edited" {
		t.Fatalf("edited hit = %+v", got)
	}
	if !strings.Contains(metricsText(m), `bnbloc_search_stale_candidates_total{service="test"}`) {
		t.Fatalf("stale candidates not counted:\n%s", metricsText(m))
	}
}

func TestPurgeBeforeAndReindex(t *testing.T) {
	fake := searchtest.New(t)
	chat := store.NewMemoryChat()
	o := newTestIndex(t, fake, chat, OpenSearchConfig{})
	chatRow(t, chat, acme, "room-1", 1, "maya", "old note", 100)
	chatRow(t, chat, acme, "room-1", 2, "maya", "new note", 900)
	chatRow(t, chat, "globex", "room-g", 1, "jack", "old elsewhere", 100)
	chatRow(t, chat, acme, "room-1", 3, "maya", "", 950)

	// Reindex walks every tenant in primary-key order, resumably.
	var cursors []string
	cursor, written := "", 0
	for i := 0; ; i++ {
		b, err := o.Reindex(context.Background(), cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		written += b.Updated
		cursor = b.Cursor
		cursors = append(cursors, cursor)
		if b.Done {
			break
		}
		if i > 5 {
			t.Fatal("reindex never finished")
		}
	}
	if written != 3 || fake.DocCount(o.IndexName()) != 3 || strings.Join(cursors, ",") != "room-1/2,room-g/1,room-g/1" {
		t.Fatalf("reindex wrote %d docs (%d stored), cursors %v", written, fake.DocCount(o.IndexName()), cursors)
	}
	// Idempotent: a second pass writes the same documents.
	if b, err := o.Reindex(context.Background(), "", 10); err != nil || b.Updated != 3 || fake.DocCount(o.IndexName()) != 3 {
		t.Fatalf("second pass = %+v %v", b, err)
	}

	fake.ResetRequests()
	n, err := o.PurgeBefore(context.Background(), acme, 500)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d %v, want 1", n, err)
	}
	if fake.Doc(o.IndexName(), "acme/room-1/1") != nil || fake.Doc(o.IndexName(), "globex/room-g/1") == nil || fake.Doc(o.IndexName(), "acme/room-1/2") == nil {
		t.Fatal("purge removed the wrong documents")
	}
	var req searchtest.Request
	for _, r := range fake.Requests() {
		if strings.HasSuffix(r.Path, "/_delete_by_query") {
			req = r
		}
	}
	if !strings.Contains(req.Query, "conflicts=proceed") || string(req.Body) != `{"query":{"bool":{"filter":[{"term":{"tenant":"acme"}},{"range":{"createdAt":{"lt":500}}}]}}}` {
		t.Fatalf("delete by query = %s ? %s", req.Body, req.Query)
	}
}

func TestStatus(t *testing.T) {
	fake := searchtest.New(t)
	chat := store.NewMemoryChat()
	o := newTestIndex(t, fake, chat, OpenSearchConfig{})
	chatRow(t, chat, acme, "room-1", 1, "maya", "note", 100)
	_ = o.Apply(context.Background(), []store.MessageKey{{TenantID: acme, RoomID: "room-1", Seq: 1}})
	st := o.Status(context.Background())
	if !st.Ready || st.Backend != BackendOpenSearch || st.Index != o.IndexName() || st.Health != "green" || st.Documents == nil || *st.Documents != 1 {
		t.Fatalf("status = %+v", st)
	}
	fake.FailWith(func(r *http.Request) int {
		if strings.HasSuffix(r.URL.Path, "/_count") {
			return http.StatusInternalServerError
		}
		return 0
	})
	if st := o.Status(context.Background()); st.Ready || st.Error == "" {
		t.Fatalf("failing status = %+v", st)
	}
}

// failingIndex counts Apply calls and fails (or blocks) them.
type failingIndex struct {
	Store
	calls atomic.Int64
	block chan struct{}
}

func (f *failingIndex) Apply(ctx context.Context, _ []store.MessageKey) error {
	f.calls.Add(1)
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
	return context.DeadlineExceeded
}

// A failing or stuck index never blocks a publisher: Enqueue returns at
// once, a full queue drops and counts, and a failed batch is retried, then
// counted as failed.
func TestUpdaterIsolatesFailures(t *testing.T) {
	m := obs.NewMetrics("test")
	idx := &failingIndex{block: make(chan struct{})}
	u := NewUpdater(idx, m)
	u.sleep = func(context.Context, time.Duration) bool { return true }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go u.Run(ctx)

	start := time.Now()
	rows := make([]store.RoomMessage, updateQueueSize+updateBatch+100)
	for i := range rows {
		rows[i] = store.RoomMessage{TenantID: acme, RoomID: "room-1", Seq: int64(i + 1)}
	}
	u.Committed(rows)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("enqueue with a stuck index took %s", d)
	}
	waitFor(t, func() bool { return u.Stats().Dropped > 0 })
	close(idx.block)
	waitFor(t, func() bool { st := u.Stats(); return st.Queued == 0 && st.Failed+st.Dropped == int64(len(rows)) })
	if st := u.Stats(); st.Applied != 0 {
		t.Fatalf("stats = %+v", st)
	}
	// Every batch was tried updateAttempts times.
	if calls := idx.calls.Load(); calls%updateAttempts != 0 {
		t.Fatalf("apply calls = %d, want a multiple of %d", calls, updateAttempts)
	}
	text := metricsText(m)
	for _, want := range []string{`bnbloc_search_index_updates_total{service="test",result="dropped"}`, `bnbloc_search_index_updates_total{service="test",result="error"}`} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics lack %s:\n%s", want, text)
		}
	}
	// A nil updater (the store index) ignores everything.
	var none *Updater
	none.Enqueue(store.MessageKey{})
	none.Committed(rows)
	if none.Stats() != nil {
		t.Fatal("nil updater has stats")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func metricsText(m *obs.Metrics) string {
	rec := &recorder{header: http.Header{}}
	m.Handler()(rec, &http.Request{})
	return rec.body.String()
}

type recorder struct {
	header http.Header
	body   strings.Builder
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *recorder) WriteHeader(int)             {}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
