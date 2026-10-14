package roomlog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// historyServer serves a paged, channel-scoped history over in-memory rows.
type historyServer struct {
	// peopleChannel is never listed (the room's people chat).
	peopleChannel string
	rows          []struct {
		seq     int64
		channel string
		kind    string
		payload string
	}
}

func (h *historyServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/channels") {
			// The listing an agent reads: every channel holding rows, in
			// order of appearance (the real listing never names the people
			// chat; peopleChannel models it here).
			type channel struct {
				ChannelID string `json:"channelId"`
				Name      string `json:"name"`
			}
			var out []channel
			seen := map[string]bool{h.peopleChannel: true}
			for _, row := range h.rows {
				if !seen[row.channel] {
					seen[row.channel] = true
					out = append(out, channel{ChannelID: row.channel, Name: "n-" + row.channel})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"channels": out})
			return
		}
		ch := r.URL.Query().Get("channelId")
		before := int64(0)
		if b := r.URL.Query().Get("before"); b != "" {
			fmt.Sscanf(b, "%d", &before)
		}
		type msg struct {
			Sequence int64           `json:"sequence"`
			Kind     string          `json:"kind"`
			Payload  json.RawMessage `json:"payload"`
		}
		// Match the real API contract: the newest `limit` messages with
		// seq < before, returned in ASCENDING order (stores reverse DESC pages).
		limit := 200
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}
		var desc []msg
		for i := len(h.rows) - 1; i >= 0 && len(desc) < limit; i-- {
			row := h.rows[i]
			if row.channel != ch {
				continue
			}
			if before > 0 && row.seq >= before {
				continue
			}
			desc = append(desc, msg{Sequence: row.seq, Kind: row.kind, Payload: json.RawMessage(row.payload)})
		}
		asc := make([]msg, len(desc))
		for i, m := range desc {
			asc[len(desc)-1-i] = m
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": asc})
	})
}

func chatRow(seq int64, ch, user, text string) (int64, string, string, string) {
	return seq, ch, "chat_message", fmt.Sprintf(`{"userId":%q,"text":%q}`, user, text)
}

// History must merge channels chronologically and stop paging once it
// passes the boundary — the push model only ever read the default channel, so
// most of a multi-channel room's conversation never reached the agent.
func TestHistoryMergesChannelsAndDeltas(t *testing.T) {
	h := &historyServer{}
	h.rows = append(h.rows,
		mkRow(chatRow(1, "general", "jack", "first in general")),
		mkRow(chatRow(2, "test", "maya", "first in test")),
		mkRow(chatRow(3, "general", "jack", "second in general")),
		mkRow(chatRow(4, "test", "maya", "second in test")),
		mkRow(chatRow(5, "general", "jack", "third in general")),
	)
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	fetch := func(ctx context.Context, path string) ([]byte, error) {
		return httpGet(ctx, srv.URL+path)
	}

	all, complete, err := History(context.Background(), fetch, "room-1", HistoryQuery{})
	if err != nil || !complete {
		t.Fatalf("full fetch: %d entries, complete=%v, err=%v", len(all), complete, err)
	}
	if len(all) != 5 || all[0].Text != "first in general" || all[4].Text != "third in general" {
		t.Fatalf("merged order wrong: %+v", all)
	}

	// Delta fetch returns the chronological TAIL — it may extend below the
	// boundary, because recent.md needs a window of the newest messages; the
	// seq-past-boundary filter is the caller's (Materialize's) job.
	tail, complete, err := History(context.Background(), fetch, "room-1", HistoryQuery{After: 3})
	if err != nil || !complete {
		t.Fatalf("delta fetch: complete=%v, err=%v", complete, err)
	}
	var delta []Entry
	for _, e := range tail {
		if e.Seq > 3 {
			delta = append(delta, e)
		}
	}
	if len(delta) != 2 || delta[0].Text != "second in test" || delta[1].Text != "third in general" {
		t.Fatalf("delta past seq 3 = %+v, want the two newest entries", delta)
	}
}

