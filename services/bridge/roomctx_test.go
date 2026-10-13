package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corepkg "github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
)

// pullGateway serves everything the pull-context flow fetches: channels,
// per-channel history, artifacts, and the activity ingest endpoints the drive
// path posts to.
type pullGateway struct {
	mu        sync.Mutex
	history   map[string]string // channel -> rows json (pre-rendered pages)
	artifacts string
	// versions / runs answer an artifact version and a run record by
	// "<artifactId>/<version>" and job id; library is the granted member
	// catalog, libraryQueries the query of each catalog read.
	versions       map[string]string
	contents       map[string]string // a version's bytes, by "<artifactId>/<version>"
	runs           map[string]string
	library        string
	libraryQueries []string
	// appStats answers an app's usage by artifact id; appQueries is the
	// query of each usage read.
	appStats   map[string]string
	appQueries []string
	// attachments are the room's chat attachments by id; downloads counts
	// each one's byte reads; reports are the bodies posted to
	// agent-activity/messages (notices among them).
	attachments map[string]testAttachment
	downloads   map[string]int
	reports     []string
	srv         *httptest.Server
}

// testAttachment is a chat attachment the test gateway serves.
type testAttachment struct {
	name, contentType string
	data              []byte
}

func newPullGateway(t *testing.T) *pullGateway {
	t.Helper()
	g := &pullGateway{history: map[string]string{}, versions: map[string]string{}, contents: map[string]string{}, runs: map[string]string{}, appStats: map[string]string{},
		attachments: map[string]testAttachment{}, downloads: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/rooms/r1/channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"channels":[{"channelId":"general"},{"channelId":"test"}]}`)
	})
	mux.HandleFunc("GET /v1/rooms/r1/history", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		// Mirrors the realtime service: an omitted channelId resolves to the
		// room's default channel.
		ch := r.URL.Query().Get("channelId")
		if ch == "" {
			ch = "general"
		}
		body, ok := g.history[ch]
		if !ok {
			body = `{"messages":[]}`
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /v1/rooms/r1/artifacts", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		fmt.Fprint(w, g.artifacts)
	})
	mux.HandleFunc("GET /v1/rooms/r1/artifacts/{id}/versions/{v}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		body, ok := g.versions[r.PathValue("id")+"/"+r.PathValue("v")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /v1/rooms/r1/artifacts/{id}/versions/{v}/content", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		body, ok := g.contents[r.PathValue("id")+"/"+r.PathValue("v")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /v1/rooms/r1/jobs/{job}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		body, ok := g.runs[r.PathValue("job")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("GET /v1/components", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.libraryQueries = append(g.libraryQueries, r.URL.RawQuery)
		if g.library == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, g.library)
	})
	mux.HandleFunc("GET /v1/rooms/r1/apps/{id}/stats", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.appQueries = append(g.appQueries, r.URL.RawQuery)
		body, ok := g.appStats[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("POST /v1/rooms/r1/agent-activity/session-state", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /v1/rooms/r1/agent-activity/messages", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.reports = append(g.reports, string(body))
		g.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("GET /v1/rooms/r1/attachments/{id}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		a, ok := g.attachments[r.PathValue("id")]
		g.downloads[r.PathValue("id")]++
		g.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a.contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", a.name))
		_, _ = w.Write(a.data)
	})
	mux.HandleFunc("GET /v1/rooms/r1/attachments/{id}/meta", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		a, ok := g.attachments[r.PathValue("id")]
		g.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"filename":%q,"contentType":%q,"size":%d}`, r.PathValue("id"), a.name, a.contentType, len(a.data))
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *pullGateway) setHistory(ch string, seqs []int64, texts map[int64]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Mirror the real API: newest `limit` below the cursor, ASCENDING order.
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i, seq := range seqs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"sequence":%d,"kind":"chat_message","payload":{"userId":"jack","text":%q}}`, seq, texts[seq])
	}
	b.WriteString(`]}`)
	g.history[ch] = b.String()
}

// addChatRow appends a chat_message row (a person's line) to the channel's
// page. Rows must be added in ascending seq order.
func (g *pullGateway) addChatRow(ch string, seq int64, user, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body := g.history[ch]
	if body == "" {
		body = `{"messages":[]}`
	}
	trimmed := strings.TrimSuffix(body, "]}")
	if !strings.HasSuffix(trimmed, "[") {
		trimmed += ","
	}
	body = trimmed + fmt.Sprintf(`{"sequence":%d,"kind":"chat_message","payload":{"userId":%q,"text":%q}}`, seq, user, text) + `]}`
	g.history[ch] = body
}

// addAgentRow appends an agent_messages row (speaker owner/provider) to the
// channel's page — the shape DecodeRow produces agent replies from.
// Rows must be added in ascending seq order.
func (g *pullGateway) addAgentRow(ch string, seq int64, owner, provider, text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	body := g.history[ch]
	if body == "" {
		body = `{"messages":[]}`
	}
	// Trim the closing "]}"; if the array is empty ("[" + "]}") keep the "[".
	trimmed := strings.TrimSuffix(body, "]}")
	if !strings.HasSuffix(trimmed, "[") {
		trimmed += ","
	}
	body = trimmed + fmt.Sprintf(`{"sequence":%d,"kind":"agent_messages","payload":{"messages":[{"payload":{"role":"assistant","text":%q,"ownerUserId":%q,"provider":%q}}]}}`, seq, text, owner, provider) + `]}`
	g.history[ch] = body
}

// A fresh session under a pull-capable core gets a POINTER prompt: no history
// inline, .room/ materialized into the workspace first, INDEX written last.
func TestDriveClaimPullWritesRoomFilesAndPointerPrompt(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1, 3}, map[int64]string{1: "first in general", 3: "third in general"})
	g.setHistory("test", []int64{2, 4}, map[int64]string{2: "first in test", 4: "second in test"})
	g.mu.Lock()
	// The draft another agent (maya/opencode on core acme) published from its
	// own workspace, and an expert it saved to the requester's Library.
	g.artifacts = `{"artifacts":[{"artifactId":"artf_draft","title":"drafts/weekly-report-assistant/component.json","kind":"code","currentVersion":2,"createdBy":"maya"}]}`
	g.versions["artf_draft/2"] = `{"version":{"artifactId":"artf_draft","version":2,"sourceJobId":"job-a","authorUserId":"maya"}}`
	g.runs["job-a"] = `{"jobId":"job-a","agent":{"owner":"maya","core":"acme","provider":"opencode"}}`
	g.library = `{"components":[],"contentComponents":[{"id":"cc_1","kind":"expert","name":"weekly-report-assistant","scope":"personal","latestVersion":"1.0.0","mine":true,"updatedAtMs":1760260000000}]}`
	g.mu.Unlock()

	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})

	_, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "write hello.rs", "ch"), nil)
	if err != nil {
		t.Fatalf("driveClaim: %v", err)
	}

	writes := prov.recordedWrites()
	if len(writes) == 0 {
		t.Fatal("no .room files written")
	}
	if writes[len(writes)-1] != ".room/INDEX.md" {
		t.Fatalf("INDEX must be written last, got order %v", writes)
	}
	var sawChunk, sawRecent bool
	for _, w := range writes {
		switch {
		case strings.HasPrefix(w, ".room/chunk-"):
			sawChunk = true
		case w == ".room/recent.md":
			sawRecent = true
		}
	}
	if !sawChunk || !sawRecent {
		t.Fatalf("missing .room files: %v", writes)
	}
	// The records: the room model, each deliverable with the agent that
	// produced it (not a file of this workspace), the requester's Library
	// read with the job's grant.
	written := map[string]string{}
	for _, w := range prov.roomFileWrites() {
		written[w.path] = w.content
	}
	for file, want := range map[string]string{
		".room/README.md":    "Every agent works in its own workspace",
		".room/artifacts.md": "drafts/weekly-report-assistant/component.json (code) v2, id artf_draft: from maya/opencode on core acme, whose source is in that agent's workspace;",
		".room/library.md":   "weekly-report-assistant · expert · v1.0.0 · personal, theirs",
	} {
		if !strings.Contains(written[file], want) {
			t.Errorf("%s lacks %q:\n%s", file, want, written[file])
		}
	}
	g.mu.Lock()
	queries := append([]string(nil), g.libraryQueries...)
	g.mu.Unlock()
	if len(queries) != 1 || queries[0] != "job=job-x&room=r1" {
		t.Errorf("Library reads = %q, want one with the job's grant", queries)
	}

	prompts := prov.recordedPrompts()
	if len(prompts) == 0 {
		t.Fatal("no session prompt recorded")
	}
	prompt := prompts[0]
	// The session's first prompt: its guidance (the .room/ legend included:
	// the core takes workspace files), then the pointer, which keeps one
	// line to the records on a core without agentProfile.
	facts, rest, ok := cutGuidance(prompt)
	if !ok || !facts.HasRoomFiles || !strings.HasSuffix(rest, "---\n\n(room records: .room/INDEX.md)\n\n[now]\nwrite hello.rs") {
		t.Fatalf("pointer prompt malformed:\n%s", prompt)
	}
	// The whole point: history must NOT be in the prompt.
	for _, leaked := range []string{"first in general", "second in test", "story so far"} {
		if strings.Contains(prompt, leaked) {
			t.Fatalf("history leaked into pointer prompt (%q):\n%s", leaked, prompt)
		}
	}
}

// A follow-up turn on the same session refreshes .room/ (delta chunks) and
// still drives with a pointer prompt.
func TestDriveClaimPullDeltaOnReuse(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1}, map[int64]string{1: "old message"})
	g.setHistory("test", []int64{}, nil)
	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "turn one", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	firstWrites := len(prov.recordedWrites())

	// New messages arrive before turn two.
	g.setHistory("general", []int64{1, 5}, map[int64]string{1: "old message", 5: "new message"})
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "turn two", "ch"), nil); err != nil {
		t.Fatal(err)
	}

	all := prov.recordedWrites()
	if len(all) <= firstWrites {
		t.Fatalf("reuse wrote nothing new: %v", all)
	}
	// The delta must land in a chunk covering seq 5.
	var sawDelta bool
	for _, w := range all[firstWrites:] {
		if strings.Contains(w, "-0000000005") {
			sawDelta = true
		}
	}
	if !sawDelta {
		t.Fatalf("no chunk covering the new seq 5: %v", all)
	}
	// Turn two's prompt is a pointer again.
	prompts := prov.recordedPrompts()
	last := prompts[len(prompts)-1]
	if strings.Contains(last, "new message") {
		t.Fatalf("history leaked into reuse prompt:\n%s", last)
	}
}

// .room/apps.md appears once the room has an interactive app (a site the
// listing marks isApp, its usage read with the turn's job grant) and is
// rewritten every turn from then on, so it never claims an app that is gone.
func TestRoomRecordsWriteAppsOnceTheRoomHasOne(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1}, map[int64]string{1: "hello"})
	g.mu.Lock()
	g.versions["artf_poll/1"] = `{"version":{"artifactId":"artf_poll","version":1,"sourceJobId":"job-a","authorUserId":"maya"}}`
	g.runs["job-a"] = `{"jobId":"job-a","agent":{"owner":"maya","core":"acme","provider":"opencode"}}`
	g.mu.Unlock()
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya"})
	// One session's passes, each with its own job, threading the state the
	// pass records (materializeRoomContext's).
	var st roomlog.CtxState
	turn := func(job string, isApp bool, stats string) (string, bool) {
		t.Helper()
		g.mu.Lock()
		g.artifacts = fmt.Sprintf(`{"artifacts":[{"artifactId":"artf_poll","title":"Friday poll","kind":"site","currentVersion":1,"createdBy":"maya","isApp":%v}]}`, isApp)
		g.appStats["artf_poll"] = stats
		g.mu.Unlock()
		files, next := b.roomRecordFiles(context.Background(), "r1", job, roomlog.Producer{Owner: "maya", Provider: "codex"}, st, roomlog.Guidance{HasRoomFiles: true}, nil)
		st.ReadmeSum, st.AppsListed = next.ReadmeSum, next.AppsListed
		for _, f := range files {
			if f.Name == ".room/apps.md" {
				return f.Content, true
			}
		}
		return "", false
	}
	if got, ok := turn("job-1", false, `{"documents":0,"bytes":0,"collections":{}}`); ok {
		t.Fatalf("a room whose site is no app gets apps.md:\n%s", got)
	}
	got, ok := turn("job-2", true, `{"documents":3,"bytes":90,"collections":{"votes":3}}`)
	if !ok || !strings.Contains(got, "- Friday poll v1, id artf_poll: from maya/opencode on core acme; 3 documents: votes 3") {
		t.Fatalf("apps.md once the room has an app (written %v):\n%s", ok, got)
	}
	if got, ok := turn("job-3", false, `{"documents":0,"bytes":0,"collections":{}}`); !ok || !strings.Contains(got, "(no apps now)") {
		t.Fatalf("apps.md after the app went (written %v):\n%s", ok, got)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.appQueries) != 1 || g.appQueries[0] != "job=job-2" {
		t.Fatalf("usage reads = %q, want one for the app's turn with its job's grant", g.appQueries)
	}
}

// .room/README.md is static: a session's first pass writes it, follow-up
// turns do not rewrite it while its content is unchanged, and the records
// whose data changes (artifacts.md, library.md) are rewritten every turn. A
// new session in the same workspace finds it there; a fresh workspace gets
// it again.
func TestDriveClaimPullWritesReadmeOnlyWhenChanged(t *testing.T) {
	prov := &profileProv{}
	b := newProfileBridge(t, prov)
	count := func() map[string]int {
		n := map[string]int{}
		for _, w := range prov.recordedWrites() {
			n[w]++
		}
		return n
	}
	for i, text := range []string{"turn one", "turn two", "turn three"} {
		if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: fmt.Sprintf("job-%d", i)}, "r1", "maya", "core-a", "agent-a", "t", text, "ch"), nil); err != nil {
			t.Fatal(err)
		}
	}
	n := count()
	if n[".room/README.md"] != 1 {
		t.Fatalf("README.md written %d times over three turns of one session, want once", n[".room/README.md"])
	}
	if n[".room/artifacts.md"] != 3 || n[".room/library.md"] != 3 {
		t.Fatalf("per-turn records written %v, want three each", n)
	}
	// A new session in the same workspace finds the README there.
	b.mu.Lock()
	b.forgetRoom("r1")
	b.mu.Unlock()
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-new"}, "r1", "maya", "core-a", "agent-a", "t", "new session", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	if got := count()[".room/README.md"]; got != 1 {
		t.Fatalf("README.md written %d times after a new session in the same workspace, want once", got)
	}
	// A fresh workspace (a bridge that kept no bindings) writes it again.
	fresh := New(b.cfg)
	if _, err := fresh.driveClaim(context.Background(), testDrive(fresh, jobRef{ID: "job-fresh"}, "r1", "maya", "core-a", "agent-a", "t", "fresh workspace", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	if got := count()[".room/README.md"]; got != 2 {
		t.Fatalf("README.md written %d times after a fresh workspace, want 2", got)
	}
}

// When the writer fails, the drive falls back to the pushed context so the
// turn still carries the room's history.
func TestDriveClaimPullFallsBackOnWriterFailure(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{1}, map[int64]string{1: "fallback marker message"})
	g.setHistory("test", []int64{}, nil)
	prov := &fakeCoreProvider{url: g.srv.URL}
	prov.failWrites = true
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	if _, err := b.driveClaim(context.Background(), testDrive(b, jobRef{ID: "job-x"}, "r1", "maya", "core-a", "agent-a", "t", "still drive", "ch"), nil); err != nil {
		t.Fatal(err)
	}
	prompt := prov.recordedPrompts()[0]
	if !strings.Contains(prompt, "fallback marker message") {
		t.Fatalf("writer failure must fall back to pushed history:\n%s", prompt)
	}
}

// The .room/ tree is the workspace's, shared by every channel session of
// the agent (docs/prd/2026-10-13-sessions-per-channel.md): it keeps every
// line of every channel an agent may read, the agent's own replies and the
// current message included (a filter of one session would hide them from
// another session, whose memory does not hold them), and a pass continues
// what the workspace holds. The current message's sender is returned for
// the [now] marker.
func TestMaterializeKeepsEveryLineInTheSharedTree(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my reply in general")
	g.setHistory("test", []int64{}, nil)
	g.addAgentRow("test", 12, "maya", "opencode", "peer agent answers")
	g.addChatRow("test", 13, "jack", "create final_check.rs: print done")
	prov := &fakeCoreProvider{url: g.srv.URL}
	b := New(Config{
		GatewayBaseURL: g.srv.URL,
		GatewayToken:   "t",
		OwnerSubject:   "maya",
		Cores:          []CoreConfig{{Name: "core-a", Provider: prov, Endpoint: corepkg.Endpoint{BaseURL: g.srv.URL}}},
	})
	w, ep, ok := testWriter(b, "core-a")
	if !ok {
		t.Fatal("fake provider must satisfy WorkspaceFileWriter")
	}
	recentOf := func() string {
		var recent string
		for _, wr := range prov.roomFileWrites() {
			if strings.HasSuffix(wr.path, "recent.md") {
				recent = wr.content
			}
		}
		return recent
	}
	self := roomlog.Producer{Owner: "maya", Provider: "agent-a"}
	st, from, err := b.materializeRoomContext(context.Background(), w, ep, "r1", "ws", "job-x", "create final_check.rs: print done", self, roomlog.CtxState{}, roomlog.Guidance{HasRoomFiles: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if from != "jack" {
		t.Fatalf("instruction sender = %q, want jack", from)
	}
	recent := recentOf()
	for _, want := range []string{"person asks", "my reply in general", "peer agent answers", "final_check"} {
		if !strings.Contains(recent, want) {
			t.Errorf("the shared records lack %q:\n%s", want, recent)
		}
	}

	// The next pass (another channel's session) continues the workspace's
	// state: only what is new is fetched into a new chunk.
	g.addAgentRow("general", 14, "maya", "agent-a", "my next reply")
	st2, _, err := b.materializeRoomContext(context.Background(), w, ep, "r1", "ws", "job-y", "", self, st, roomlog.Guidance{HasRoomFiles: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st2.EndSeq != 14 || len(st2.Chunks) != len(st.Chunks)+1 || !strings.Contains(recentOf(), "my next reply") {
		t.Fatalf("continued pass: state %+v after %+v", st2, st)
	}
}

// driveTextDelta is the push model: same semantics as pull, inline delivery.
// Fresh turn = full cross-channel window; follow-up = delta since the last
// push; nothing new = bare instruction. Self replies never re-inject (the
// session memory already holds them), the current instruction never appears in
// the records (it is the [now] section).
func TestDriveTextDeltaFreshWindow(t *testing.T) {
	g := newPullGateway(t)
	// general: a person's line, then SELF's reply. test: the current
	// instruction (chat), then a peer agent's reply.
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my own earlier reply")
	g.setHistory("test", []int64{12}, map[int64]string{12: "do the thing"})
	g.addAgentRow("test", 13, "maya", "opencode", "peer agent answers")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya"})

	st := roomlog.CtxState{}
	prompt := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", roomlog.Now{From: "maya", Text: "do the thing"}, &st, true)

	if !strings.Contains(prompt, "person asks") {
		t.Fatalf("person's message (general channel) must inject:\n%s", prompt)
	}
	if !strings.Contains(prompt, "peer agent answers") {
		t.Fatalf("peer agent's reply (test channel) must inject — push must read ALL channels:\n%s", prompt)
	}
	// A FRESH session keeps its own prior replies: a rebind starts a blank
	// CLI, so they are continuity, not redundancy.
	if !strings.Contains(prompt, "my own earlier reply") {
		t.Fatalf("fresh session must see its own prior replies (blank CLI memory holds none):\n%s", prompt)
	}
	if strings.Count(prompt, "do the thing") != 1 {
		t.Fatalf("instruction must appear exactly once as [now], never in records:\n%s", prompt)
	}
	if !strings.Contains(prompt, "[now — from jack]\ndo the thing") {
		t.Fatalf("instruction must directly follow the now marker:\n%s", prompt)
	}
	if st.PushSeenSeq != 13 {
		t.Fatalf("PushSeenSeq = %d, want 13 (newest fetched row, filtered rows included)", st.PushSeenSeq)
	}
}

func TestDriveTextDeltaFollowUpOnlyDelta(t *testing.T) {
	g := newPullGateway(t)
	g.setHistory("general", []int64{10}, map[int64]string{10: "person asks"})
	g.addAgentRow("general", 11, "maya", "agent-a", "my own earlier reply")
	g.setHistory("test", []int64{12}, map[int64]string{12: "do the thing"})
	g.addAgentRow("test", 13, "maya", "opencode", "peer agent answers")
	b := New(Config{GatewayBaseURL: g.srv.URL, GatewayToken: "t", OwnerSubject: "maya"})

	st := roomlog.CtxState{}
	first := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", roomlog.Now{From: "maya", Text: "do the thing"}, &st, true)
	_ = first

	// New activity since the last push: a person question, a peer reply, and
	// THIS session's own reply (posted as the first turn drained).
	g.setHistory("general", []int64{20}, map[int64]string{20: "new question"})
	g.addAgentRow("test", 21, "maya", "opencode", "peer follow-up")
	g.addAgentRow("general", 22, "maya", "agent-a", "my own this-session reply")
	second := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", roomlog.Now{From: "maya", Text: "next instruction"}, &st, false)

	if !strings.Contains(second, "New room messages since your last turn") {
		t.Fatalf("follow-up must be framed as a delta:\n%s", second)
	}
	if !strings.Contains(second, "new question") || !strings.Contains(second, "peer follow-up") {
		t.Fatalf("delta must carry the new messages:\n%s", second)
	}
	if strings.Contains(second, "my own this-session reply") {
		t.Fatalf("this session's own reply is session memory; the delta must drop it:\n%s", second)
	}
	if strings.Contains(second, "person asks") {
		t.Fatalf("already-seen messages must not re-inject — that repetition is the push model's old cost:\n%s", second)
	}
	if st.PushSeenSeq != 22 {
		t.Fatalf("PushSeenSeq = %d, want 22 (self replies count as seen — the session authored them)", st.PushSeenSeq)
	}

	// Nothing new: the instruction alone, under the marker naming its
	// sender (the requester when the history does not hold it).
	third := b.driveTextDelta(context.Background(), "r1", "maya", "agent-a", roomlog.Now{From: "maya", Text: "nothing new at all"}, &st, false)
	if third != "[now — from maya]\nnothing new at all" {
		t.Fatalf("empty delta must send the instruction alone, got:\n%s", third)
	}
}
