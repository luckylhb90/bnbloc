package gateway

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// End to end through the gateway: a member makes a webhook task and its
// URL; an outside system's signed request to the open route
// POST /v1/hooks/{id} (no session) sets the task's run, an unsigned one is
// refused and counted; a member's own event (POST /v1/rooms/{id}/events)
// sets a task taking members' events, and a platform type is refused.
func TestHooksAndMemberEventsThroughTheGateway(t *testing.T) {
	e := newAppGW(t)
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	box, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e.rt.HookBox = box
	maya := "Authorization: Bearer " + e.session("maya")
	create := func(source string, types ...string) wire.RoomSchedule {
		t.Helper()
		r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/schedules", map[string]any{
			"text": "Run the tests.", "recipient": map[string]any{"kind": "room"}, "timezone": "UTC",
			"recurrence": map[string]any{"kind": "event", "source": source, "types": types},
		}, maya)
		var sc wire.RoomSchedule
		if r.status != http.StatusCreated || json.Unmarshal(r.body, &sc) != nil {
			t.Fatalf("create = %d %s", r.status, r.body)
		}
		return sc
	}
	sc := create(wire.SubscribeWebhook, "com.github.push")
	r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/schedules/"+sc.ScheduleID+"/hook", nil, maya)
	var made wire.ScheduleHookSecret
	if r.status != http.StatusCreated || json.Unmarshal(r.body, &made) != nil || made.Secret == "" {
		t.Fatalf("make hook = %d %s", r.status, r.body)
	}
	body := `{"ref":"refs/heads/main"}`
	path := wire.HookPathPrefix + made.Hook.HookID
	if r := e.call(http.MethodPost, path, body, wire.GitHubEventHeader+": push"); r.status != http.StatusUnauthorized {
		t.Fatalf("unsigned = %d %s", r.status, r.body)
	}
	r = e.call(http.MethodPost, path, body, wire.GitHubEventHeader+": push", wire.GitHubSignatureHeader+": "+schedule.SignHookBody(made.Secret, []byte(body)),
		"Cookie: session=whatever")
	if r.status != http.StatusAccepted || !strings.Contains(string(r.body), `"triggered":1`) {
		t.Fatalf("signed = %d %s", r.status, r.body)
	}
	got, err := e.b.Schedules.GetSchedule(e.ctx(), e.roomID, sc.ScheduleID)
	if err != nil || got.Event == nil || got.Event.Type != "com.github.push" || got.Hook == nil || got.Hook.Refused != 1 {
		t.Fatalf("the task = %+v (%v)", got, err)
	}
	if r := e.call(http.MethodPost, path, strings.Repeat(" ", schedule.MaxHookBodyBytes+10), wire.HookEventHeader+": x"); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body over the bound = %d", r.status)
	}

	member := create(wire.SubscribeMember, "dev.finished")
	r = e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/events", map[string]any{"type": "dev.finished", "subject": "v2"}, "Authorization: Bearer "+e.session("jack"))
	if r.status != http.StatusAccepted || !strings.Contains(string(r.body), `"triggered":1`) || !strings.Contains(string(r.body), `"source":"bnbloc/member/jack"`) {
		t.Fatalf("a member's event = %d %s", r.status, r.body)
	}
	if got, _ := e.b.Schedules.GetSchedule(e.ctx(), e.roomID, member.ScheduleID); got.Event == nil || got.Event.Subject != "v2" {
		t.Fatalf("the member task = %+v", got.Event)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/events", map[string]any{"type": "platform.deployed"}, maya); r.status != http.StatusBadRequest {
		t.Fatalf("a platform type = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/events", map[string]any{"type": "dev.finished"}, "Authorization: Bearer "+e.session("nora")); r.status != http.StatusForbidden {
		t.Fatalf("a non-member = %d %s", r.status, r.body)
	}
	if r := e.call(http.MethodPost, "/v1/rooms/"+e.roomID+"/events", map[string]any{"type": "dev.finished"}); r.status != http.StatusUnauthorized {
		t.Fatalf("no session = %d", r.status)
	}
}