// A long channel pages backward correctly: with the cursor on the page's
// OLDEST seq (pages arrive ascending), every message is fetched exactly once
// and the pass terminates. Regression: the cursor once read the page's newest
// seq, so each page dropped one message and re-fetched the rest until the row
// cap — leaving later channels unfetched entirely.
func TestHistoryPagesLongChannelWithoutDuplicates(t *testing.T) {
	h := &historyServer{}
	var rows []struct {
		seq     int64
		channel string
		kind    string
		payload string
	}
	// Channel A: 450 messages (3 pages); channel B: 5 messages that must also
	// be fetched even though A alone exceeds one page.
	for i := 1; i <= 450; i++ {
		rows = append(rows, mkRow(chatRow(int64(i), "a", "jack", fmt.Sprintf("a-%d", i))))
	}
	for i := 451; i <= 455; i++ {
		rows = append(rows, mkRow(chatRow(int64(i), "b", "maya", fmt.Sprintf("b-%d", i))))
	}
	h.rows = rows
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	fetch := func(ctx context.Context, path string) ([]byte, error) {
		return httpGet(ctx, srv.URL+path)
	}

	entries, complete, err := History(context.Background(), fetch, "room-1", HistoryQuery{})
	if err != nil || !complete {
		t.Fatalf("fetch: %d entries, complete=%v, err=%v", len(entries), complete, err)
	}
	if len(entries) != 455 {
		t.Fatalf("entries = %d, want 455 (450 from A + 5 from B — a missed channel means the cursor looped)", len(entries))
	}
	if entries[0].Text != "a-1" || entries[len(entries)-1].Text != "b-455" {
		t.Fatalf("coverage wrong: first=%q last=%q", entries[0].Text, entries[len(entries)-1].Text)
	}
}

// Materialize: first pass chunks everything, second pass only the delta, and
// the index always reflects the full set.
func TestMaterializeChunksAndDeltas(t *testing.T) {
	entries := make([]Entry, 0, 250)
	for i := 1; i <= 250; i++ {
		entries = append(entries, Entry{Seq: int64(i), Speaker: "jack", Kind: KindHuman, Text: fmt.Sprintf("message %d", i)})
	}
	files, st := Materialize(entries, true, CtxState{})
	chunks := 0
	recentIdx, indexIdx := -1, -1
	for i, f := range files {
		switch {
		case strings.HasPrefix(f.Name, ".room/chunk-"):
			chunks++
		case f.Name == ".room/recent.md":
			recentIdx = i
		case f.Name == ".room/INDEX.md":
			indexIdx = i
		}
	}
	if chunks != 2 { // 200-entry cap per chunk
		t.Fatalf("chunks = %d, want 2", chunks)
	}
	if st.EndSeq != 250 || len(st.Chunks) != 2 || st.OldestSeq != 0 {
		t.Fatalf("state = %+v", st)
	}
	if indexIdx != len(files)-1 {
		t.Fatal("INDEX must be the last file so a partial write never advertises missing chunks")
	}
	if recentIdx == -1 {
		t.Fatal("recent.md missing")
	}
	if !strings.Contains(files[recentIdx].Content, "message 250") || strings.Contains(files[recentIdx].Content, "message 100") {
		t.Fatal("recent.md must carry the newest entries only")
	}

	// Delta pass: 10 new entries, only one new chunk file.
	newEntries := append(append([]Entry{}, entries[240:]...), Entry{Seq: 251, Speaker: "maya", Kind: KindHuman, Text: "new 251"})
	files2, st2 := Materialize(newEntries, true, st)
	newChunks := 0
	for _, f := range files2 {
		if strings.HasPrefix(f.Name, ".room/chunk-") {
			newChunks++
		}
	}
	if newChunks != 1 || st2.EndSeq != 251 || len(st2.Chunks) != 3 {
		t.Fatalf("delta materialize: newChunks=%d state=%+v", newChunks, st2)
	}
	if !strings.Contains(files2[len(files2)-1].Content, "through seq 251") {
		t.Fatal("index must reflect the new end")
	}
}

