package room

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The channel setup routes (docs/prd/2026-10-28-channels-and-threads.md,
// slice 1), each rule tested with members of different roles: the room's
// owner (alice), an admin (erin), members (bob, carol) and a visitor (vic).

// count is how many events of kind were published (recordingPublisher,
// appdata_events_test.go).
func (p *recordingPublisher) count(kind string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, ev := range p.evs {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

type setupEnv struct {
	t   *testing.T
	srv *httptest.Server
	svc *Service
	pub *recordingPublisher
	def string // the default channel
}

func newSetupEnv(t *testing.T) *setupEnv {
	t.Helper()
	srv, svc := newTestServer(t)
	pub := &recordingPublisher{}
	svc.Publisher = pub
	e := &setupEnv{t: t, srv: srv, svc: svc, pub: pub}
	e.call(http.MethodPost, "/v1/rooms", "alice", map[string]string{"roomId": "room-1", "name": "Team"}, http.StatusOK, nil)
	for user, role := range map[string]store.Role{"bob": store.RoleMember, "carol": store.RoleMember, "erin": store.RoleAdmin, "vic": store.RoleVisitor} {
		if err := svc.Store.AddMember(dctx(), store.Membership{RoomID: "room-1", UserID: user, Role: role}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range e.list("alice", "").Channels {
		if c.IsDefault {
			e.def = c.ChannelID
		}
	}
	return e
}

// call sends a request as user, asserts its status and decodes the answer
// into out (when not nil); it returns the raw body.
func (e *setupEnv) call(method, path, user string, body any, want int, out any) string {
	e.t.Helper()
	resp := do(e.t, method, e.srv.URL+path, user, body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s as %s = %d (%s), want %d", method, path, user, resp.StatusCode, raw, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatalf("decode %s: %v", raw, err)
		}
	}
	return string(raw)
}

func (e *setupEnv) list(user, query string) wire.ChannelList {
	e.t.Helper()
	var out wire.ChannelList
	e.call(http.MethodGet, "/v1/rooms/room-1/channels"+query, user, nil, http.StatusOK, &out)
	return out
}

func (e *setupEnv) create(user string, req wire.ChannelCreateRequest, want int) wire.RoomChannel {
	e.t.Helper()
	var out wire.RoomChannel
	if want != http.StatusOK {
		e.call(http.MethodPost, "/v1/rooms/room-1/channels", user, req, want, nil)
		return out
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/channels", user, req, want, &out)
	return out
}

func (e *setupEnv) patch(user, channelID string, body any, want int) wire.RoomChannel {
	e.t.Helper()
	var out wire.RoomChannel
	if want != http.StatusOK {
		e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+channelID, user, body, want, nil)
		return out
	}
	e.call(http.MethodPatch, "/v1/rooms/room-1/channels/"+channelID, user, body, want, &out)
	return out
}

func byChannel(list []wire.RoomChannel) map[string]wire.RoomChannel {
	out := map[string]wire.RoomChannel{}
	for _, c := range list {
		out[c.ChannelID] = c
	}
	return out
}

// Who changes a channel's setup: the room's owner and admins and the
// channel's owner (its creator by default); not another member, not a
// visitor. Bounds and values are checked; the people chat is not
// configurable and the default channel keeps its name and admits agents.
func TestChannelPatchAuthority(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design", Purpose: "  how it looks  "}, http.StatusOK)
	if design.OwnerID != "bob" || design.CreatedBy != "bob" || design.Purpose != "how it looks" || !design.Joined {
		t.Fatalf("created channel = %+v: owner and creator bob, purpose trimmed, the creator joined", design)
	}
	before := e.pub.count(wire.EventChannelUpdated)
	purpose := map[string]any{"purpose": "pixels"}
	e.patch("carol", design.ChannelID, purpose, http.StatusForbidden)
	e.patch("vic", design.ChannelID, purpose, http.StatusForbidden)
	for _, who := range []string{"bob", "erin", "alice"} {
		if got := e.patch(who, design.ChannelID, purpose, http.StatusOK); got.Purpose != "pixels" {
			t.Fatalf("patch by %s = %+v", who, got)
		}
	}
	if got := e.pub.count(wire.EventChannelUpdated) - before; got != 3 {
		t.Fatalf("channel_updated published %d times for 3 changes", got)
	}
	// Bounds, in characters.
	e.patch("bob", design.ChannelID, map[string]any{"purpose": strings.Repeat("é", wire.MaxChannelPurpose)}, http.StatusOK)
	e.patch("bob", design.ChannelID, map[string]any{"purpose": strings.Repeat("é", wire.MaxChannelPurpose+1)}, http.StatusBadRequest)
	e.patch("bob", design.ChannelID, map[string]any{"rules": strings.Repeat("r", wire.MaxChannelRules+1)}, http.StatusBadRequest)
	// Settings values.
	for _, bad := range []map[string]any{
		{"whoPosts": "owners"},
		{"agentParticipation": "always"},
		{"slowModeSeconds": -1},
		{"defaultAgent": map[string]any{"provider": "codex"}},
	} {
		e.patch("bob", design.ChannelID, map[string]any{"settings": bad}, http.StatusBadRequest)
	}
	got := e.patch("bob", design.ChannelID, map[string]any{"settings": map[string]any{
		"whoPosts": "admins", "noAgents": true, "slowModeSeconds": 30, "agentParticipation": "questions",
		"defaultAgent": map[string]any{"ownerId": " jack ", "core": "nimbus", "provider": "codex"},
	}}, http.StatusOK)
	if s := got.Settings; s.WhoPosts != "admins" || !s.NoAgents || s.SlowModeSeconds != 30 || s.AgentParticipation != "questions" ||
		s.DefaultAgent == nil || *s.DefaultAgent != (wire.ChannelAgent{OwnerID: "jack", Core: "nimbus", Provider: "codex"}) {
		t.Fatalf("settings = %+v", got.Settings)
	}
	// The channel's owner moves; the new owner manages it, the old one no
	// longer does. A visitor owns nothing.
	e.patch("bob", design.ChannelID, map[string]any{"ownerId": "vic"}, http.StatusBadRequest)
	e.patch("bob", design.ChannelID, map[string]any{"ownerId": "carol"}, http.StatusOK)
	e.patch("bob", design.ChannelID, purpose, http.StatusForbidden)
	e.patch("carol", design.ChannelID, purpose, http.StatusOK)

	// The default channel: no rename, never bars agents, other settings ok.
	e.patch("alice", e.def, map[string]any{"name": "lobby"}, http.StatusConflict)
	e.patch("alice", e.def, map[string]any{"settings": map[string]any{"noAgents": true}}, http.StatusConflict)
	e.patch("alice", e.def, map[string]any{"purpose": "everything else", "settings": map[string]any{"slowModeSeconds": 5}}, http.StatusOK)
	// The people chat is not configurable.
	var people wire.RoomChannel
	e.call(http.MethodPost, "/v1/rooms/room-1/people-chat", "alice", nil, http.StatusOK, &people)
	e.patch("alice", people.ChannelID, purpose, http.StatusConflict)
}

// Archive and restore: by the channel's managers only, never the default
// channel or the people chat; an archived channel leaves the default
// listing and is listed with ?archived=1.
func TestChannelArchiveRestore(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design"}, http.StatusOK)
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+design.ChannelID+"/archive", "carol", nil, http.StatusForbidden, nil)
	var archived wire.RoomChannel
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+design.ChannelID+"/archive", "bob", nil, http.StatusOK, &archived)
	if archived.ArchivedAtMs == 0 {
		t.Fatalf("archived channel = %+v", archived)
	}
	if _, listed := byChannel(e.list("carol", "").Channels)[design.ChannelID]; listed {
		t.Fatal("an archived channel is in the default listing")
	}
	if got := e.list("carol", "?archived=1").Channels; len(got) != 1 || got[0].ChannelID != design.ChannelID {
		t.Fatalf("?archived=1 = %+v", got)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+design.ChannelID+"/restore", "vic", nil, http.StatusForbidden, nil)
	var restored wire.RoomChannel
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+design.ChannelID+"/restore", "erin", nil, http.StatusOK, &restored)
	if restored.ArchivedAtMs != 0 {
		t.Fatalf("restored channel = %+v", restored)
	}
	if _, listed := byChannel(e.list("carol", "").Channels)[design.ChannelID]; !listed {
		t.Fatal("a restored channel is missing from the listing")
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+e.def+"/archive", "alice", nil, http.StatusConflict, nil)
	var people wire.RoomChannel
	e.call(http.MethodPost, "/v1/rooms/room-1/people-chat", "alice", nil, http.StatusOK, &people)
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+people.ChannelID+"/archive", "alice", nil, http.StatusConflict, nil)
}

