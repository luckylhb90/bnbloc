package room

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/contentbuiltin"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type tplFixture struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *Service
	sched *store.MemoryRoomSchedules
	seq   int
}

func newTplFixture(t *testing.T) *tplFixture {
	t.Helper()
	srv, svc := newTestServer(t)
	f := &tplFixture{t: t, srv: srv, svc: svc, sched: store.NewMemoryRoomSchedules()}
	svc.Templates = store.NewMemoryRoomTemplates()
	svc.Schedules = f.sched
	svc.Content = contentbuiltin.Overlay(store.NewMemoryContentComponents())
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
			"promptPacks":     []map[string]any{{"componentId": starterPack}, {"componentId": " "}, {"componentId": starterPack}},
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
	if strings.Join(b.Channels, ",") != "design,ops" || len(b.PromptPacks) != 1 || strings.Join(b.DefaultMembers, ",") != "bob,carol" {
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
	// The room's prompt packs the saver may use are captured: not another
	// member's personal pack, not a skill pack.
	olgas, amysSkills := f.component("olga", contentpkg.KindPrompt), f.component("amy", contentpkg.KindSkill)
	for _, id := range []string{starterPack, olgas, amysSkills} {
		f.roomUse("src", id, "")
	}
	req := map[string]any{
		"visibility": "tenant", "includeMembers": true, "includeSchedules": true,
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
		strings.Join(b.DefaultMembers, ",") != "bob,olga" || len(b.SuggestedAgents) != 1 ||
		len(b.Schedules) != 1 || b.Schedules[0].Channel != "triage" || b.Schedules[0].Text != "triage now" ||
		len(b.PromptPacks) != 1 || b.PromptPacks[0].ComponentID != starterPack {
		t.Fatalf("captured = %+v", tpl)
	}
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/from-room/src/extra", "amy", req); code != http.StatusNotFound {
		t.Fatalf("bad action path = %d", code)
	}
	if code, _ := f.call(http.MethodPost, "/v1/room-templates/tpl_x/explode", "amy", nil); code != http.StatusNotFound {
		t.Fatalf("unknown action = %d", code)
	}
}

// starterPack is the built-in starters prompt pack, in every member's reach.
const starterPack = contentbuiltin.IDPrefix + "starters"

// component creates owner's personal content component of kind (version
// 1.0.0) and returns its id.
func (f *tplFixture) component(owner, kind string) string {
	f.t.Helper()
	f.seq++
	id := fmt.Sprintf("cc_%s_%d", owner, f.seq)
	c := store.ContentComponent{ID: id, Kind: kind, Name: id, TenantID: tenant.Default, Owner: owner,
		Scope: store.ContentScopePersonal, Status: store.ContentStatusActive, LatestVersion: "1.0.0", CreatedAtMs: 1, UpdatedAtMs: 1}
	v := store.ContentComponentVersion{ComponentID: id, Version: "1.0.0", ManifestJSON: json.RawMessage(`{}`),
		BundleBlobKey: "sha256/" + strings.Repeat("a", 64), SHA256: strings.Repeat("a", 64), CreatedBy: owner, CreatedAtMs: 1}
	if err := f.svc.Content.CreateContentComponent(context.Background(), c, v); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// roomUse turns a component on in a room, as the gateway's content-use
// route does.
func (f *tplFixture) roomUse(roomID, id, version string) {
	f.t.Helper()
	c, err := f.svc.Content.GetContentComponent(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.svc.Content.PutContentUse(context.Background(), store.ContentUse{TenantID: tenant.Default, ScopeKind: store.ContentUseRoom,
		ScopeID: roomID, ComponentID: id, Kind: c.Kind, PinnedVersion: version, CreatedBy: "olga", CreatedAtMs: 1}); err != nil {
		f.t.Fatal(err)
	}
}

// roomPacks lists the components (with their pins) a room uses.
func (f *tplFixture) roomPacks(roomID string) []string {
	f.t.Helper()
	uses, err := f.svc.Content.ListContentUses(context.Background(), store.ContentUseRoom, roomID)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, u := range uses {
		out = append(out, u.ComponentID+"@"+u.PinnedVersion)
	}
	sort.Strings(out)
	return out
}

// A template's starters are its prompt packs': a room made from it gets
// them as room content uses (by the creator), a pack the creator may not
// use is reported and left out, and a template names only prompt packs
// its author may use.
func TestTemplatePromptPacksBecomeRoomUses(t *testing.T) {
	f := newTplFixture(t)
	alices := f.component("alice", contentpkg.KindPrompt)
	tpl := f.createTemplate("alice", map[string]any{"name": "Packs", "visibility": "tenant", "body": map[string]any{
		"promptPacks": []map[string]any{{"componentId": starterPack}, {"componentId": alices, "version": "1.0.0"}},
	}})
	code, out := f.call(http.MethodPost, "/v1/room-templates/"+tpl.TemplateID+"/instantiate", "alice", map[string]any{"roomId": "packs-a"})
	if code != http.StatusOK {
		t.Fatalf("instantiate = %d %s", code, out)
	}
	var res wire.RoomTemplateInstance
	_ = json.Unmarshal(out, &res)
	if len(res.PromptPacks) != 2 || len(res.FailedPromptPacks) != 0 {
		t.Fatalf("alice's instance = %+v", res)
	}
	if got := strings.Join(f.roomPacks("packs-a"), ","); got != alices+"@1.0.0,"+starterPack+"@" {
		t.Fatalf("room uses = %s", got)
	}
	// Bob may not use alice's personal pack: the room gets the rest.
	code, out = f.call(http.MethodPost, "/v1/room-templates/"+tpl.TemplateID+"/instantiate", "bob", map[string]any{"roomId": "packs-b"})
	if code != http.StatusOK {
		t.Fatalf("bob instantiate = %d %s", code, out)
	}
	res = wire.RoomTemplateInstance{}
	_ = json.Unmarshal(out, &res)
	if strings.Join(res.PromptPacks, ",") != starterPack || strings.Join(res.FailedPromptPacks, ",") != alices {
		t.Fatalf("bob's instance = %+v", res)
	}
	if got := strings.Join(f.roomPacks("packs-b"), ","); got != starterPack+"@" {
		t.Fatalf("bob's room uses = %s", got)
	}
	// Only prompt packs the author may use, at a version that exists.
	skills, bobs := f.component("alice", contentpkg.KindSkill), f.component("bob", contentpkg.KindPrompt)
	for _, p := range []map[string]any{{"componentId": skills}, {"componentId": bobs}, {"componentId": "cc_missing"}, {"componentId": alices, "version": "9.9.9"}} {
		body := map[string]any{"name": "Bad", "body": map[string]any{"promptPacks": []map[string]any{p}}}
		if code, out := f.call(http.MethodPost, "/v1/room-templates", "alice", body); code != http.StatusBadRequest {
			t.Errorf("pack %v = %d %s, want 400", p, code, out)
		}
	}
	if code, out := f.call(http.MethodPatch, "/v1/room-templates/"+tpl.TemplateID, "alice", map[string]any{"body": map[string]any{
		"promptPacks": []map[string]any{{"componentId": bobs}}}}); code != http.StatusBadRequest {
		t.Fatalf("patch to bob's pack = %d %s, want 400", code, out)
	}
	// Without a content store a template cannot name packs.
	f.svc.Content = nil
	if code, out := f.call(http.MethodPost, "/v1/room-templates", "alice", map[string]any{"name": "No store", "body": map[string]any{
		"promptPacks": []map[string]any{{"componentId": starterPack}}}}); code != http.StatusServiceUnavailable {
		t.Fatalf("no content store = %d %s, want 503", code, out)
	}
}
