package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// noScheduleRuns is the rig's AI schedule runner: no run happens in these
// tests (a room package's schedules are created disabled).
type noScheduleRuns struct{}

func (noScheduleRuns) RunSchedule(context.Context, wire.RoomSchedule, time.Time, bool) (wire.ScheduleRunReply, error) {
	return wire.ScheduleRunReply{}, errors.New("no schedule runs in this test")
}

// schedule stores a schedule of maya's in room.
func (e *appGW) schedule(room, channel, text string, rcpt wire.ScheduleRecipient, rec wire.ScheduleRecurrence) {
	e.t.Helper()
	if _, err := e.b.Schedules.CreateSchedule(e.ctx(), wire.RoomSchedule{ScheduleID: ids.New("sch_"), RoomID: room, ChannelID: channel, CreatedBy: "maya",
		Text: text, Recipient: rcpt, Recurrence: rec, Timezone: "UTC", Enabled: true, CreatedAtMs: 1, UpdatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
}

// reviewRoom sets maya's room up as a room worth saving: a "reports"
// channel, the room expert (her organization's reviewer), two skill packs
// (her own charts, her organization's palettes), a prompt pack, an app
// copied in from her organization's lunch-poll work, and five schedules:
// an AI task in reports and a notice to the room (kept), and a message to
// an agent, a one-time task and a notice to named people (left out).
func (e *appGW) reviewRoom() {
	e.t.Helper()
	expert := e.content(store.ContentScopeOrg, contentpkg.KindExpert, "reviewer", map[string]any{})
	charts := e.content(store.ContentScopePersonal, contentpkg.KindSkill, "charts", map[string]any{"skills": []string{"charts"}})
	palettes := e.content(store.ContentScopeOrg, contentpkg.KindSkill, "palettes", map[string]any{"skills": []string{"palettes"}})
	prompts := e.content(store.ContentScopeOrg, contentpkg.KindPrompt, "review-prompts", map[string]any{"prompts": []any{map[string]any{"id": "recap", "title": "Recap", "body": "Recap"}}})
	for _, id := range []string{expert, charts, palettes, prompts} {
		c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), id)
		if err := e.b.ContentComponents.PutContentUse(e.ctx(), store.ContentUse{TenantID: e.tenant, ScopeKind: store.ContentUseRoom, ScopeID: e.roomID, ComponentID: id, Kind: c.Kind, CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
			e.t.Fatal(err)
		}
	}
	if r := e.call(http.MethodPut, "/v1/content-uses", wire.ContentUseRequest{Scope: wire.ContentUseRoomExpert, RoomID: e.roomID, ComponentID: expert}, e.as("maya")); r.status != http.StatusNoContent {
		e.t.Fatalf("room expert = %d %s", r.status, r.body)
	}
	reports := ids.New("ch_")
	if err := e.b.Channel.PutChannel(e.ctx(), store.RoomChannel{ChannelID: reports, TenantID: e.tenant, RoomID: e.roomID, Name: "reports", CreatedBy: "maya", CreatedAtMs: 1}); err != nil {
		e.t.Fatal(err)
	}
	daily := wire.ScheduleRecurrence{Kind: wire.RecurDaily, Time: "09:00"}
	e.schedule(e.roomID, reports, "Summarize yesterday.", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, daily)
	e.schedule(e.roomID, "", "Week {week}", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: json.RawMessage(`"room"`), Title: "Review", URL: "/rooms/x"}, daily)
	e.schedule(e.roomID, "", "Status?", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent, OwnerID: "jack", Provider: "codex"}, daily)
	e.schedule(e.roomID, "", "Once.", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAI}, wire.ScheduleRecurrence{Kind: wire.RecurOnce, AtMs: 1})
	e.schedule(e.roomID, "", "Hi", wire.ScheduleRecipient{Kind: wire.ScheduleRecipientNotice, To: json.RawMessage(`["jack"]`), Title: "Hi"}, daily)

	work := e.publishToOrg(e.publishApp("maya", e.pollSite(), wire.WorkPublishRequest{Name: "lunch-poll"}))
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/use-app", wire.AppUseRequest{RoomID: e.roomID}, e.as("maya")); r.status != http.StatusCreated {
		e.t.Fatalf("use-app = %d %s", r.status, r.body)
	}
}

