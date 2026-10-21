package realtimehttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// hook makes (or rotates) sc's webhook as user.
func (f *schedFixture) hook(user, id string) (int, wire.ScheduleHookSecret) {
	f.t.Helper()
	code, out := f.do(user, http.MethodPost, "/v1/rooms/room-1/schedules/"+id+"/hook", nil)
	var s wire.ScheduleHookSecret
	_ = json.Unmarshal(out, &s)
	return code, s
}

// post sends body to the webhook as the gateway forwards it, with headers.
func (f *schedFixture) post(hookID string, body []byte, headers map[string]string) (int, string) {
	f.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/internal/v1/hooks/"+hookID, bytes.NewReader(body))
	req.Header.Set(svcauth.HeaderUser, svcauth.ServiceSubject("gateway"))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func webhookTask(f *schedFixture, filter map[string]any) wire.RoomSchedule {
	body := eventBody("Run the tests on what was pushed.", map[string]any{"kind": "room"}, "webhook", "com.github.push", "ci.*")
	if filter != nil {
		body["recurrence"].(map[string]any)["filter"] = filter
	}
	return f.create("alice", body)
}

// A webhook task's creator makes its URL and gets the secret once; the
// list shows the hook, never the secret. Only the creator makes or rotates
// it, and only a task whose trigger is a webhook has one.
func TestHookMakeShownOnce(t *testing.T) {
	f := newSchedFixture(t)
	sc := webhookTask(f, nil)
	if code, _ := f.hook("bob", sc.ScheduleID); code != http.StatusForbidden {
		t.Fatalf("another member making the hook = %d", code)
	}
	clock := f.create("alice", dailyBody("standup", map[string]any{"kind": "room"}))
	if code, _ := f.hook("alice", clock.ScheduleID); code != http.StatusConflict {
		t.Fatalf("a clock task's hook = %d", code)
	}
	code, made := f.hook("alice", sc.ScheduleID)
	if code != http.StatusCreated || !strings.HasPrefix(made.Hook.HookID, "hk_") || !strings.HasPrefix(made.Secret, "whsec_") || len(made.Secret) < 40 {
		t.Fatalf("make = %d %+v", code, made)
	}
	_, out := f.do("bob", http.MethodGet, "/v1/rooms/room-1/schedules", nil)
	if !strings.Contains(string(out), made.Hook.HookID) || strings.Contains(string(out), made.Secret) || strings.Contains(string(out), "whsec_") {
		t.Fatalf("the list shows %s", out)
	}
	off := newSchedFixture(t)
	off.svc.HookBox = nil
	if code, _ := off.hook("alice", webhookTask(off, nil).ScheduleID); code != http.StatusServiceUnavailable {
		t.Fatalf("a hook without the secret store key = %d", code)
	}
}

// A signed request (ours, or GitHub's header for the same scheme) sets the
// task's run with the event it carries; unsigned, wrongly signed, too
// large, not JSON or typeless requests are refused and counted; a type or
// filter the trigger does not take sets nothing.
func TestHookRequests(t *testing.T) {
	f := newSchedFixture(t)
	sc := webhookTask(f, map[string]any{"data": map[string]any{"ref": "refs/heads/main"}})
	_, made := f.hook("alice", sc.ScheduleID)
	id, secret := made.Hook.HookID, made.Secret
	push := []byte(`{"ref":"refs/heads/main","after":"abc123","commits":[{"id":"abc123"}]}`)

	refusals := []struct {
		name    string
		body    []byte
		headers map[string]string
		want    int
	}{
		{"unsigned", push, map[string]string{wire.GitHubEventHeader: "push"}, http.StatusUnauthorized},
		{"wrong secret", push, map[string]string{wire.GitHubEventHeader: "push", wire.GitHubSignatureHeader: schedule.SignHookBody("whsec_other", push)}, http.StatusUnauthorized},
		{"another body", push, map[string]string{wire.GitHubEventHeader: "push", wire.HookSignatureHeader: schedule.SignHookBody(secret, []byte(`{}`))}, http.StatusUnauthorized},
		{"not json", []byte("ref=main"), map[string]string{wire.HookEventHeader: "ci.done", wire.HookSignatureHeader: schedule.SignHookBody(secret, []byte("ref=main"))}, http.StatusBadRequest},
		{"no type", []byte(`{"ref":"x"}`), map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(secret, []byte(`{"ref":"x"}`))}, http.StatusBadRequest},
		{"too large", bytes.Repeat([]byte(" "), schedule.MaxHookBodyBytes+1), map[string]string{wire.HookEventHeader: "ci.done"}, http.StatusRequestEntityTooLarge},
	}
	for _, c := range refusals {
		if code, out := f.post(id, c.body, c.headers); code != c.want {
			t.Errorf("%s = %d %s, want %d", c.name, code, out, c.want)
		}
	}
	if got := f.get(sc.ScheduleID); got.Hook == nil || got.Hook.Refused != int64(len(refusals)) || got.Hook.LastRefusedAtMs == 0 || got.NextRunAtMs != 0 {
		t.Fatalf("after the refusals: %+v (hook %+v)", got, got.Hook)
	}
	if code, _ := f.post("hk_nobody", push, nil); code != http.StatusNotFound {
		t.Fatalf("an unknown hook = %d", code)
	}

	// The filter refuses another branch; a type the trigger does not take.
	other := []byte(`{"ref":"refs/heads/dev"}`)
	if code, out := f.post(id, other, map[string]string{wire.GitHubEventHeader: "push", wire.GitHubSignatureHeader: schedule.SignHookBody(secret, other)}); code != http.StatusAccepted || out != "{\"triggered\":0}\n" {
		t.Fatalf("a filtered push = %d %s", code, out)
	}
	if code, out := f.post(id, push, map[string]string{wire.GitHubEventHeader: "issues", wire.GitHubSignatureHeader: schedule.SignHookBody(secret, push)}); code != http.StatusAccepted || !strings.Contains(out, `"triggered":0`) {
		t.Fatalf("a type it does not take = %d %s", code, out)
	}
	if f.get(sc.ScheduleID).NextRunAtMs != 0 {
		t.Fatal("a refused or untaken request set the run")
	}
	// GitHub's scheme.
	if code, out := f.post(id, push, map[string]string{wire.GitHubEventHeader: "push", wire.GitHubDeliveryHeader: "d-1", wire.GitHubSignatureHeader: schedule.SignHookBody(secret, push)}); code != http.StatusAccepted || !strings.Contains(out, `"triggered":1`) {
		t.Fatalf("a GitHub push = %d %s", code, out)
	}
	ev := f.get(sc.ScheduleID).Event
	if ev == nil || ev.Type != "com.github.push" || ev.ID != "d-1" || ev.Source != wire.SourceWebhookPrefix+id || !strings.Contains(string(ev.Data), `"after":"abc123"`) {
		t.Fatalf("event = %+v", ev)
	}
	// Ours, the type in the body.
	ci := []byte(`{"type":"ci.finished","ref":"refs/heads/main","status":"passed"}`)
	if code, out := f.post(id, ci, map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(secret, ci)}); code != http.StatusAccepted || !strings.Contains(out, `"triggered":1`) {
		t.Fatalf("our scheme = %d %s", code, out)
	}
	if ev := f.get(sc.ScheduleID).Event; ev.Type != "ci.finished" {
		t.Fatalf("event = %+v", ev)
	}
}

