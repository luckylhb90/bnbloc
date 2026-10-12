package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// captureRoomEvents puts a recorder in front of the gateway's realtime: it
// keeps the events published to rooms and passes everything else through.
func captureRoomEvents(t *testing.T, g *contentGateway) func() []wire.Event {
	t.Helper()
	target, _ := url.Parse(g.svc.Backends.Realtime)
	proxy := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	var events []wire.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/v1/rooms/") && strings.HasSuffix(r.URL.Path, "/publish") {
			raw, _ := io.ReadAll(r.Body)
			var ev wire.Event
			_ = json.Unmarshal(raw, &ev)
			ev.RoomID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/v1/rooms/"), "/publish")
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	g.svc.Backends.Realtime = srv.URL
	return func() []wire.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]wire.Event(nil), events...)
	}
}

func (g *contentGateway) create(user string, form map[string]any) wire.ContentComponent {
	g.t.Helper()
	code, raw := g.json(user, http.MethodPost, "/v1/content-components", map[string]any{"form": form})
	if code != http.StatusCreated {
		g.t.Fatalf("create %v = %d %s", form["name"], code, raw)
	}
	return decodeDetail(g.t, raw).Component
}

func (g *contentGateway) library(user, kind string) map[string]wire.ContentComponent {
	g.t.Helper()
	code, raw := g.json(user, http.MethodGet, "/v1/content-components?kind="+kind, nil)
	if code != http.StatusOK {
		g.t.Fatalf("list = %d %s", code, raw)
	}
	var out struct {
		Components []wire.ContentComponent `json:"components"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		g.t.Fatalf("decode %s: %v", raw, err)
	}
	byName := map[string]wire.ContentComponent{}
	for _, c := range out.Components {
		byName[c.Name] = c
	}
	return byName
}

// A team saved by form lists its roster; experts show their runs and
// distinct requesters, packs the members who turned them on.
func TestContentTeamAndUsageCounts(t *testing.T) {
	g := newContentGateway(t)
	reviewer := g.create("maya", map[string]any{"kind": "expert", "name": "code-reviewer", "instructions": "Review."})
	team := g.create("maya", map[string]any{"kind": "expert", "name": "release-team", "instructions": "Plan, delegate, merge.",
		"team": map[string]any{"lead": "coordinator", "members": []map[string]any{{"role": "reviewer", "expert": "code-reviewer@^1", "defaultAgent": "member:jack"}}}})
	if team.Team == nil || team.Team.Lead != "coordinator" || len(team.Team.Members) != 1 || team.Team.Members[0].DefaultAgent != "member:jack" {
		t.Fatalf("team = %+v", team.Team)
	}
	if code, raw := g.json("maya", http.MethodPost, "/v1/content-components", map[string]any{"form": map[string]any{"kind": "expert", "name": "bad-team", "instructions": "x",
		"team": map[string]any{"members": []map[string]any{{"role": "r", "expert": "x", "defaultAgent": "anyone"}}}}}); code != http.StatusBadRequest {
		t.Fatalf("bad team = %d %s", code, raw)
	}
	pack := g.create("maya", map[string]any{"kind": "skill", "name": "kit", "skills": []map[string]string{{"name": "lint", "description": "Use to lint", "body": "run lint"}}})

	ctx := tenant.NewContext(t.Context(), "home")
	for i, requester := range []string{"maya", "maya", "bob"} {
		if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-" + requester + string(rune('a'+i)), RoomID: "room-1", MessageSeq: int64(i + 1), RequesterUserID: requester,
			OwnerUserID: "maya", Provider: "codex", ExpertID: reviewer.ID, ExpertVersion: "1.0.0", CreatedAtMs: time.Now().UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: "member", ComponentID: pack.ID}); code != http.StatusNoContent {
		t.Fatalf("enable pack = %d %s", code, raw)
	}
	lib := g.library("maya", "expert")
	if c := lib["code-reviewer"]; c.Runs != 3 || c.Members != 2 {
		t.Fatalf("reviewer usage = %d runs, %d members", c.Runs, c.Members)
	}
	if c := lib["release-team"]; c.Runs != 0 || c.Team == nil {
		t.Fatalf("team listing = %+v", c)
	}
	if c := g.library("maya", "skill")["kit"]; c.Members != 1 || c.Runs != 0 {
		t.Fatalf("pack usage = %d runs, %d members", c.Runs, c.Members)
	}
}

// The room expert: one of the room's pinned experts (never a team), set by
// the room's owner or admins, replaced by the next, cleared with its pin;
// every change of the room's uses is announced to the room.
func TestContentRoomExpert(t *testing.T) {
	g := newContentGateway(t)
	g.svc.limiter.get(1000, 1000, nil) // more writes than one member's chat burst
	events := captureRoomEvents(t, g)
	house := g.create("maya", map[string]any{"kind": "expert", "name": "house-style", "instructions": "Write plainly."})
	other := g.create("maya", map[string]any{"kind": "expert", "name": "other", "instructions": "Other."})
	team := g.create("maya", map[string]any{"kind": "expert", "name": "crew", "instructions": "Lead.",
		"team": map[string]any{"members": []map[string]any{{"role": "a", "expert": "other", "defaultAgent": "platform"}}}})
	put := func(user string, req wire.ContentUseRequest) (int, string) {
		code, raw := g.json(user, http.MethodPut, "/v1/content-uses", req)
		return code, string(raw)
	}
	roomExpert := func(id string) wire.ContentUseRequest {
		return wire.ContentUseRequest{Scope: wire.ContentUseRoomExpert, RoomID: "room-1", ComponentID: id}
	}
	if code, body := put("maya", roomExpert(house.ID)); code != http.StatusBadRequest || !strings.Contains(body, "pin the expert") {
		t.Fatalf("unpinned room expert = %d %s", code, body)
	}
	for _, c := range []wire.ContentComponent{house, other, team} {
		if code, body := put("maya", wire.ContentUseRequest{Scope: "room", RoomID: "room-1", ComponentID: c.ID}); code != http.StatusNoContent {
			t.Fatalf("pin %s = %d %s", c.Name, code, body)
		}
	}
	if code, body := put("maya", roomExpert(team.ID)); code != http.StatusBadRequest || !strings.Contains(body, "team") {
		t.Fatalf("team as room expert = %d %s", code, body)
	}
	if code, _ := put("jack", roomExpert(house.ID)); code != http.StatusForbidden {
		t.Fatalf("a member set the room expert: %d", code)
	}
	if code, body := put("maya", roomExpert(house.ID)); code != http.StatusNoContent {
		t.Fatalf("room expert = %d %s", code, body)
	}
	uses := func() wire.ContentUsesReply {
		code, raw := g.json("jack", http.MethodGet, "/v1/content-uses?room=room-1", nil)
		var out wire.ContentUsesReply
		if code != http.StatusOK || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("uses = %d %s", code, raw)
		}
		return out
	}
	if got := uses(); got.RoomExpert != house.ID || len(got.Room) != 3 {
		t.Fatalf("uses = %+v", got)
	}
	if code, body := put("maya", roomExpert(other.ID)); code != http.StatusNoContent {
		t.Fatalf("replace = %d %s", code, body)
	}
	if got := uses(); got.RoomExpert != other.ID {
		t.Fatalf("replaced = %q", got.RoomExpert)
	}
	if code, raw := g.json("maya", http.MethodDelete, "/v1/content-uses?scope=room&roomId=room-1&componentId="+other.ID, nil); code != http.StatusNoContent {
		t.Fatalf("unpin = %d %s", code, raw)
	}
	if got := uses(); got.RoomExpert != "" || len(got.Room) != 2 {
		t.Fatalf("after unpin = %+v", got)
	}
	// The room expert is not a second use of the room.
	if c := g.library("maya", "expert")["house-style"]; c.Uses != 1 {
		t.Fatalf("house uses = %d", c.Uses)
	}

	var changed []string
	for _, ev := range events() {
		if ev.Kind == wire.EventContentUsesChanged && ev.RoomID == "room-1" {
			p, _ := json.Marshal(ev.Payload)
			var c wire.ContentUsesChanged
			_ = json.Unmarshal(p, &c)
			changed = append(changed, c.Scope)
		}
	}
	if strings.Join(changed, ",") != "room,room,room,room-expert,room-expert,room" {
		t.Fatalf("content_uses_changed scopes = %v", changed)
	}
}

// The bridge resolving a job's turn records the room expert it wears on
// the job (its run record) and audits the use once, like the chat's expert.
func TestContentEffectiveRecordsRoomExpert(t *testing.T) {
	g := newContentGateway(t)
	audit := store.NewMemoryExtensionAudit()
	g.svc.ExtAudit = audit
	house := g.create("maya", map[string]any{"kind": "expert", "name": "house-style", "displayName": "House style", "instructions": "Write plainly."})
	for _, req := range []wire.ContentUseRequest{
		{Scope: "room", RoomID: "room-1", ComponentID: house.ID},
		{Scope: wire.ContentUseRoomExpert, RoomID: "room-1", ComponentID: house.ID},
	} {
		if code, raw := g.json("maya", http.MethodPut, "/v1/content-uses", req); code != http.StatusNoContent {
			t.Fatalf("use %s = %d %s", req.Scope, code, raw)
		}
	}
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	job, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-re", RoomID: "room-1", MessageSeq: 4, RequesterUserID: "maya", OwnerUserID: "jack", Core: "acme", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/content-components/effective?job=" + job.JobID + "&room=room-1"
	for range 2 { // a retried resolution records nothing new
		code, raw := g.do("jack", http.MethodGet, path, "", nil)
		var eff wire.ContentEffective
		if code != http.StatusOK || json.Unmarshal(raw, &eff) != nil || eff.RoomExpert == nil || eff.RoomExpert.ComponentID != house.ID {
			t.Fatalf("effective = %d %s", code, raw)
		}
	}
	got, err := g.jobs.GetAgentRun(ctx, job.JobID)
	if err != nil || got.RoomExpertID != house.ID || got.RoomExpertVersion != "1.0.0" || got.RoomExpertName != "House style" {
		t.Fatalf("job = %+v, %v", got, err)
	}
	recs, _ := audit.ExtAuditRecords(ctx, wire.AuditKindContent, 0, 0)
	var uses []store.ExtAuditRecord
	for _, r := range recs {
		if r.Action == "use" {
			uses = append(uses, r)
		}
	}
	if len(uses) != 1 || uses[0].Actor != "maya" || uses[0].Name != "house-style@1.0.0" ||
		uses[0].Detail != "room expert House style v1.0.0 on agent jack/acme/codex (job job-re, room room-1)" {
		t.Fatalf("audit uses = %+v", uses)
	}
}