// draft is what "Save as component" captures of room as subject.
func (e *appGW) draft(subject, room string) (wire.RoomComponentDraft, resp) {
	e.t.Helper()
	r := e.call(http.MethodGet, "/v1/rooms/"+room+"/component-draft", nil, e.as(subject))
	var d wire.RoomComponentDraft
	if r.status == http.StatusOK && json.Unmarshal(r.body, &d) != nil {
		e.t.Fatalf("draft = %s", r.body)
	}
	return d, r
}

// The draft holds what a package can: the channels, the AI task and the
// room notice (its link dropped), the room expert, the packs and the app
// copied from a work, each by "name@^major"; the rest is said. Only the
// room's owner or an admin captures it.
func TestRoomComponentDraft(t *testing.T) {
	e := galleryGW(t)
	e.reviewRoom()
	for _, who := range []string{"jack", "olaf"} {
		if _, r := e.draft(who, e.roomID); r.status != http.StatusForbidden {
			t.Fatalf("draft as %s = %d %s", who, r.status, r.body)
		}
	}
	d, r := e.draft("maya", e.roomID)
	if r.status != http.StatusOK {
		t.Fatalf("draft = %d %s", r.status, r.body)
	}
	if d.Name != "r" || d.DisplayName != "r" || !slices.Equal(d.Channels, []string{"reports"}) {
		t.Fatalf("draft = %+v", d)
	}
	kinds := map[string]wire.RoomPackageSchedule{}
	for _, sc := range d.Schedules {
		kinds[sc.Recipient.Kind] = sc
	}
	if ai, notice := kinds[wire.ScheduleRecipientAI], kinds[wire.ScheduleRecipientNotice]; len(d.Schedules) != 2 || ai.Channel != "reports" ||
		notice.Text != "Week {week}" || notice.Channel != "" || notice.Recipient.URL != "" {
		t.Fatalf("draft schedules = %+v", d.Schedules)
	}
	if d.Expert == nil || d.Expert.Ref != "reviewer@^1" || len(d.Prompts) != 1 || d.Prompts[0].Ref != "review-prompts@^1" ||
		len(d.Apps) != 1 || d.Apps[0].Ref != "lunch-poll@^1" {
		t.Fatalf("draft parts = expert %+v prompts %+v apps %+v", d.Expert, d.Prompts, d.Apps)
	}
	skills := []string{}
	for _, s := range d.Skills {
		skills = append(skills, s.Ref)
	}
	slices.Sort(skills)
	if !slices.Equal(skills, []string{"charts@^1", "palettes@^1"}) {
		t.Fatalf("draft skills = %v", skills)
	}
	left := strings.Join(d.Left, "\n")
	for _, want := range []string{"1 schedule posting as a person or driving an agent", "1 schedule notifying named people", "1 schedule running once",
		"the link of 1 scheduled notice", "members and their agents"} {
		if !strings.Contains(left, want) {
			t.Errorf("left = %q, want %q", left, want)
		}
	}
}

// roomForm is the content form a member saves from a draft (every part
// ticked) as a room package, with the runs source asked for.
func roomForm(d wire.RoomComponentDraft, name string) contentpkg.Form {
	f := contentpkg.Form{Kind: "room", Name: name, DisplayName: "Weekly review", Description: "Reviews the week.",
		Welcome: "Turn the daily summary on.", Channels: d.Channels, Schedules: d.Schedules, Sources: []string{wire.RoomSourceRuns}}
	if d.Expert != nil {
		f.Requires.Expert = d.Expert.Ref
	}
	for _, p := range d.Skills {
		f.Requires.Skills = append(f.Requires.Skills, p.Ref)
	}
	for _, p := range d.Prompts {
		f.Requires.Prompts = append(f.Requires.Prompts, p.Ref)
	}
	for _, p := range d.Apps {
		f.Apps = append(f.Apps, p.Ref)
	}
	return f
}

