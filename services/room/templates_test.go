package room

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type tplFixture struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *Service
	sched *store.MemoryRoomSchedules
}

func newTplFixture(t *testing.T) *tplFixture {
	t.Helper()
	srv, svc := newTestServer(t)
	f := &tplFixture{t: t, srv: srv, svc: svc, sched: store.NewMemoryRoomSchedules()}
	svc.Templates = store.NewMemoryRoomTemplates()
	svc.Schedules = f.sched
	return f
}

func (f *tplFixture) call(method, path, user string, body any) (int, []byte) {
	f.t.Helper()
	resp := do(f.t, method, f.srv.URL+path, user, body)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (f *tplFixture) createTemplate(user string, body map[string]any) wire.RoomTemplate {
	f.t.Helper()
	code, out := f.call(http.MethodPost, "/v1/room-templates", user, body)
	if code != http.StatusCreated {
		f.t.Fatalf("create template = %d %s", code, out)
	}
	var tpl wire.RoomTemplate
	_ = json.Unmarshal(out, &tpl)
	return tpl
}

func sampleTemplateBody() map[string]any {
	return map[string]any{
		"name": "Weekly review", "visibility": "tenant",
		"body": map[string]any{
			"roomDescription": "Design reviews",
			"channels":        []string{"design", "General", "design", "ops"},
			"starterPrompts":  []string{"Summarize open PRs", ""},
			"suggestedAgents": []map[string]any{{"ownerId": "dave", "provider": "codex", "core": "c1"}},
			"defaultMembers":  []string{"bob", "carol", "bob"},
			"schedules": []map[string]any{{
				"channel": "design", "text": "Weekly digest please", "timezone": "Europe/Berlin",
				"recipient":  map[string]any{"kind": "agent", "ownerId": "dave", "provider": "codex", "core": "c1"},
				"recurrence": map[string]any{"kind": "weekly", "time": "09:30", "days": []int{1}},
			}},
		},
	}
}

func TestTemplateCreateNormalizesAndValidates(t *testing.T) {
	f := newTplFixture(t)
	tpl := f.createTemplate("alice", sampleTemplateBody())
	b := tpl.Body
	if strings.Join(b.Channels, ",") != "design,ops" || len(b.StarterPrompts) != 1 || strings.Join(b.DefaultMembers, ",") != "bob,carol" {
		t.Fatalf("normalized body = %+v", b)
	}
	bad := []map[string]any{
		{"name": ""},
		{"name": "x", "visibility": "public"},
		{"name": "x", "body": map[string]any{"suggestedAgents": []map[string]any{{"provider": "codex"}}}},
		{"name": "x", "body": map[string]any{"schedules": []map[string]any{{"text": "t", "timezone": "UTC",
			"recurrence": map[string]any{"kind": "cron", "cron": "* * * * *"}}}}},
		{"name": "x", "body": map[string]any{"schedules": []map[string]any{{"text": "t", "timezone": "UTC", "channel": "nope",
			"recurrence": map[string]any{"kind": "daily", "time": "09:00"}}}}},
	}
	for i, body := range bad {
		if code, out := f.call(http.MethodPost, "/v1/room-templates", "alice", body); code != http.StatusBadRequest {
			t.Errorf("case %d = %d %s, want 400", i, code, out)
		}
	}
}

func TestTemplateAuthorization(t *testing.T) {
	f := newTplFixture(t)
	shared := f.createTemplate("alice", sampleTemplateBody())
	private := f.createTemplate("alice", map[string]any{"name": "Mine"})
	// Bob sees the tenant template only, cannot edit or delete it.
	code, out := f.call(http.MethodGet, "/v1/room-templates", "bob", nil)
	if code != http.StatusOK || !strings.Contains(string(out), shared.TemplateID) || strings.Contains(string(out), private.TemplateID) {
		t.Fatalf("bob list = %d %s", code, out)
	}
	if code, _ := f.call(http.MethodGet, "/v1/room-templates/"+private.TemplateID, "bob", nil); code != http.StatusNotFound {
		t.Fatalf("bob get private = %d, want 404", code)
	}
	if code, _ := f.call(http.MethodPatch, "/v1/room-templates/"+shared.TemplateID, "bob", map[string]any{"name": "Mine now"}); code != http.StatusForbidden {
		t.Fatalf("bob patch = %d, want 403", code)
	}
	if code, _ := f.call(http.MethodDelete, "/v1/room-templates/"+shared.TemplateID, "bob", nil); code != http.StatusForbidden {
		t.Fatalf("bob delete = %d, want 403", code)
	}
	// The author edits.
	if code, out := f.call(http.MethodPatch, "/v1/room-templates/"+shared.TemplateID, "alice", map[string]any{"name": "Review"}); code != http.StatusOK || !strings.Contains(string(out), `"Review"`) {
		t.Fatalf("alice patch = %d %s", code, out)
	}
	// The org-admin moderation path deletes tenant-visible templates only.
	if code, _ := f.call(http.MethodDelete, "/v1/tenant/room-templates/"+private.TemplateID, "admin", nil); code != http.StatusNotFound {
		t.Fatalf("moderate private = %d, want 404", code)
	}
	if code, _ := f.call(http.MethodDelete, "/v1/tenant/room-templates/"+shared.TemplateID, "admin", nil); code != http.StatusNoContent {
		t.Fatalf("moderate shared = %d, want 204", code)
	}
	if code, _ := f.call(http.MethodDelete, "/v1/room-templates/"+private.TemplateID, "alice", nil); code != http.StatusNoContent {
		t.Fatalf("author delete = %d", code)
	}
}

func TestInstantiateCreatesRoomChannelsMembersDisabledSchedules(t *testing.T) {
	f := newTplFixture(t)
	tpl := f.createTemplate("alice", sampleTemplateBody())
	// Bob instantiates alice's tenant template: bob owns the new room.
	code, out := f.call(http.MethodPost, "/v1/room-templates/"+tpl.TemplateID+"/instantiate", "bob", map[string]any{"roomId": "review-1", "name": "Review #1"})
	if code != http.StatusOK {
		t.Fatalf("instantiate = %d %s", code, out)
	}
	var res wire.RoomTemplateInstance
	_ = json.Unmarshal(out, &res)
	if res.RoomID != "review-1" || strings.Join(res.Channels, ",") != "design,ops" || len(res.Schedules) != 1 {
		t.Fatalf("instance = %+v", res)
	}
	ctx := dctx()
	rm, err := f.svc.Store.GetRoom(ctx, "review-1")
	if err != nil || rm.Name != "Review #1" || rm.Description != "Design reviews" || rm.Visibility != store.VisibilityPrivate {
		t.Fatalf("room = %+v, %v", rm, err)
	}
	role, _, err := f.svc.Store.MemberRole(ctx, "review-1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if role != store.RoleOwner {
		t.Fatalf("instantiator role = %s, want owner", role)
	}
	// Default members join as members (bob, the creator, is skipped).
	role, ok, err := f.svc.Store.MemberRole(ctx, "review-1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || role != store.RoleMember {
		t.Fatalf("carol = %s %v", role, ok)
	}
	if strings.Join(res.AddedMembers, ",") != "carol" {
		t.Fatalf("added members = %v", res.AddedMembers)
	}
	var names []string
	design := ""
	channels, err := f.svc.Channels.ListChannels(ctx, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range channels {
		names = append(names, c.Name)
		if c.Name == "design" {
			design = c.ChannelID
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "design,general,ops" {
		t.Fatalf("channels = %v", names)
	}
	// The schedule exists, owned by the instantiator, DISABLED, in the
	// template's channel, with no planned run.
	list, _ := f.sched.ListSchedules(ctx, "review-1")
	if len(list) != 1 {
		t.Fatalf("schedules = %+v", list)
	}
	sc := list[0]
	if sc.Enabled || sc.NextRunAtMs != 0 || sc.CreatedBy != "bob" || sc.ChannelID != design || sc.Recipient.OwnerID != "dave" || sc.Recurrence.Days[0] != 1 {
		t.Fatalf("schedule = %+v", sc)
	}
	// Re-using the room id is a conflict, like POST /v1/rooms.
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/"+tpl.TemplateID+"/instantiate", "bob", map[string]any{"roomId": "review-1"}); code != http.StatusConflict {
		t.Fatalf("duplicate room id = %d, want 409", code)
	}
	if got, _ := f.svc.Templates.GetTemplate(ctx, tpl.TemplateID, "bob"); got.UseCount != 1 {
		t.Fatalf("use count = %d", got.UseCount)
	}
}

// Suggested agents are hints: their owners are not added to the room, and
// the room service (which has no grant path at all) creates no authorization.
// The response hands them to the UI, which offers invite + request-borrow.
func TestInstantiateNeverAuthorizesSuggestedAgents(t *testing.T) {
	f := newTplFixture(t)
	tpl := f.createTemplate("alice", map[string]any{
		"name": "Pairing", "body": map[string]any{
			"suggestedAgents": []map[string]any{{"ownerId": "dave", "provider": "codex"}, {"ownerId": "erin", "provider": "claude"}},
		},
	})
	code, out := f.call(http.MethodPost, "/v1/room-templates/"+tpl.TemplateID+"/instantiate", "alice", map[string]any{"roomId": "pair-1"})
	if code != http.StatusOK {
		t.Fatalf("instantiate = %d %s", code, out)
	}
	var res wire.RoomTemplateInstance
	_ = json.Unmarshal(out, &res)
	if len(res.SuggestedAgents) != 2 || len(res.AddedMembers) != 0 {
		t.Fatalf("instance = %+v", res)
	}
	for _, owner := range []string{"dave", "erin"} {
		isMember, err := f.svc.Store.IsMember(dctx(), "pair-1", owner)
		if err != nil {
			t.Fatal(err)
		}
		if isMember {
			t.Fatalf("suggested agent owner %s was added to the room", owner)
		}
	}
	members, err := f.svc.Store.Members(dctx(), "pair-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UserID != "alice" {
		t.Fatalf("members = %+v", members)
	}
}

func TestTemplateFromRoom(t *testing.T) {
	f := newTplFixture(t)
	if code, out := f.call(http.MethodPost, "/v1/rooms", "olga", map[string]any{"roomId": "src", "name": "Source", "description": "the source"}); code != http.StatusOK {
		t.Fatalf("create room = %d %s", code, out)
	}
	f.call(http.MethodPost, "/v1/rooms/src/channels", "olga", map[string]any{"name": "triage"})
	f.call(http.MethodPost, "/v1/rooms/src/members", "olga", map[string]any{"userId": "bob", "role": "member"})
	f.call(http.MethodPost, "/v1/rooms/src/members", "olga", map[string]any{"userId": "amy", "role": "admin"})
	var triage string
	channels, err := f.svc.Channels.ListChannels(dctx(), "src")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range channels {
		if c.Name == "triage" {
			triage = c.ChannelID
		}
	}
	_, _ = f.sched.CreateSchedule(dctx(), wire.RoomSchedule{
		ScheduleID: "sch_x", RoomID: "src", ChannelID: triage, CreatedBy: "bob", Text: "triage now",
		Recipient: wire.ScheduleRecipient{Kind: wire.ScheduleRecipientRoom}, Timezone: "UTC",
		Recurrence: wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "08:00"}, Enabled: true, NextRunAtMs: 5,
	})
	req := map[string]any{
		"visibility": "tenant", "includeMembers": true, "includeSchedules": true,
		"starterPrompts":  []string{"What broke overnight?"},
		"suggestedAgents": []map[string]any{{"ownerId": "bob", "provider": "codex"}},
	}
	// A plain member may not capture the room.
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/from-room/src", "bob", req); code != http.StatusForbidden {
		t.Fatalf("member capture = %d, want 403", code)
	}
	// An admin may.
	code, out := f.call(http.MethodPost, "/v1/room-templates/from-room/src", "amy", req)
	if code != http.StatusCreated {
		t.Fatalf("admin capture = %d %s", code, out)
	}
	var tpl wire.RoomTemplate
	_ = json.Unmarshal(out, &tpl)
	b := tpl.Body
	sort.Strings(b.DefaultMembers)
	if tpl.Name != "Source" || tpl.CreatedBy != "amy" || b.RoomDescription != "the source" || strings.Join(b.Channels, ",") != "triage" ||
		strings.Join(b.DefaultMembers, ",") != "bob,olga" || len(b.StarterPrompts) != 1 || len(b.SuggestedAgents) != 1 ||
		len(b.Schedules) != 1 || b.Schedules[0].Channel != "triage" || b.Schedules[0].Text != "triage now" {
		t.Fatalf("captured = %+v", tpl)
	}
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/from-room/src/extra", "amy", req); code != http.StatusNotFound {
		t.Fatalf("bad action path = %d", code)
	}
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/tpl_x/explode", "amy", nil); code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", code)
	}
}
