package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/archtest"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
	realtimehttp "github.com/luckylhb90/bnbloc/services/realtime"
)

// sourcesGW is the schedule rig with the organization's admin olga, the
// reading room (e.roomID: maya owner, jack member) asking for runs, and
// three other rooms with finished runs: shared (jack's, shared), quiet
// (nora's, private) and open (nora's, private, opted in later). The
// slot is 2026-10-15 09:00 UTC; every run is of the day before.
type sourcesGW struct {
	*appGW
	model  *toolModel
	sink   *recordedAudit
	outbox *store.MemoryNotifications
}

func newSourcesGW(t *testing.T) *sourcesGW {
	t.Helper()
	e, model, outbox := scheduleGW(t)
	sink := &recordedAudit{}
	e.gw.ExtAudit = sink
	if err := e.b.Tenant.PutMember(e.ctx(), store.TenantMember{TenantID: e.tenant, UserID: "olga", Role: store.OrgRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	e.gw.OrgAdmins = func(string) ([]string, error) { return []string{"olga"}, nil }
	g := &sourcesGW{appGW: e, model: model, sink: sink, outbox: outbox}
	// A realtime that keeps history and schedules: read_records reads each
	// run's turn through it, the consent the grantee's tasks.
	rt := &realtimehttp.Service{Hub: e.hub, Blobs: e.blobs, Chat: e.b.Chat, Schedules: e.b.Schedules, AIRuns: noScheduleRuns{}, Membership: membership.NewRoomClient(e.roomURL)}
	rtSrv := httptest.NewServer(svcauth.Middleware(appGWKey, rt.Handler()))
	t.Cleanup(rtSrv.Close)
	e.gw.Backends.Realtime = rtSrv.URL
	// explain-source-use is the default content's, published for every
	// organization (the gateway runs the organization's or the platform's).
	e.gw.ContentComponents = e.b.ContentComponents
	e.publishDefault(assistForm(t, wire.AssistTaskExplainSourceUse, defaultAssists[wire.AssistTaskExplainSourceUse]))
	for _, rm := range []struct {
		id, owner string
		vis       store.Visibility
	}{{"shared", "jack", store.VisibilityShared}, {"quiet", "nora", store.VisibilityPrivate}, {"open", "nora", store.VisibilityPrivate}} {
		if err := e.b.Room.CreateRoomWithOwner(e.ctx(), store.Room{RoomID: rm.id, Name: "Room " + rm.id, Visibility: rm.vis, CreatedBy: rm.owner, CreatedAtMs: 1},
			store.Membership{RoomID: rm.id, UserID: rm.owner, Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
	}
	day := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC).UnixMilli()
	g.job("j-shared", "shared", "jack", "jack", store.AgentJobReplied, "Draft the release notes", day)
	g.job("j-quiet", "quiet", "nora", "jack", store.AgentJobFailed, "Read the sales sheet", day+1)
	g.job("j-open", "open", "nora", "nora", store.AgentJobReplied, "Plan the offsite", day+2)
	g.job("j-self", e.roomID, "maya", "jack", store.AgentJobReplied, "Review yesterday", day+3)
	if err := e.b.RoomSources.AskRoomSource(e.ctx(), store.RoomSourceGrant{RoomID: e.roomID, Source: wire.RoomSourceRuns, ComponentID: "cc_review",
		ComponentName: "outcome-review", ComponentVersion: "1.0.0", Purpose: "Judge whether conversations reached their goal.", AliasKey: "alias-key",
		AskedBy: "maya", AskedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	return g
}

// job stores a finished run of owner's agent that requester asked for in
// room (its request at room sequence atMs), with its reply preview.
func (g *sourcesGW) job(id, room, requester, owner string, status store.AgentJobStatus, text string, atMs int64) {
	g.t.Helper()
	g.putJob(store.AgentJob{JobID: id, RoomID: room, MessageSeq: atMs, RequesterUserID: requester, OwnerUserID: owner, Provider: "codex", MessageText: text,
		Status: status, CreatedAtMs: atMs}, 1)
}

// putJob stores j with the run record's preview of its first reply and
// the count of replies its run posted.
func (g *sourcesGW) putJob(j store.AgentJob, replies int) {
	g.t.Helper()
	j.DiagnosticsJSON, _ = json.Marshal(map[string]any{wire.RunDiagReply: wire.AgentRunReply{MessageID: "m-" + j.JobID, Text: "Reply to " + j.MessageText, Count: replies}})
	if j.Status == store.AgentJobFailed {
		j.FailureReason = store.FailureAgentError
	}
	if _, err := g.jobs.PutAgentJob(g.ctx(), j); err != nil {
		g.t.Fatal(err)
	}
}

// ask stores user's request in room's default channel at seq, with files.
func (g *sourcesGW) ask(room string, seq int64, user, text string, files ...string) {
	g.t.Helper()
	var atts []wire.ChatAttachment
	for i, f := range files {
		atts = append(atts, wire.ChatAttachment{ID: fmt.Sprintf("att-%d-%d", seq, i), Filename: f})
	}
	g.message(room, seq, wire.KindChatMessage, user, map[string]any{"userId": user, "text": text, "attachments": atts})
}

// answer stores agent's (owner/provider) reply to the request at source in
// room's default channel at seq.
func (g *sourcesGW) answer(room string, seq int64, agent string, source int64, text string) {
	g.t.Helper()
	owner, provider, _ := strings.Cut(agent, "/")
	g.message(room, seq, wire.KindAgentMessages, owner, map[string]any{"agentSessionId": "s-" + agent, "messages": []any{map[string]any{"seq": seq,
		"payload": map[string]any{"messageId": fmt.Sprint("msg-", seq), "role": "assistant", "text": text, "ownerUserId": owner, "provider": provider, "sourceSeq": source}}}})
}

// message stores one row of room's default channel at seq.
func (g *sourcesGW) message(room string, seq int64, kind, author string, payload any) {
	g.t.Helper()
	raw, _ := json.Marshal(payload)
	if err := g.b.Chat.AppendRoomMessage(g.ctx(), store.RoomMessage{RoomID: room, Seq: seq, Kind: kind, Author: author, Payload: raw, CreatedAtMs: seq}); err != nil {
		g.t.Fatal(err)
	}
}

// orgAdmin is olga's session.
func (g *sourcesGW) orgAdmin() string {
	return "Authorization: Bearer " + g.gw.Signer.Issue(auth.Claims{Subject: "olga", Tenant: g.tenant, Role: string(store.OrgRoleAdmin), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
}

// decide grants scope, read by maya, the reading room's owner ("" withdraws)
// as olga.
func (g *sourcesGW) decide(scope string) {
	g.t.Helper()
	if r := g.decideAs(scope, "maya"); r.status != http.StatusOK {
		g.t.Fatalf("decide %q = %d %s", scope, r.status, r.body)
	}
}

// decideAs is olga's decision: scope read by grantee ("" withdraws).
func (g *sourcesGW) decideAs(scope, grantee string) resp {
	g.t.Helper()
	path := "/v1/tenant/room-sources/" + g.roomID + "/" + wire.RoomSourceRuns
	if scope == "" {
		return g.call(http.MethodDelete, path, nil, g.orgAdmin())
	}
	return g.call(http.MethodPut, path, wire.RoomSourceDecision{Scope: scope, Grantee: grantee}, g.orgAdmin())
}

// read runs owner's schedule once with the given read_records calls and
// answers what each call returned to the model.
func (g *sourcesGW) read(owner string, args ...string) []string {
	g.t.Helper()
	g.model.turns = nil
	for _, a := range args {
		g.model.turns = append(g.model.turns, calls([2]string{readRecordsTool, a}))
	}
	g.model.turns = append(g.model.turns, map[string]any{"content": "done"})
	g.runAIAs(owner, "Review yesterday's conversations.", false)
	var out []string
	last := g.model.seen[len(g.model.seen)-1]
	for _, m := range last {
		if m["role"] == "tool" {
			out = append(out, fmt.Sprint(m["content"]))
		}
	}
	return out
}

// page is a read's records and next (an error answer is fatal).
func (g *sourcesGW) page(out string) ([]sourceRecord, string) {
	g.t.Helper()
	start, end := strings.Index(out, "{\"next\""), strings.LastIndex(out, "}")
	if strings.HasPrefix(out, "error:") || start < 0 || end < start {
		g.t.Fatalf("a read answered %q", out)
	}
	var p struct {
		Records []sourceRecord `json:"records"`
		Next    string         `json:"next"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &p); err != nil {
		g.t.Fatalf("a read's page %q: %v", out, err)
	}
	return p.Records, p.Next
}

func roomsOf(recs []sourceRecord) string {
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.RoomID)
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

// A room that asks for no source is offered no read_records; one that asks
// is, but reads nothing until the admin grants. Each scope covers its rooms
// and never the reading room; a withdrawal holds at the next read; a
// schedule of a member who is not the room's owner or admin reads nothing.
// Every read is audited without its records' text.
func TestRoomSourceConsentScopes(t *testing.T) {
	g := newSourcesGW(t)
	runs := `{"source":"runs"}`

	// No ask: no tool.
	if err := g.b.Room.CreateRoomWithOwner(g.ctx(), store.Room{RoomID: "plain", Name: "plain", CreatedBy: "maya", CreatedAtMs: 1},
		store.Membership{RoomID: "plain", UserID: "maya", Role: store.RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if !g.gw.asksForSources(g.ctx(), auth.Claims{Subject: "maya", Tenant: g.tenant}, g.roomID) || g.gw.asksForSources(g.ctx(), auth.Claims{Subject: "maya", Tenant: g.tenant}, "plain") {
		t.Fatal("asksForSources: the reading room asks, a plain room does not")
	}

	// Asked, not granted.
	if out := g.read("maya", runs); !strings.Contains(out[0], "has not granted") {
		t.Fatalf("before the grant = %q", out)
	}
	if !slices.Contains(g.model.tools, readRecordsTool) {
		t.Fatalf("tools offered = %v", g.model.tools)
	}

	g.decide(wire.RoomSourceScopeAll)
	recs, next := g.page(g.read("maya", runs)[0])
	if roomsOf(recs) != "open,quiet,shared" || next != "" {
		t.Fatalf("scope all = %s (next %q)", roomsOf(recs), next)
	}
	for _, r := range recs {
		if r.RoomID == "quiet" && (r.Status != string(store.AgentJobFailed) || r.Failure == "" || r.Reply != "Reply to Read the sales sheet" || r.Request != "Read the sales sheet" || r.Room != "Room quiet") {
			t.Fatalf("a failed record = %+v", r)
		}
	}

	g.decide(wire.RoomSourceScopeNonePrivate)
	if recs, _ := g.page(g.read("maya", runs)[0]); roomsOf(recs) != "shared" {
		t.Fatalf("scope none-private = %s", roomsOf(recs))
	}

	g.decide(wire.RoomSourceScopeOptedIn)
	if recs, _ := g.page(g.read("maya", runs)[0]); len(recs) != 0 {
		t.Fatalf("scope opted-in before any opt-in = %s", roomsOf(recs))
	}
	optIn := "/v1/rooms/open/sources/runs/opt-in"
	if r := g.call(http.MethodPut, optIn, wire.RoomSourceOptIn{OptedIn: true}, g.as("jack")); r.status != http.StatusForbidden {
		t.Fatalf("a non-owner's opt-in = %d", r.status)
	}
	if r := g.call(http.MethodPut, optIn, wire.RoomSourceOptIn{OptedIn: true}, g.as("nora")); r.status != http.StatusOK {
		t.Fatalf("the owner's opt-in = %d %s", r.status, r.body)
	}
	if recs, _ := g.page(g.read("maya", runs)[0]); roomsOf(recs) != "open" {
		t.Fatalf("scope opted-in = %s", roomsOf(recs))
	}

	// jack's schedule (a member, not the person the grant is pinned to).
	if out := g.read("jack", runs); !strings.Contains(out[0], "read by maya: only their schedules read it") {
		t.Fatalf("a member's schedule read = %q", out)
	}

	g.sink.take()
	g.decide("")
	if out := g.read("maya", runs); !strings.Contains(out[0], "has not granted") {
		t.Fatalf("after the withdrawal = %q", out)
	}
	var reads []store.ExtAuditRecord
	for _, rec := range g.sink.take() {
		if rec.Kind == wire.AuditKindSource {
			reads = append(reads, rec)
		}
	}
	if len(reads) != 2 || reads[0].Action != "withdraw" || reads[0].Actor != "olga" || reads[1].Action != "read" || reads[1].Outcome != wire.AuditOutcomeDeny {
		t.Fatalf("audit = %+v", reads)
	}
}

// Names reach only a reader who is an organization admin; anyone else
// reads the grant's stable aliases. A read is audited with its range and
// count, never a record's text.
func TestRoomSourceAliasesAndAudit(t *testing.T) {
	g := newSourcesGW(t)
	g.decide(wire.RoomSourceScopeAll)
	g.sink.take()
	recs, _ := g.page(g.read("maya", `{"source":"runs","since":"2026-10-14","until":"2026-10-15"}`)[0])
	aliases := map[string]string{}
	for _, r := range recs {
		if strings.Contains(r.Requester+r.Agent, "jack") || strings.Contains(r.Requester+r.Agent, "nora") || !strings.HasPrefix(r.Requester, "member-") {
			t.Fatalf("a member reads a name: %+v", r)
		}
		aliases[r.ID] = r.Requester
	}
	if aliases["j-quiet"] != aliases["j-open"] || aliases["j-shared"] == aliases["j-quiet"] {
		t.Fatalf("aliases are not one per person: %v", aliases)
	}
	again, _ := g.page(g.read("maya", `{"source":"runs"}`)[0])
	for _, r := range again {
		if aliases[r.ID] != r.Requester {
			t.Fatalf("an alias changed between reads: %s %s", aliases[r.ID], r.Requester)
		}
	}
	var audited []store.ExtAuditRecord
	for _, rec := range g.sink.take() {
		if rec.Kind == wire.AuditKindSource && rec.Action == "read" {
			audited = append(audited, rec)
		}
	}
	if len(audited) != 2 || audited[0].Outcome != wire.AuditOutcomeAllow || audited[0].Name != g.roomID || audited[0].Actor != "maya" ||
		!strings.Contains(audited[0].Detail, "3 records") || !strings.Contains(audited[0].Detail, "2026-10-14") || strings.Contains(audited[0].Detail, "sales") {
		t.Fatalf("audit = %+v", audited)
	}

	if err := g.b.Tenant.PutMember(g.ctx(), store.TenantMember{TenantID: g.tenant, UserID: "maya", Role: store.OrgRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	recs, _ = g.page(g.read("maya", `{"source":"runs"}`)[0])
	for _, r := range recs {
		if r.ID == "j-quiet" && (r.Requester != "nora" || r.Agent != "jack/codex" || !r.Borrowed) {
			t.Fatalf("an org admin's record = %+v", r)
		}
	}
}

// A record carries the run's turn as its room holds it, read through the
// gateway's one history read (realtime's internal route: the reading
// schedule's owner is no member of the covered room): the request's files
// by name and the last reply of the run's own agent to that request, cut
// to its bound. Another agent's answer, an answer to another request and
// one past the read are not it. What the read does not reach is said: the
// run record's preview with a note, the files counted.
func TestReadRecordsTurn(t *testing.T) {
	g := newSourcesGW(t)
	g.decide(wire.RoomSourceScopeAll)
	at := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC).UnixMilli()
	g.putJob(store.AgentJob{JobID: "j-turn", RoomID: "shared", MessageSeq: at, RequesterUserID: "jack", OwnerUserID: "jack", Provider: "codex",
		MessageText: "Clean the sample data", AttachmentIDs: []string{"att-1", "att-2"}, Status: store.AgentJobReplied, CreatedAtMs: at}, 3)
	g.ask("shared", at, "jack", "Clean the sample data", "sample.csv", "notes.md")
	g.answer("shared", at+1, "jack/codex", at, "Working on it.")
	g.answer("shared", at+2, "nora/claude", at, "Another agent's answer.")
	g.answer("shared", at+3, "jack/codex", at-7, "An answer to another request.")
	g.answer("shared", at+4, "jack/codex", at, strings.Repeat("The sample has 40 bad rows. ", 120))
	g.answer("shared", at+recordTurnRows+1, "jack/codex", at, "Past the read.")
	g.putJob(store.AgentJob{JobID: "j-gone", RoomID: "open", MessageSeq: at + 10, RequesterUserID: "nora", OwnerUserID: "nora", Provider: "codex",
		MessageText: "Merge the two sheets", AttachmentIDs: []string{"att-9", "att-10"}, Status: store.AgentJobReplied, CreatedAtMs: at + 10}, 1)

	recs, _ := g.page(g.read("maya", `{"source":"runs","since":"2026-10-14","until":"2026-10-15"}`)[0])
	byID := map[string]sourceRecord{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	turn := byID["j-turn"]
	if !slices.Equal(turn.Attachments, []string{"sample.csv", "notes.md"}) || !strings.HasPrefix(turn.Reply, "The sample has 40 bad rows.") ||
		!strings.HasSuffix(turn.Reply, "…") || len(turn.Reply) > maxRecordReplyBytes || turn.ReplyNote != "the last of 2 replies read here; the run posted 3" {
		t.Fatalf("a run's turn = attachments %q, reply %d bytes %.40q…, note %q", turn.Attachments, len(turn.Reply), turn.Reply, turn.ReplyNote)
	}
	gone := byID["j-gone"]
	if gone.Reply != "Reply to Merge the two sheets" || !strings.Contains(gone.ReplyNote, "preview of the run's first reply") ||
		len(gone.Attachments) != 1 || !strings.HasPrefix(gone.Attachments[0], "2 files (names not read") {
		t.Fatalf("a turn the room no longer holds = %+v", gone)
	}
}

// A read is bounded: at most maxRecordsPerRead records a page (next
// continues where it stopped, nothing twice), at most maxScheduleReads
// reads a run, a range of at most maxRecordRange.
func TestReadRecordsBounds(t *testing.T) {
	g := newSourcesGW(t)
	g.decide(wire.RoomSourceScopeNonePrivate)
	day := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := range maxRecordsPerRead + 5 {
		seq := day + int64(i)*10
		g.job(fmt.Sprintf("j-bulk-%02d", i), "shared", "jack", "jack", store.AgentJobReplied, strings.Repeat("long request ", 200), seq)
		g.answer("shared", seq+1, "jack/codex", seq, strings.Repeat("a long <reply> & more ", 200))
	}
	out := g.read("maya", `{"source":"runs"}`)[0]
	if len(out) > maxScheduleToolText {
		t.Fatalf("a page of %d bytes is cut at the tool's %d", len(out), maxScheduleToolText)
	}
	first, next := g.page(out)
	if len(first) == 0 || len(first) > maxRecordsPerRead || next == "" {
		t.Fatalf("first page = %d records, next %q", len(first), next)
	}
	if n := len(first[0].Request); n > maxRecordRequestBytes {
		t.Fatalf("a request of %d bytes", n)
	}
	if n := len(first[0].Reply); n > maxRecordReplyBytes || n < maxRecordReplyBytes-8 {
		t.Fatalf("a reply of %d bytes, want the last reply cut to %d", n, maxRecordReplyBytes)
	}
	seen := map[string]bool{}
	for _, r := range first {
		seen[r.ID] = true
	}
	total := len(first)
	for next != "" {
		var recs []sourceRecord
		recs, next = g.page(g.read("maya", fmt.Sprintf(`{"source":"runs","next":%q}`, next))[0])
		for _, r := range recs {
			if seen[r.ID] {
				t.Fatalf("%s read twice", r.ID)
			}
			seen[r.ID] = true
		}
		total += len(recs)
	}
	if total != maxRecordsPerRead+6 {
		t.Fatalf("read %d records, want %d", total, maxRecordsPerRead+6)
	}
	var args []string
	for range maxScheduleReads + 1 {
		args = append(args, `{"source":"runs"}`)
	}
	if out := g.read("maya", args...); !strings.Contains(out[maxScheduleReads], fmt.Sprintf("at most %d times", maxScheduleReads)) {
		t.Fatalf("the read past the run's budget = %q", out[maxScheduleReads])
	}
	if out := g.read("maya", `{"source":"runs","since":"2026-08-01","until":"2026-10-15"}`); !strings.Contains(out[0], "at most 31 days") {
		t.Fatalf("a range too long = %q", out)
	}
	if out := g.read("maya", `{"source":"runs","since":"yesterday"}`); !strings.Contains(out[0], "RFC 3339") {
		t.Fatalf("a malformed since = %q", out)
	}
}

// Members of a covered room see which room reviews it (linked only for a
// member of that room); the reading room's members see its ask and where
// it stands; nobody outside the room reads either. The owner or an admin
// asks the organization's admins, once a day, while it waits.
func TestRoomSourcesTransparencyAndAsk(t *testing.T) {
	g := newSourcesGW(t)
	info := func(room, subject string) wire.RoomSourcesInfo {
		t.Helper()
		r := g.call(http.MethodGet, "/v1/rooms/"+room+"/sources", nil, g.as(subject))
		var out wire.RoomSourcesInfo
		if r.status != http.StatusOK || json.Unmarshal(r.body, &out) != nil {
			t.Fatalf("sources of %s as %s = %d %s", room, subject, r.status, r.body)
		}
		return out
	}
	if got := info(g.roomID, "jack"); len(got.Asks) != 1 || got.Asks[0].Scope != "" || got.Asks[0].Purpose == "" || len(got.ReviewedBy) != 0 {
		t.Fatalf("the reading room's sources = %+v", got)
	}
	if got := info("shared", "jack"); len(got.ReviewedBy) != 0 {
		t.Fatalf("a room reviewed before any grant: %+v", got)
	}
	ask := "/v1/rooms/" + g.roomID + "/sources/runs/ask-admin"
	if r := g.call(http.MethodPost, ask, nil, g.as("jack")); r.status != http.StatusForbidden {
		t.Fatalf("a member's ask = %d", r.status)
	}
	if r := g.call(http.MethodPost, ask, nil, g.as("maya")); r.status != http.StatusOK || r.json()["queued"] != true {
		t.Fatalf("the owner's ask = %d %s", r.status, r.body)
	}
	got := queued(t, g.outbox)
	if len(got) != 1 || got[0].Type != wire.NotificationRoomReport || !slices.Equal(recipientsOf(got[0]), []string{"olga"}) ||
		!strings.Contains(got[0].Text, "Judge whether") || !strings.HasSuffix(got[0].URL, "/#/org") {
		t.Fatalf("queued = %+v", got)
	}
	if r := g.call(http.MethodPost, ask, nil, g.as("maya")); r.status != http.StatusOK || r.json()["queued"] != false {
		t.Fatalf("a second ask the same day = %d %s", r.status, r.body)
	}

	g.decide(wire.RoomSourceScopeAll)
	if r := g.call(http.MethodPost, ask, nil, g.as("maya")); r.status != http.StatusConflict {
		t.Fatalf("an ask once granted = %d", r.status)
	}
	if got := info("shared", "jack"); len(got.ReviewedBy) != 1 || got.ReviewedBy[0].RoomName != "r" || got.ReviewedBy[0].RoomID != g.roomID {
		t.Fatalf("a member of both rooms reads %+v", got.ReviewedBy)
	}
	if got := info("quiet", "nora"); len(got.ReviewedBy) != 1 || got.ReviewedBy[0].RoomName != "r" || got.ReviewedBy[0].RoomID != "" {
		t.Fatalf("a member of the reviewed room only reads %+v", got.ReviewedBy)
	}
	if got := info(g.roomID, "maya"); len(got.ReviewedBy) != 0 || got.Asks[0].Scope != wire.RoomSourceScopeAll {
		t.Fatalf("the reading room reviews itself: %+v", got)
	}
	if r := g.call(http.MethodGet, "/v1/rooms/quiet/sources", nil, g.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a non-member reads a room's sources = %d", r.status)
	}

	// The console: the ask with its purpose and decision, for an admin only.
	r := g.call(http.MethodGet, "/v1/tenant/room-sources", nil, g.orgAdmin())
	var list wire.RoomSourceGrantsReply
	if r.status != http.StatusOK || json.Unmarshal(r.body, &list) != nil || len(list.Grants) != 1 || list.Grants[0].From != "outcome-review@1.0.0" ||
		list.Grants[0].Scope != wire.RoomSourceScopeAll || list.Grants[0].DecidedBy != "olga" || list.Grants[0].RoomName != "r" {
		t.Fatalf("the console = %d %s", r.status, r.body)
	}
	if r := g.call(http.MethodGet, "/v1/tenant/room-sources", nil, g.as("maya")); r.status != http.StatusForbidden {
		t.Fatalf("a member reads the console = %d", r.status)
	}
	if r := g.call(http.MethodPut, "/v1/tenant/room-sources/"+g.roomID+"/runs", wire.RoomSourceDecision{Scope: "everything"}, g.orgAdmin()); r.status != http.StatusBadRequest {
		t.Fatalf("an unknown scope = %d", r.status)
	}
	if r := g.call(http.MethodPut, "/v1/tenant/room-sources/"+g.roomID+"/runs", wire.RoomSourceDecision{Scope: wire.RoomSourceScopeAll}, g.orgAdmin()); r.status != http.StatusBadRequest {
		t.Fatalf("a grant read by nobody = %d", r.status)
	}
	if r := g.call(http.MethodPut, "/v1/tenant/room-sources/shared/runs", wire.RoomSourceDecision{Scope: wire.RoomSourceScopeAll, Grantee: "jack"}, g.orgAdmin()); r.status != http.StatusNotFound {
		t.Fatalf("a grant to a room that asks nothing = %d", r.status)
	}
}

// The consent has one owner: room_sources.go reads and changes grants and
// opt-ins, alone decides a scope and alone watches a grant (heldGrant,
// watchGrant); the schedule run (whether to offer read_records) only
// reads whether a room asks; read_records reads its grant only through
// heldGrant, so the watch runs before every read; the install records an
// ask through askRoomSources. read_records is the gateway's one reader of
// the organization's runs.
func TestRoomSourceConsentSingleOwner(t *testing.T) {
	grantChanges := regexp.MustCompile(`\.(AskRoomSource|DecideRoomSource|SetRoomSourceWatch|SetRoomSourceOptIn)\(`)
	grantReads := regexp.MustCompile(`\.(RoomSourceGrants|GetRoomSourceGrant|RoomSourceOptIns)\(`)
	scope := regexp.MustCompile(`wire\.RoomSourceScope(All|OptedIn|NonePrivate)\b`)
	reads := map[string]bool{"services/gateway/room_sources.go": true, "services/gateway/schedule_run.go": true}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "cmd", "internal"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/store/") {
			return
		}
		if grantChanges.Match(src) && rel != "services/gateway/room_sources.go" {
			t.Errorf("%s changes a room source's grant; room_sources.go is the consent's one owner", rel)
		}
		if grantReads.Match(src) && !reads[rel] {
			t.Errorf("%s reads a room source's grant itself; room_sources.go is the consent's one owner (heldGrant)", rel)
		}
		if rel == "services/gateway/schedule_records.go" && !strings.Contains(string(src), "s.heldGrant(") {
			t.Errorf("read_records reads its grant without heldGrant: the watch would not run before a read")
		}
		if scope.Match(src) && rel != "services/gateway/room_sources.go" && !strings.HasPrefix(rel, "internal/wire/") {
			t.Errorf("%s decides on a grant's scope; sourceCover.covers (room_sources.go) is the one rule", rel)
		}
		if strings.HasPrefix(rel, "services/gateway/") && strings.Contains(string(src), ".ListAgentRuns(") && rel != "services/gateway/schedule_records.go" {
			t.Errorf("%s reads the organization's runs; read_records (schedule_records.go) is the gateway's one reader, within a grant", rel)
		}
		if strings.HasPrefix(rel, "services/gateway/") && strings.Contains(string(src), "consented: true") && rel != "services/gateway/schedule_records.go" {
			t.Errorf("%s reads a room's history as a consented read; read_records (schedule_records.go) alone does, within a grant", rel)
		}
		if strings.HasPrefix(rel, "services/gateway/") && strings.Contains(string(src), `"/history`) && rel != "services/gateway/assist.go" {
			t.Errorf("%s reads a room's history itself; roomHistory (assist.go) is the gateway's one history read", rel)
		}
	})
}