// saveRoomPackage saves form as subject's and publishes it to their
// organization (which publishes without approval here): the
// organization's row.
func (e *appGW) saveRoomPackage(subject string, f contentpkg.Form) wire.ContentComponent {
	e.t.Helper()
	r := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": f}, e.as(subject))
	var saved wire.ContentComponentDetail
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &saved) != nil {
		e.t.Fatalf("save room package = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/content-components/"+saved.Component.ID+"/publish", wire.ContentPublishInput{Scope: "org"}, e.as(subject)); r.status != http.StatusCreated {
		e.t.Fatalf("publish = %d %s", r.status, r.body)
	}
	list := e.call(http.MethodGet, "/v1/content-components?kind=room", nil, e.as("jack"))
	var lib struct {
		Components []wire.ContentComponent `json:"components"`
	}
	_ = json.Unmarshal(list.body, &lib)
	for _, c := range lib.Components {
		if c.Name == f.Name && c.Scope == "org" {
			return c
		}
	}
	e.t.Fatalf("jack's Library has no %s: %s", f.Name, list.body)
	return wire.ContentComponent{}
}

// A room package saved from a draft installs as a room its installer owns:
// the description, channels, the room expert and the packs they may use
// turned on, the app copied in, the schedules created paused and theirs,
// the sources only reported; the install counts as a use. What is not
// theirs to use is reported, never granted.
func TestCreateRoomFromComponent(t *testing.T) {
	e := galleryGW(t)
	e.reviewRoom()
	d, _ := e.draft("maya", e.roomID)
	pkg := e.saveRoomPackage("maya", roomForm(d, "weekly-review"))
	if pkg.Kind != "room" || pkg.Room == nil || !slices.Equal(pkg.Room.Sources, []string{wire.RoomSourceRuns}) || pkg.Requires.Expert != "reviewer@^1" || len(pkg.Room.Schedules) != 2 {
		t.Fatalf("the package's view = %+v", pkg)
	}

	r := e.call(http.MethodPost, "/v1/content-components/"+pkg.ID+"/create-room", wire.RoomInstallRequest{RoomID: "jacks-review", Name: "Jack's review"}, e.as("jack"))
	var got wire.RoomInstallReply
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &got) != nil {
		t.Fatalf("create-room = %d %s", r.status, r.body)
	}
	if got.RoomID != "jacks-review" || got.From != "weekly-review@1.0.0" || !slices.Equal(got.Channels, []string{"reports"}) || got.Expert != "Reviewer" ||
		!slices.Equal(got.Sources, []string{wire.RoomSourceRuns}) || got.Welcome != "Turn the daily summary on." || len(got.Apps) != 1 || len(got.Schedules) != 2 {
		t.Fatalf("reply = %+v", got)
	}
	if !slices.Equal(got.Skipped, []string{"skill pack charts@^1 (not one you can use)"}) {
		t.Fatalf("skipped = %v", got.Skipped)
	}
	room, err := e.b.Room.GetRoom(e.ctx(), "jacks-review")
	if err != nil || room.Name != "Jack's review" || room.Description != "Reviews the week." {
		t.Fatalf("the room = %+v, %v", room, err)
	}
	if role, ok, _ := e.b.Room.MemberRole(e.ctx(), "jacks-review", "jack"); !ok || role != store.RoleOwner {
		t.Fatalf("jack's role = %q %v", role, ok)
	}
	if _, ok, _ := e.b.Room.MemberRole(e.ctx(), "jacks-review", "maya"); ok {
		t.Fatal("the package's author was added to the installer's room")
	}
	channels, _ := e.b.Channel.ListChannels(e.ctx(), "jacks-review")
	reports := ""
	for _, c := range channels {
		if c.Name == "reports" {
			reports = c.ChannelID
		}
	}
	scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), "jacks-review")
	if len(scheds) != 2 || reports == "" {
		t.Fatalf("schedules = %+v (reports %q)", scheds, reports)
	}
	for _, sc := range scheds {
		if sc.Enabled || sc.CreatedBy != "jack" || sc.NextRunAtMs != 0 {
			t.Fatalf("a schedule not paused or not jack's: %+v", sc)
		}
		if want := map[string]string{wire.ScheduleRecipientAI: reports, wire.ScheduleRecipientNotice: ""}[sc.Recipient.Kind]; sc.ChannelID != want {
			t.Fatalf("the %s schedule is in channel %q, want %q", sc.Recipient.Kind, sc.ChannelID, want)
		}
	}
	uses, _ := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoom, "jacks-review")
	on := []string{}
	for _, u := range uses {
		c, _ := e.b.ContentComponents.GetContentComponent(e.ctx(), u.ComponentID)
		on = append(on, c.Name)
	}
	slices.Sort(on)
	if !slices.Equal(on, []string{"palettes", "review-prompts", "reviewer"}) {
		t.Fatalf("turned on = %v", on)
	}
	if expert, _ := e.gw.roomExpertOf(e.ctx(), "jacks-review"); expert == "" {
		t.Fatal("no room expert")
	}
	apps, _ := e.b.Artifacts.ListArtifacts(e.ctx(), store.ArtifactQuery{RoomID: "jacks-review", Limit: 10})
	if len(apps) != 1 || apps[0].Kind != store.ArtifactKindSite || apps[0].CreatedBy != "jack" {
		t.Fatalf("the room's apps = %+v", apps)
	}
	if installs, _ := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoomInstall, "jacks-review"); len(installs) != 1 || installs[0].ComponentID != pkg.ID {
		t.Fatalf("install uses = %+v", installs)
	}

	// The id is taken now; a work is not a room package; someone else's
	// personal package is not there.
	if r := e.call(http.MethodPost, "/v1/content-components/"+pkg.ID+"/create-room", wire.RoomInstallRequest{RoomID: "jacks-review"}, e.as("jack")); r.status != http.StatusConflict {
		t.Fatalf("a taken room id = %d %s", r.status, r.body)
	}
	work := e.libraryApp("jack", "lunch-poll")
	if r := e.call(http.MethodPost, "/v1/content-components/"+work.ID+"/create-room", nil, e.as("jack")); r.status != http.StatusBadRequest {
		t.Fatalf("a work = %d %s", r.status, r.body)
	}
	mine := e.call(http.MethodPost, "/v1/content-components", map[string]any{"form": contentpkg.Form{Kind: "room", Name: "private-room"}}, e.as("maya"))
	var private wire.ContentComponentDetail
	_ = json.Unmarshal(mine.body, &private)
	if r := e.call(http.MethodPost, "/v1/content-components/"+private.Component.ID+"/create-room", nil, e.as("jack")); r.status != http.StatusNotFound {
		t.Fatalf("maya's personal package as jack = %d %s", r.status, r.body)
	}
}