// Rotating keeps the URL and refuses the old secret; revoking (the
// creator, or the room's owner) makes the URL answer 404.
func TestHookRotateRevoke(t *testing.T) {
	f := newSchedFixture(t)
	sc := webhookTask(f, nil)
	_, first := f.hook("alice", sc.ScheduleID)
	code, second := f.hook("alice", sc.ScheduleID)
	if code != http.StatusCreated || second.Hook.HookID != first.Hook.HookID || second.Secret == first.Secret {
		t.Fatalf("rotate = %d %+v (was %+v)", code, second, first)
	}
	body := []byte(`{"type":"ci.done"}`)
	if code, _ := f.post(first.Hook.HookID, body, map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(first.Secret, body)}); code != http.StatusUnauthorized {
		t.Fatalf("the rotated-out secret = %d", code)
	}
	if code, _ := f.post(first.Hook.HookID, body, map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(second.Secret, body)}); code != http.StatusAccepted {
		t.Fatalf("the new secret = %d", code)
	}
	if code, _ := f.do("bob", http.MethodDelete, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/hook", nil); code != http.StatusForbidden {
		t.Fatalf("a member revoking another's hook = %d", code)
	}
	if code, _ := f.do("olga", http.MethodDelete, "/v1/rooms/room-1/schedules/"+sc.ScheduleID+"/hook", nil); code != http.StatusNoContent {
		t.Fatalf("the owner revoking = %d", code)
	}
	if code, _ := f.post(first.Hook.HookID, body, map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(second.Secret, body)}); code != http.StatusNotFound {
		t.Fatalf("a revoked hook = %d", code)
	}
	if got := f.get(sc.ScheduleID); got.Hook != nil {
		t.Fatalf("revoked, the task still has %+v", got.Hook)
	}
	_, again := f.hook("alice", sc.ScheduleID)
	if again.Hook.HookID == first.Hook.HookID {
		t.Fatal("a hook made after a revoke reuses the revoked URL")
	}
}

// A hook's requests spend its task's rate limit: past it they are dropped
// (429) and counted on the task.
func TestHookRateLimit(t *testing.T) {
	f := newSchedFixture(t)
	sc := webhookTask(f, nil)
	_, made := f.hook("alice", sc.ScheduleID)
	body := []byte(`{"type":"ci.done"}`)
	headers := map[string]string{wire.HookSignatureHeader: schedule.SignHookBody(made.Secret, body)}
	limited := 0
	for i := 0; i < int(taskEventBurst)+3; i++ {
		if code, _ := f.post(made.Hook.HookID, body, headers); code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 3 || f.get(sc.ScheduleID).DroppedEvents != 3 {
		t.Fatalf("limited %d, dropped %d; want 3", limited, f.get(sc.ScheduleID).DroppedEvents)
	}
}