// The pointer prompt must carry no history — the whole point is that the
// instruction is never diluted.
func TestComposePointerHasNoHistory(t *testing.T) {
	if out := ComposePointer(Now{From: "maya", Text: "存到文件里"}, false); out != "[now — from maya]\n存到文件里" {
		t.Errorf("pointer prompt = %q", out)
	}
	if out := ComposePointer(Now{From: "maya", Text: "存到文件里"}, true); out != "(room records: .room/INDEX.md)\n\n[now — from maya]\n存到文件里" {
		t.Errorf("pointer prompt with the records line = %q", out)
	}
}

func mkRow(seq int64, ch, kind, payload string) struct {
	seq     int64
	channel string
	kind    string
	payload string
} {
	return struct {
		seq     int64
		channel string
		kind    string
		payload string
	}{seq, ch, kind, payload}
}

// Decision O1: an agent reads exactly the channels realtime admits for an
// agent reader — every listed channel, never the people chat (which the
// listing omits) — and a read narrowed to one channel refuses one outside
// that set instead of fetching it with the owner's human session.
func TestHistoryReadsOnlyAgentChannels(t *testing.T) {
	h := &historyServer{peopleChannel: "people"}
	h.rows = append(h.rows,
		mkRow(chatRow(1, "general", "jack", "in general")),
		mkRow(chatRow(2, "people", "maya", "people only")),
		mkRow(chatRow(3, "design", "maya", "in design")),
	)
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	var paths []string
	fetch := func(ctx context.Context, path string) ([]byte, error) {
		paths = append(paths, path)
		return httpGet(ctx, srv.URL+path)
	}
	all, _, err := History(context.Background(), fetch, "room-1", HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Text != "in general" || all[1].Text != "in design" {
		t.Fatalf("agent history = %+v, want general and design only", all)
	}
	for _, p := range paths {
		if strings.Contains(p, "channelId=people") {
			t.Fatalf("the people chat was fetched: %s", p)
		}
	}
	if _, _, err := History(context.Background(), fetch, "room-1", HistoryQuery{Only: "people"}); err != ErrChannelNotReadable {
		t.Fatalf("Only=people: err = %v, want ErrChannelNotReadable", err)
	}
	one, _, err := History(context.Background(), fetch, "room-1", HistoryQuery{Only: "design"})
	if err != nil || len(one) != 1 || one[0].Text != "in design" {
		t.Fatalf("Only=design = %+v, %v", one, err)
	}
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var buf strings.Builder
	_, _ = fmt.Fprint(&buf, json.RawMessage{})
	_ = buf
	out := make([]byte, 0)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		out = append(out, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return out, nil
}

// The row cap must mark the pass truncated (complete=false) instead of
// silently starving the channels it never reached — a truncated pass tells
// Materialize to record its true coverage boundary.
func TestHistoryCapMarksTruncated(t *testing.T) {
	h := &historyServer{}
	var rows []struct {
		seq     int64
		channel string
		kind    string
		payload string
	}
	// 2600 messages on one channel: over maxFetchRows (2400).
	for i := 1; i <= 2600; i++ {
		rows = append(rows, mkRow(chatRow(int64(i), "a", "jack", fmt.Sprintf("a-%d", i))))
	}
	h.rows = rows
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	fetch := func(ctx context.Context, path string) ([]byte, error) {
		return httpGet(ctx, srv.URL+path)
	}
	entries, complete, err := History(context.Background(), fetch, "room-1", HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("a pass over 2600 rows must be reported incomplete, not silently truncated")
	}
	if len(entries) > 2400 {
		t.Fatalf("entries = %d, want capped at 2400", len(entries))
	}
	// A truncated pass still yields the newest coverage it did fetch.
	if entries[len(entries)-1].Text != "a-2600" {
		t.Fatalf("newest fetched = %q, want a-2600 (the cap must keep the newest, not the oldest)", entries[len(entries)-1].Text)
	}
}

// ComposeDelta: empty delta = bare message; otherwise a fenced delta section
// with the newest entries, bounded by the window budget.
func TestComposeDelta(t *testing.T) {
	if got := ComposeDelta(nil, Now{From: "maya", Text: "just do it"}, Options{}); got != "[now — from maya]\njust do it" {
		t.Fatalf("empty delta = %q, want the message alone", got)
	}
	entries := []Entry{
		{Seq: 1, Speaker: "jack", Kind: KindHuman, Text: "first new line"},
		{Seq: 2, Speaker: "jill/codex", Kind: KindAgent, Text: "second new line"},
	}
	out := ComposeDelta(entries, Now{From: "maya", Text: "act now"}, Options{WindowChars: 10 << 10})
	for _, want := range []string{RecordsHeader("New room messages since your last turn"), "jack: first new line", "jill/codex: second new line", "[now — from maya]\nact now"} {
		if !strings.Contains(out, want) {
			t.Errorf("ComposeDelta missing %q:\n%s", want, out)
		}
	}
	// A tiny budget keeps only the newest and reports the omission.
	tight := ComposeDelta(entries, Now{Text: "act now"}, Options{WindowChars: 10})
	if strings.Contains(tight, "first new line") {
		t.Errorf("window budget must drop the oldest delta line:\n%s", tight)
	}
	if !strings.Contains(tight, "second new line") || !strings.Contains(tight, "omitted") {
		t.Errorf("newest line must survive with an omission note:\n%s", tight)
	}
}

// An attachment on the current message must be named as an imperative "open
// these" block in every composer, distinct from the .room/ background records —
// the fix for agents ignoring attached images. Covers pull (ComposePointer) and
// push (Compose fresh / ComposeDelta follow-up), including the no-history cases.
func TestAttachmentBlockRendered(t *testing.T) {
	atts := []Attachment{{Path: ".room/attachments/att_x.png", Filename: "shot.png", ContentType: "image/png"}}
	check := func(name, out string) {
		if !strings.Contains(out, "OPEN THESE") {
			t.Fatalf("%s: missing attachments block: %q", name, out)
		}
		if !strings.Contains(out, ".room/attachments/att_x.png") {
			t.Fatalf("%s: missing attachment path: %q", name, out)
		}
	}
	check("pointer", ComposePointer(Now{Text: "what is this?", Attachments: atts}, false))
	check("compose-fresh-nohistory", Compose(nil, Now{Text: "what is this?", Attachments: atts}, Options{}))
	check("composedelta-nohistory", ComposeDelta(nil, Now{Text: "what is this?", Attachments: atts}, Options{}))
	// No attachments → no block (bare pointer still works).
	if strings.Contains(ComposePointer(Now{Text: "hi"}, false), "OPEN THESE") {
		t.Fatal("no-attachment prompt must not render the block")
	}
}

// A busy channel never starves the others: with the cap shared fairly,
// every channel's newest messages are read even when the first channel
// alone holds more than the cap.
func TestHistorySharesTheCapAcrossChannels(t *testing.T) {
	h := &historyServer{}
	for i := 1; i <= 500; i++ {
		h.rows = append(h.rows, mkRow(chatRow(int64(i), "busy", "jack", fmt.Sprintf("busy-%d", i))))
	}
	h.rows = append(h.rows, mkRow(chatRow(501, "quiet", "maya", "quiet-501")))
	for i := 502; i <= 510; i++ {
		h.rows = append(h.rows, mkRow(chatRow(int64(i), "busy", "jack", fmt.Sprintf("busy-%d", i))))
	}
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	fetch := func(ctx context.Context, path string) ([]byte, error) { return httpGet(ctx, srv.URL+path) }
	entries, complete, err := History(context.Background(), fetch, "room-1", HistoryQuery{MaxRows: 200})
	if err != nil || complete {
		t.Fatalf("complete=%v err=%v; want a truncated read", complete, err)
	}
	var quiet, busy int
	for _, e := range entries {
		switch e.ChannelID {
		case "quiet":
			quiet++
			if Line(e) != "#n-quiet maya: quiet-501\n" {
				t.Fatalf("line = %q, want the other channel's tag", Line(e))
			}
		case "busy":
			busy++
		}
	}
	if quiet != 1 || busy != 199 || len(entries) != 200 {
		t.Fatalf("quiet=%d busy=%d total=%d: the quiet channel must not starve", quiet, busy, len(entries))
	}
	// The default channel's lines stay unmarked; a reader of the quiet
	// channel sees the busy one's tagged instead.
	if Line(entries[len(entries)-1]) != "jack: busy-510\n" {
		t.Fatalf("line = %q, want no tag for the default channel", Line(entries[len(entries)-1]))
	}
	MarkChannels(entries, "quiet")
	if Line(entries[len(entries)-1]) != "#n-busy jack: busy-510\n" {
		t.Fatalf("line = %q, want the busy channel tagged for a quiet-channel reader", Line(entries[len(entries)-1]))
	}
}

// A message's attachments reach the records, an attachment-only message
// included, and a room with one channel needs no channel label.
func TestHistoryRecordsAttachments(t *testing.T) {
	h := &historyServer{}
	h.rows = append(h.rows,
		mkRow(1, "general", "chat_message", `{"userId":"maya","text":"see the brief","attachments":[{"id":"att_1","filename":"brief.md","contentType":"text/markdown","size":2048}]}`),
		mkRow(2, "general", "chat_message", `{"userId":"jack","attachments":[{"id":"att_2","filename":"plan.pdf","contentType":"application/pdf","size":10}]}`),
	)
	srv := httptest.NewServer(h.handler(t))
	defer srv.Close()
	fetch := func(ctx context.Context, path string) ([]byte, error) { return httpGet(ctx, srv.URL+path) }
	entries, _, err := History(context.Background(), fetch, "room-1", HistoryQuery{})
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v; the attachment-only message must be recorded", entries, err)
	}
	if got := Line(entries[0]); got != "maya: see the brief [attached: brief.md (text/markdown, 2 KB)]\n" {
		t.Fatalf("line = %q", got)
	}
	if got := Line(entries[1]); got != "jack: [attached: plan.pdf (application/pdf, 10 bytes)]\n" {
		t.Fatalf("attachment-only line = %q", got)
	}
}


// An attachment's copy keeps its type's extension (the original name's when
// it is one of them), else the name's own.
func TestAttachmentPath(t *testing.T) {
	for _, c := range []struct{ ct, name, want string }{
		{"image/png", "image", ".room/attachments/a.png"},
		{"image/jpeg", "photo.JPEG", ".room/attachments/a.jpeg"},
		{"application/vnd.openxmlformats-officedocument.presentationml.template", "Brand.potx", ".room/attachments/a.potx"},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", "deck", ".room/attachments/a.pptx"},
		{"application/x-unknown", "data.bin", ".room/attachments/a.bin"},
		{"application/x-unknown", "noext", ".room/attachments/a"},
	} {
		if got := AttachmentPath("a", c.ct, c.name); got != c.want {
			t.Errorf("AttachmentPath(%q, %q) = %q, want %q", c.ct, c.name, got, c.want)
		}
	}
}

// A pass mirrors the newest attachments first, never one it holds or one
// over the per-file bound, within the pass budget; records point at copies.
func TestAttachmentsToMirror(t *testing.T) {
	entries := []Entry{
		{Seq: 1, Attachments: []AttachmentRef{{ID: "old", Size: 10}}},
		{Seq: 2, Attachments: []AttachmentRef{{ID: "held", Size: 10}, {ID: "huge", Size: MaxMirrorFileBytes + 1}}},
		{Seq: 3, Attachments: []AttachmentRef{{ID: "new", Size: 10, Filename: "brief.md"}}},
	}
	got := AttachmentsToMirror(entries, map[string]string{"held": ".room/attachments/held"})
	if len(got) != 2 || got[0].ID != "new" || got[1].ID != "old" {
		t.Fatalf("to mirror = %+v, want new then old", got)
	}
	PointAttachments(entries, map[string]string{"new": ".room/attachments/new.md"})
	if l := Line(Entry{Speaker: "maya", Text: "see", Attachments: entries[2].Attachments}); l != "maya: see [attached: brief.md → .room/attachments/new.md]\n" {
		t.Fatalf("line = %q", l)
	}
}