// An install that fails once the room exists leaves no room behind: its
// channels and schedules go with it, and nothing counts as a use.
func TestCreateRoomFromComponentIsAtomic(t *testing.T) {
	e := galleryGW(t)
	// A stored version whose second schedule realtime refuses (its text is
	// past a message's: a save would have refused it, a stored row may not).
	bad := e.content(store.ContentScopeOrg, contentpkg.KindRoom, "broken-room", map[string]any{
		"channels": []string{"reports"},
		"schedules": []any{
			map[string]any{"text": "Fine.", "recipient": map[string]any{"kind": "ai"}, "recurrence": map[string]any{"kind": "daily", "time": "09:00"}, "timezone": "UTC"},
			map[string]any{"text": strings.Repeat("x", wire.MaxChatTextBytes+1), "recipient": map[string]any{"kind": "ai"}, "recurrence": map[string]any{"kind": "daily", "time": "09:00"}, "timezone": "UTC"},
		},
	})
	r := e.call(http.MethodPost, "/v1/content-components/"+bad+"/create-room", wire.RoomInstallRequest{RoomID: "doomed"}, e.as("jack"))
	if r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("create-room = %d %s", r.status, r.body)
	}
	if _, err := e.b.Room.GetRoom(e.ctx(), "doomed"); !errors.Is(err, store.ErrRoomNotFound) {
		t.Fatalf("the half-made room = %v, want gone", err)
	}
	if scheds, _ := e.b.Schedules.ListSchedules(e.ctx(), "doomed"); len(scheds) != 0 {
		t.Fatalf("schedules left = %+v", scheds)
	}
	if channels, _ := e.b.Channel.ListChannels(e.ctx(), "doomed"); len(channels) != 0 {
		t.Fatalf("channels left = %+v", channels)
	}
	if installs, _ := e.b.ContentComponents.ListContentUses(e.ctx(), store.ContentUseRoomInstall, "doomed"); len(installs) != 0 {
		t.Fatalf("install counted = %+v", installs)
	}
}
