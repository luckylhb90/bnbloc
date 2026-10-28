package roomtools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// channelGateway is setupGateway with a roster of two agents of one provider
// under two cores: a handle names one of them, or none.
func channelGateway() *fakeGateway {
	base := setupGateway("")
	inner := base.handle
	base.handle = func(method, path string, body []byte) (int, any) {
		if p, _, _ := strings.Cut(path, "?"); method == "GET" && p == "/v1/rooms/room-1/agents" {
			return 200, map[string]any{"agents": []wire.RosterAgent{{OwnerUserID: "jack", Core: "acme", Provider: "claude"}, {OwnerUserID: "jack", Core: "nimbus", Provider: "claude"}}}
		}
		return inner(method, path, body)
	}
	return base
}

// channel makes a channel (by name, then the rest of its setup) or changes
// one by id, on the routes the channel's menu uses, through the door: the
// settings document is the channel's own with the given fields changed (a
// PATCH replaces it whole), the default agent is the handle's key (owner,
// core, provider), archive and restore are their own routes, and each change
// is a line in the room. set takes the room's rules and recommended
// channels, the room's PATCH with nothing else in it.
func TestRoomSetupChannel(t *testing.T) {
	gw := channelGateway()
	env := setupEnv(t, gw, true)
	for _, args := range []string{
		`{"action":"channel","channel":{"name":"releases","purpose":"Ship notes","sectionId":"sec_1","whoPosts":"admins"}}`,
		`{"action":"channel","id":"ch_rep","channel":{"defaultAgent":"jack/nimbus/claude","noAgents":false,"slowModeSeconds":0,"archived":true}}`,
		`{"action":"channel","id":"ch_old","channel":{"archived":false,"purpose":"Back again"}}`,
		`{"action":"set","rules":"Be kind.","recommendedChannels":["ch_gen","ch_rep"]}`,
	} {
		if r := runSetup(env, args); r.Err != "" {
			t.Fatalf("%s: %s", args, r.Err)
		}
	}
	var got []string
	bodies := map[string]string{}
	for _, c := range gw.calls {
		if c.Method != "GET" && !strings.HasSuffix(c.Path, "/agent-activity/messages") {
			got = append(got, c.Method+" "+c.Path)
			bodies[c.Method+" "+c.Path] += string(c.Body)
		}
	}
	want := []string{
		"POST /v1/rooms/room-1/channels?job=job-1",
		"PATCH /v1/rooms/room-1/channels/ch_9?job=job-1",
		"PATCH /v1/rooms/room-1/channels/ch_rep?job=job-1",
		"POST /v1/rooms/room-1/channels/ch_rep/archive?job=job-1",
		"POST /v1/rooms/room-1/channels/ch_old/restore?job=job-1",
		"PATCH /v1/rooms/room-1/channels/ch_old?job=job-1",
		"PATCH /v1/rooms/room-1?job=job-1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("changes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if b := bodies["POST /v1/rooms/room-1/channels?job=job-1"]; b != `{"name":"releases","purpose":"Ship notes","sectionId":"sec_1"}` {
		t.Fatalf("create = %s", b)
	}
	if b := bodies["PATCH /v1/rooms/room-1/channels/ch_9?job=job-1"]; b != `{"settings":{"whoPosts":"admins"}}` {
		t.Fatalf("the new channel's setup = %s", b)
	}
	// The channel's own settings kept (whoPosts), slow mode turned off, the
	// default agent by its key: nimbus's, not the first claude.
	var patch wire.ChannelPatchRequest
	if err := json.Unmarshal([]byte(bodies["PATCH /v1/rooms/room-1/channels/ch_rep?job=job-1"]), &patch); err != nil || patch.Settings == nil ||
		patch.Settings.WhoPosts != "admins" || patch.Settings.SlowModeSeconds != 0 || patch.Name != nil || patch.Settings.DefaultAgent == nil ||
		*patch.Settings.DefaultAgent != (wire.ChannelAgent{OwnerID: "jack", Core: "nimbus", Provider: "claude"}) {
		t.Fatalf("patch = %+v (%v)", patch, err)
	}
	if b := bodies["PATCH /v1/rooms/room-1?job=job-1"]; b != `{"rules":"Be kind.","recommendedChannels":["ch_gen","ch_rep"]}` {
		t.Fatalf("room patch = %s", b)
	}
	wantLines := []string{
		"maya's agent added the channel #digest",
		"maya's agent changed the channel #digest: who posts",
		"maya's agent changed the channel #renamed: default agent, agents admitted, slow mode",
		"maya's agent archived the channel #renamed",
		"maya's agent restored the channel #old",
		"maya's agent changed the channel #renamed: purpose",
		"maya's agent set the room's rules and recommended channels",
	}
	if got := lines(gw); strings.Join(got, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("room lines:\n%s", strings.Join(got, "\n"))
	}
}

// A refused change comes back in the room service's words and the room
// shows nothing; a handle two agents answer names none (nothing is
// guessed); arguments are checked before anything changes.
func TestRoomSetupChannelRefusals(t *testing.T) {
	gw := setupGateway("PATCH /v1/rooms/room-1/channels/")
	if r := runSetup(setupEnv(t, gw, true), `{"action":"channel","id":"ch_rep","channel":{"purpose":"x"}}`); !strings.Contains(r.Err, "only the room's owner or admins") || len(lines(gw)) != 0 {
		t.Fatalf("refused = %q, lines %v", r.Err, lines(gw))
	}
	gw = channelGateway()
	for name, args := range map[string]string{
		"ambiguous handle": `{"action":"channel","id":"ch_rep","channel":{"defaultAgent":"jack/claude"}}`,
		"new, bad agent":   `{"action":"channel","channel":{"name":"x","defaultAgent":"olaf/codex"}}`,
	} {
		if r := runSetup(setupEnv(t, gw, true), args); !strings.Contains(r.Err, "not one of the room's agents") {
			t.Errorf("%s = %q", name, r.Err)
		}
	}
	for _, c := range gw.calls {
		if c.Method != "GET" {
			t.Fatalf("a refused call changed something: %s %s", c.Method, c.Path)
		}
	}
}