// The bridge's listing (?agents=1) leaves out a channel that bars agents
// (and the people chat) and keeps an archived one, whose history agents
// still read; people see every channel in the default listing.
func TestChannelListForAgents(t *testing.T) {
	e := newSetupEnv(t)
	quiet := e.create("bob", wire.ChannelCreateRequest{Name: "quiet"}, http.StatusOK)
	old := e.create("bob", wire.ChannelCreateRequest{Name: "old"}, http.StatusOK)
	e.patch("bob", quiet.ChannelID, map[string]any{"settings": map[string]any{"noAgents": true}}, http.StatusOK)
	e.call(http.MethodPost, "/v1/rooms/room-1/channels/"+old.ChannelID+"/archive", "bob", nil, http.StatusOK, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/people-chat", "alice", nil, http.StatusOK, nil)

	agents := byChannel(e.list("alice", "?"+wire.ChannelListForAgents+"=1").Channels)
	if _, ok := agents[quiet.ChannelID]; ok {
		t.Fatal("the agents' listing names a channel that bars agents")
	}
	if _, ok := agents[old.ChannelID]; !ok {
		t.Fatal("the agents' listing lost an archived channel")
	}
	if _, ok := agents[e.def]; !ok || len(agents) != 2 {
		t.Fatalf("the agents' listing = %v, want the default and the archived channel", agents)
	}
	people := byChannel(e.list("alice", "").Channels)
	if _, ok := people[quiet.ChannelID]; !ok {
		t.Fatal("people lost a channel that bars agents")
	}
	// The toggle back admits it again.
	e.patch("bob", quiet.ChannelID, map[string]any{"settings": map[string]any{}}, http.StatusOK)
	if _, ok := byChannel(e.list("alice", "?agents=1").Channels)[quiet.ChannelID]; !ok {
		t.Fatal("a channel that admits agents again is missing from the agents' listing")
	}
}

// Sections and the room's channel creation policy (C15): who makes a
// section, who places a channel where, who manages a section, and a
// section's delete moving its channels to the top level.
func TestSectionsAndCreationPolicy(t *testing.T) {
	e := newSetupEnv(t)
	var sec wire.RoomSection
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "carol", map[string]any{"name": "Design", "visibility": "members"}, http.StatusBadRequest, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "vic", map[string]any{"name": "Design"}, http.StatusForbidden, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "carol", map[string]any{"name": "Design"}, http.StatusOK, &sec)
	if sec.Visibility != wire.SectionOpen || len(sec.Owners) != 1 || sec.Owners[0] != "carol" {
		t.Fatalf("created section = %+v", sec)
	}
	// Anyone: a section's owner places channels in it, another member only
	// at the top level; the room's admins anywhere.
	inSec := e.create("carol", wire.ChannelCreateRequest{Name: "mocks", SectionID: sec.SectionID}, http.StatusOK)
	if inSec.SectionID != sec.SectionID {
		t.Fatalf("channel in section = %+v", inSec)
	}
	e.create("bob", wire.ChannelCreateRequest{Name: "mine", SectionID: sec.SectionID}, http.StatusForbidden)
	e.create("bob", wire.ChannelCreateRequest{Name: "mine", SectionID: "sec_unknown"}, http.StatusBadRequest)
	top := e.create("bob", wire.ChannelCreateRequest{Name: "mine"}, http.StatusOK)
	e.create("erin", wire.ChannelCreateRequest{Name: "admin-made", SectionID: sec.SectionID}, http.StatusOK)
	e.create("vic", wire.ChannelCreateRequest{Name: "nope"}, http.StatusForbidden)
	// Bob may not move his channel into carol's section; carol may move hers out.
	e.patch("bob", top.ChannelID, map[string]any{"sectionId": sec.SectionID}, http.StatusForbidden)
	e.patch("carol", inSec.ChannelID, map[string]any{"sectionId": ""}, http.StatusOK)
	e.patch("carol", inSec.ChannelID, map[string]any{"sectionId": sec.SectionID}, http.StatusOK)

	// "sections": the top level is the owner's and admins'.
	e.call(http.MethodPatch, "/v1/rooms/room-1", "bob", map[string]any{"channelCreation": "sections"}, http.StatusForbidden, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{"channelCreation": "everyone"}, http.StatusBadRequest, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{"channelCreation": "sections"}, http.StatusOK, nil)
	e.create("bob", wire.ChannelCreateRequest{Name: "top"}, http.StatusForbidden)
	var bobs wire.RoomSection
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "bob", map[string]any{"name": "Bob's"}, http.StatusOK, &bobs)
	e.create("bob", wire.ChannelCreateRequest{Name: "inside", SectionID: bobs.SectionID}, http.StatusOK)
	e.create("erin", wire.ChannelCreateRequest{Name: "top-by-admin"}, http.StatusOK)

	// "admins": members make neither sections nor channels.
	e.call(http.MethodPatch, "/v1/rooms/room-1", "alice", map[string]any{"channelCreation": "admins"}, http.StatusOK, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/sections", "bob", map[string]any{"name": "More"}, http.StatusForbidden, nil)
	e.create("bob", wire.ChannelCreateRequest{Name: "inside-2", SectionID: bobs.SectionID}, http.StatusForbidden)
	e.create("alice", wire.ChannelCreateRequest{Name: "owner-made"}, http.StatusOK)

	// A section's owners manage it and its channels; owners are members who post.
	e.call(http.MethodPatch, "/v1/rooms/room-1/sections/"+sec.SectionID, "bob", map[string]any{"name": "x"}, http.StatusForbidden, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1/sections/"+sec.SectionID, "carol", map[string]any{"owners": []string{"carol", "vic"}}, http.StatusBadRequest, nil)
	var patched wire.RoomSection
	e.call(http.MethodPatch, "/v1/rooms/room-1/sections/"+sec.SectionID, "carol", map[string]any{"owners": []string{"carol", "bob", "bob"}, "rules": " be brief "}, http.StatusOK, &patched)
	if len(patched.Owners) != 2 || patched.Rules != "be brief" {
		t.Fatalf("patched section = %+v", patched)
	}
	// Bob owns the section now: he manages carol's channel in it.
	e.patch("bob", inSec.ChannelID, map[string]any{"purpose": "mockups"}, http.StatusOK)
	// Delete: the channels move to the top level.
	e.call(http.MethodDelete, "/v1/rooms/room-1/sections/"+sec.SectionID, "vic", nil, http.StatusForbidden, nil)
	e.call(http.MethodDelete, "/v1/rooms/room-1/sections/"+sec.SectionID, "carol", nil, http.StatusOK, nil)
	list := e.list("alice", "")
	if got := byChannel(list.Channels)[inSec.ChannelID]; got.SectionID != "" {
		t.Fatalf("a deleted section's channel = %+v, want the top level", got)
	}
	for _, s := range list.Sections {
		if s.SectionID == sec.SectionID {
			t.Fatal("a deleted section is still listed")
		}
	}
}

// Following is the caller's own: join, star, hide, leave; the directory
// lists every open channel with its followers.
func TestFollowAndDirectory(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design", Purpose: "pixels"}, http.StatusOK)
	path := "/v1/rooms/room-1/channels/" + design.ChannelID + "/follow"
	var got wire.RoomChannel
	e.call(http.MethodPut, path, "carol", nil, http.StatusOK, &got)
	if !got.Joined || got.Starred || got.Hidden {
		t.Fatalf("after follow = %+v", got)
	}
	e.call(http.MethodPut, path, "carol", map[string]any{"starred": true}, http.StatusOK, &got)
	if !got.Joined || !got.Starred {
		t.Fatalf("after star = %+v", got)
	}
	e.call(http.MethodPut, path, "vic", map[string]any{"hidden": true}, http.StatusOK, &got)
	if !got.Joined || !got.Hidden || got.Starred {
		t.Fatalf("a visitor's own marks = %+v", got)
	}
	// One member's marks are not another's.
	if c := byChannel(e.list("erin", "").Channels)[design.ChannelID]; c.Joined || c.Starred {
		t.Fatalf("erin sees carol's marks: %+v", c)
	}
	var dir wire.ChannelDirectory
	e.call(http.MethodGet, "/v1/rooms/room-1/channel-directory", "erin", nil, http.StatusOK, &dir)
	found := false
	for _, c := range dir.Channels {
		if c.ChannelID == design.ChannelID {
			found = true
			// bob (the creator), carol, vic.
			if c.Followers != 3 || c.Purpose != "pixels" || c.OwnerID != "bob" || c.Joined {
				t.Fatalf("directory entry = %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("the directory lacks an open channel")
	}
	e.call(http.MethodDelete, path, "carol", nil, http.StatusOK, &got)
	if got.Joined || got.Starred {
		t.Fatalf("after leaving = %+v", got)
	}
	e.call(http.MethodPut, path, "mallory", nil, http.StatusForbidden, nil)

	// Every member is in the default channel, followed or not (QA
	// 2026-10-28: a room's default channel left the rail as soon as another
	// channel was opened), and it cannot be left.
	if c := byChannel(e.list("erin", "").Channels)[e.def]; !c.Joined {
		t.Fatalf("the default channel for a member with no follow = %+v", c)
	}
	e.call(http.MethodDelete, "/v1/rooms/room-1/channels/"+e.def+"/follow", "carol", nil, http.StatusConflict, nil)
}

// The room's rules, recommended channels and creation policy are the
// owner's or an admin's; the name stays the owner's. A new member joins the
// recommended channels; agreeing takes the current version only.
func TestRoomRulesAndRecommendedChannels(t *testing.T) {
	e := newSetupEnv(t)
	design := e.create("bob", wire.ChannelCreateRequest{Name: "design"}, http.StatusOK)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "bob", map[string]any{"rules": "Be kind."}, http.StatusForbidden, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{"name": "Renamed"}, http.StatusForbidden, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{"rules": strings.Repeat("r", wire.MaxRoomRules+1)}, http.StatusBadRequest, nil)
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{"recommendedChannels": []string{"ch_unknown"}}, http.StatusBadRequest, nil)
	var detail wire.RoomDetail
	e.call(http.MethodPatch, "/v1/rooms/room-1", "erin", map[string]any{
		"rules": "Be kind.", "requireRulesAgreement": true, "recommendedChannels": []string{design.ChannelID, design.ChannelID, e.def},
	}, http.StatusOK, &detail)
	if detail.Rules != "Be kind." || detail.RulesVersion != 1 || !detail.RequireRulesAgreement ||
		len(detail.RecommendedChannels) != 2 || detail.ChannelCreation != wire.ChannelCreationAnyone {
		t.Fatalf("room detail = %+v", detail)
	}
	// A new member (added, or by invite) joins the recommended channels.
	e.call(http.MethodPost, "/v1/rooms/room-1/members", "alice", map[string]string{"userId": "dave", "role": "member"}, http.StatusOK, nil)
	joined := byChannel(e.list("dave", "").Channels)
	if !joined[design.ChannelID].Joined || !joined[e.def].Joined {
		t.Fatalf("a new member's channels = %+v, want both recommended joined", joined)
	}
	// Agreeing: the current version only.
	var rules wire.RoomRulesState
	e.call(http.MethodGet, "/v1/rooms/room-1/rules", "dave", nil, http.StatusOK, &rules)
	if rules.Version != 1 || !rules.Required || rules.AgreedVersion != 0 || rules.Rules != "Be kind." {
		t.Fatalf("rules = %+v", rules)
	}
	e.call(http.MethodPatch, "/v1/rooms/room-1", "alice", map[string]any{"rules": "Be kinder."}, http.StatusOK, nil)
	body := e.call(http.MethodPost, "/v1/rooms/room-1/rules/agree", "dave", map[string]int{"version": 1}, http.StatusConflict, nil)
	if !strings.Contains(body, wire.RulesChanged) {
		t.Fatalf("a stale agreement = %s, want code %s", body, wire.RulesChanged)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/rules/agree", "dave", map[string]int{"version": 0}, http.StatusBadRequest, nil)
	e.call(http.MethodPost, "/v1/rooms/room-1/rules/agree", "dave", map[string]int{"version": 2}, http.StatusOK, &rules)
	if rules.AgreedVersion != 2 {
		t.Fatalf("after agreeing = %+v", rules)
	}
	e.call(http.MethodPost, "/v1/rooms/room-1/rules/agree", "mallory", map[string]int{"version": 2}, http.StatusForbidden, nil)
}
