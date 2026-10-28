package gateway

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// room_setup's channel action and set's rules against the real gateway and
// room service (docs/prd/2026-10-28-channels-and-threads.md §2.6): the
// agent door admits a channel's patch, archive and restore and the room's
// rules and recommended channels, as the person who asked; the room
// service decides who may (the channel's owner, the room's owner and
// admins); each change is audited and is a line in the room. Deleting a
// channel, its owner, the agreement switch and the creation policy are
// never an agent's act.
func TestRoomSetupChannelThroughTheDoor(t *testing.T) {
	e := galleryGW(t)
	audit := store.NewMemoryExtensionAudit()
	e.gw.ExtAudit = audit
	activity := newRecorder(t)
	e.gw.Backends.Activity = activity.srv.URL
	e.rt.Roster.Advertise(e.roomID, "jack", []realtime.AgentRef{{Core: "acme", Provider: "claude"}})
	e.setupJob("job-maya", e.roomID, "maya")
	run := func(args map[string]any) map[string]any {
		t.Helper()
		r := e.toolFor(e.roomID, "job-maya", "maya", "room_setup", args)
		if r.Err != "" {
			t.Fatalf("%v: %s", args, r.Err)
		}
		return r.Value.(map[string]any)
	}
	made := run(map[string]any{"action": "channel", "channel": map[string]any{"name": "releases", "purpose": "Ship notes", "whoPosts": "admins"}})
	id := made["id"].(string)
	run(map[string]any{"action": "channel", "id": id, "channel": map[string]any{"rules": "Notes only.", "defaultAgent": "jack/claude", "slowModeSeconds": 60, "archived": true}})
	run(map[string]any{"action": "set", "rules": "Be kind.", "recommendedChannels": []string{}})

	ch, ok, err := e.b.Channel.GetChannel(e.ctx(), e.roomID, id)
	if err != nil || !ok || ch.Purpose != "Ship notes" || ch.Rules != "Notes only." || ch.OwnerID != "maya" || !ch.Archived() ||
		ch.Settings.WhoPosts != wire.ChannelPostersAdmins || ch.Settings.SlowModeSeconds != 60 ||
		ch.Settings.DefaultAgent == nil || *ch.Settings.DefaultAgent != (wire.ChannelAgent{OwnerID: "jack", Core: "acme", Provider: "claude"}) {
		t.Fatalf("the channel = %+v, %v, %v", ch, ok, err)
	}
	room, _ := e.b.Room.GetRoom(e.ctx(), e.roomID)
	if room.Rules != "Be kind." || room.RulesVersion != 1 {
		t.Fatalf("the room's rules = %q v%d", room.Rules, room.RulesVersion)
	}
	run(map[string]any{"action": "channel", "id": id, "channel": map[string]any{"archived": false}})
	if ch, _, _ := e.b.Channel.GetChannel(e.ctx(), e.roomID, id); ch.Archived() {
		t.Fatal("the channel was not restored")
	}

	recs, _ := audit.AuditLog(context.Background(), store.AuditLogQuery{AllTenants: true, Kinds: []string{wire.AuditKindRoomSetup}, Limit: 50})
	var actions []string
	for _, r := range recs {
		if r.Actor != "maya" || r.Outcome != wire.AuditOutcomeAllow || strings.Contains(r.Detail, "Notes only") {
			t.Errorf("audit record %+v", r)
		}
		actions = append(actions, r.Action)
	}
	slices.Sort(actions)
	if want := []string{"add-channel", "archive-channel", "restore-channel", "set-channel", "set-channel", "set-rules"}; !slices.Equal(actions, want) {
		t.Fatalf("audited = %v, want %v", actions, want)
	}
	lines := roomLines(activity, wire.NoticeRoomSetup)
	if len(lines) != 6 || lines[0] != "maya's agent added the channel #releases" || lines[3] != "maya's agent archived the channel #releases" ||
		lines[4] != "maya's agent set the room's rules and recommended channels" {
		t.Fatalf("room lines = %q", lines)
	}

	// A plain member's agent: their own channel they may change, another's
	// not (the room service's words); the room's rules are the owner's or an
	// admin's.
	e.setupJob("job-jack", e.roomID, "jack")
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "channel", "id": id, "channel": map[string]any{"purpose": "mine"}}, "the channel's owner"},
		{map[string]any{"action": "set", "rules": "Mine."}, "only the owner may edit the room"},
	} {
		if r := e.toolFor(e.roomID, "job-jack", "jack", "room_setup", c.args); !strings.Contains(r.Err, c.want) {
			t.Errorf("%v = %q, want %q", c.args, r.Err, c.want)
		}
	}
	if r := e.toolFor(e.roomID, "job-jack", "jack", "room_setup", map[string]any{"action": "channel", "channel": map[string]any{"name": "jacks", "purpose": "His"}}); r.Err != "" {
		t.Fatalf("a member's own channel = %s", r.Err)
	}

	// The door itself, whoever asks: never a channel's deletion or owner,
	// never the agreement switch or the creation policy, never the
	// description with the rules.
	jack := "Authorization: Bearer " + e.session("jack")
	for _, c := range []struct {
		method, path string
		body         any
		status       int
		want         string
	}{
		{http.MethodDelete, "/v1/rooms/" + e.roomID + "/channels/" + id + "?job=job-maya", nil, http.StatusForbidden, ""},
		{http.MethodPatch, "/v1/rooms/" + e.roomID + "/channels/" + id + "?job=job-maya", map[string]any{"ownerId": "jack"}, http.StatusForbidden, "who owns a channel"},
		{http.MethodPatch, "/v1/rooms/" + e.roomID + "?job=job-maya", map[string]any{"requireRulesAgreement": true}, http.StatusForbidden, "agreement switch"},
		{http.MethodPatch, "/v1/rooms/" + e.roomID + "?job=job-maya", map[string]any{"channelCreation": "admins"}, http.StatusForbidden, "channel creation"},
		{http.MethodPatch, "/v1/rooms/" + e.roomID + "?job=job-maya", map[string]any{"description": "x", "rules": "y"}, http.StatusForbidden, "description, or its rules"},
	} {
		r := e.call(c.method, c.path, c.body, jack)
		if r.status == http.StatusOK || (c.status != 0 && r.status != c.status && r.status != http.StatusBadRequest) || !strings.Contains(string(r.body), c.want) {
			t.Errorf("%s %s = %d %s, want %d %q", c.method, c.path, r.status, r.body, c.status, c.want)
		}
	}
	// A section is no door route: a ?job= there is ignored and the caller
	// acts as themselves (jack's own section), never as the job's requester.
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/sections?job=job-maya", map[string]any{"name": "Ops"}, jack); r.status != http.StatusOK ||
		!strings.Contains(string(r.body), `"createdBy":"jack"`) {
		t.Fatalf("a section with a job's grant = %d %s", r.status, r.body)
	}
	if ch, ok, _ := e.b.Channel.GetChannel(e.ctx(), e.roomID, id); !ok || ch.OwnerID != "maya" {
		t.Fatalf("the channel after the refused door calls = %+v %v", ch, ok)
	}
	if room, _ := e.b.Room.GetRoom(e.ctx(), e.roomID); room.RequireRulesAgreement || room.ChannelCreationPolicy() != wire.ChannelCreationAnyone {
		t.Fatalf("the room after the refused door calls = %+v", room)
	}
}
