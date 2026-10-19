package realtimehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A job's live view end to end on realtime: the gateway's frame is held
// (newest only) and announced without its image, a member fetches the
// image, only the owner takes control, the controller's input reaches the
// tool once in its next post's reply, and a stranger sees nothing.
func TestJobLiveView(t *testing.T) {
	const key = "internal-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	mem.Add("room-1", "jack")
	hub := realtime.NewHub(16, 8)
	now := time.Unix(1_800_000_000, 0)
	svc := &Service{Hub: hub, Membership: mem, Now: func() time.Time { return now }}
	srv := httptest.NewServer(svcauth.Middleware(key, svc.Handler()))
	defer srv.Close()
	sub := hub.SubscribeLive("room-1")
	defer hub.Unsubscribe("room-1", sub)

	call := func(method, path, subject string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, err := svcauth.Key(key).NewRequest(context.Background(), method, srv.URL+path, rd, subject, "acme")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	gateway := svcauth.ServiceSubject("gateway")
	post := func(frame component.LiveFramePost) component.LiveFrameReply {
		t.Helper()
		code, out := call(http.MethodPost, "/internal/v1/rooms/room-1/live/job-1", gateway, wire.LiveFrameIngest{ChannelID: "ch-1", Owner: "maya", Requester: "maya", Frame: frame})
		if code != http.StatusOK {
			t.Fatalf("post frame = %d %s", code, out)
		}
		var reply component.LiveFrameReply
		_ = json.Unmarshal(out, &reply)
		return reply
	}
	// Only the gateway posts frames.
	if code, _ := call(http.MethodPost, "/internal/v1/rooms/room-1/live/job-1", "maya", wire.LiveFrameIngest{Owner: "maya"}); code != http.StatusForbidden {
		t.Fatalf("a member's frame post = %d, want 403", code)
	}

	reply := post(component.LiveFramePost{Run: "run-1", Seq: 1, MimeType: "image/jpeg", Data: []byte("jpeg-1"), Width: 800, Height: 600, Title: "Expenses"})
	if reply.Watching || reply.Controller != "" || len(reply.Input) != 0 {
		t.Fatalf("first reply = %+v", reply)
	}
	select {
	case ev := <-sub.Events():
		raw, _ := json.Marshal(ev)
		if ev.Kind != wire.EventJobLiveFrame || ev.ChannelID != "ch-1" || strings.Contains(string(raw), "anBlZy0x") || !strings.Contains(string(raw), `"owner":"maya"`) {
			t.Fatalf("event = %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no job_live_frame event")
	}

	// A member reads the newest image; watching it shows in the next reply.
	post(component.LiveFramePost{Run: "run-1", Seq: 2, MimeType: "image/jpeg", Data: []byte("jpeg-2"), Width: 800, Height: 600})
	code, img := call(http.MethodGet, "/v1/rooms/room-1/jobs/job-1/live/frame?watch=1", "jack", nil)
	if code != http.StatusOK || string(img) != "jpeg-2" {
		t.Fatalf("frame = %d %q", code, img)
	}
	if code, _ := call(http.MethodGet, "/v1/rooms/room-1/jobs/job-1/live/frame", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("a stranger's frame = %d, want 403", code)
	}
	if !post(component.LiveFramePost{}).Watching {
		t.Fatal("a watched view did not say so")
	}

	// Control: not jack's (another member's machine), maya's own.
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/control", "jack", wire.LiveControlPost{Take: true}); code != http.StatusForbidden {
		t.Fatalf("jack took control: %d", code)
	}
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputText, Text: "x"}}}); code != http.StatusConflict {
		t.Fatalf("input without control = %d, want 409", code)
	}
	if code, out := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/control", "maya", wire.LiveControlPost{Take: true}); code != http.StatusOK || !strings.Contains(string(out), `"controller":"maya"`) {
		t.Fatalf("maya take control = %d %s", code, out)
	}
	in := []component.LiveInput{
		{Type: component.LiveInputMouse, Action: "down", X: 0.5, Y: 0.25},
		{Type: component.LiveInputText, Text: "120.50"},
		{Type: component.LiveInputMouse, X: 2, Y: 0.5}, // outside the frame: dropped
		{Type: "script", Text: "alert(1)"},             // unknown: dropped
	}
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: in}); code != http.StatusNoContent {
		t.Fatalf("input = %d", code)
	}
	reply = post(component.LiveFramePost{})
	if reply.Controller != "maya" || len(reply.Input) != 2 || reply.Input[1].Text != "120.50" {
		t.Fatalf("reply with input = %+v", reply)
	}
	if again := post(component.LiveFramePost{}); len(again.Input) != 0 {
		t.Fatalf("input delivered twice: %+v", again)
	}

	// An empty post waiting for input answers as soon as it is queued.
	got := make(chan component.LiveFrameReply, 1)
	go func() { got <- post(component.LiveFramePost{WaitInput: true}) }()
	time.Sleep(100 * time.Millisecond)
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputText, Text: "fast"}}}); code != http.StatusNoContent {
		t.Fatalf("input = %d", code)
	}
	select {
	case r := <-got:
		if len(r.Input) != 1 || r.Input[0].Text != "fast" {
			t.Fatalf("long poll = %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting post did not answer with the input")
	}
	// A frame that leaves the input takes none: it waits for the tool's
	// waiting post.
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputText, Text: "later"}}}); code != http.StatusNoContent {
		t.Fatalf("input = %d", code)
	}
	if r := post(component.LiveFramePost{LeaveInput: true}); len(r.Input) != 0 || r.Controller != "maya" {
		t.Fatalf("a leaving post took the input: %+v", r)
	}
	if r := post(component.LiveFramePost{WaitInput: true}); len(r.Input) != 1 || r.Input[0].Text != "later" {
		t.Fatalf("the waiting post after a leaving one = %+v", r)
	}

	// Control lapses without renewal; then input is refused again.
	now = now.Add(wire.LiveControlLapseMs*time.Millisecond + time.Second)
	if reply := post(component.LiveFramePost{}); reply.Controller != "" {
		t.Fatalf("control did not lapse: %+v", reply)
	}
	// Bringing the window forward is the owner's, without control.
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputFocus}}}); code != http.StatusNoContent {
		t.Fatalf("owner focus = %d", code)
	}
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "jack", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputFocus}}}); code != http.StatusForbidden {
		t.Fatalf("jack focus = %d, want 403", code)
	}

	// A view waiting for a person says so to the room and in the list a
	// late viewer reads; a frame clears it until the tool says it again.
	drain := func() wire.JobLiveFrame {
		t.Helper()
		var last wire.JobLiveFrame
		for {
			select {
			case ev := <-sub.Events():
				raw, _ := json.Marshal(ev.Payload)
				_ = json.Unmarshal(raw, &last)
			case <-time.After(200 * time.Millisecond):
				return last
			}
		}
	}
	drain()
	post(component.LiveFramePost{Waiting: "Sign in to continue"})
	if ev := drain(); ev.Waiting != "Sign in to continue" || ev.Seq != 2 {
		t.Fatalf("waiting event = %+v", ev)
	}
	code, out := call(http.MethodGet, "/v1/rooms/room-1/live", "jack", nil)
	var list wire.JobLiveList
	if code != http.StatusOK || json.Unmarshal(out, &list) != nil || len(list.Views) != 1 || list.Views[0].JobID != "job-1" || list.Views[0].Waiting == "" {
		t.Fatalf("list = %d %s", code, out)
	}
	post(component.LiveFramePost{Waiting: "Sign in to continue", Ready: true})
	if ev := drain(); !ev.Ready || ev.Waiting == "" {
		t.Fatalf("ready event = %+v", ev)
	}
	if code, _ := call(http.MethodGet, "/v1/rooms/room-1/live", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("a stranger's list = %d, want 403", code)
	}

	// The page's dialog rides with the view's facts while the tool says it
	// is open (an unknown kind is dropped), and is answered as input.
	post(component.LiveFramePost{Waiting: "Sign in to continue", Dialog: &component.LiveDialog{Kind: component.LiveDialogConfirm, Message: "Leave the sign-in?"}})
	if ev := drain(); ev.Dialog == nil || ev.Dialog.Kind != component.LiveDialogConfirm || ev.Dialog.Message != "Leave the sign-in?" {
		t.Fatalf("dialog event = %+v", ev)
	}
	post(component.LiveFramePost{Waiting: "Sign in to continue", Dialog: &component.LiveDialog{Kind: "print", Message: "x"}})
	if ev := drain(); ev.Dialog != nil {
		t.Fatalf("an unknown dialog kind was kept: %+v", ev.Dialog)
	}
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/control", "maya", wire.LiveControlPost{Take: true}); code != http.StatusOK {
		t.Fatalf("take control = %d", code)
	}
	if code, _ := call(http.MethodPost, "/v1/rooms/room-1/jobs/job-1/live/input", "maya", wire.LiveInputPost{Events: []component.LiveInput{{Type: component.LiveInputDialog, Action: "accept"}}}); code != http.StatusNoContent {
		t.Fatalf("dialog answer = %d", code)
	}
	if r := post(component.LiveFramePost{Waiting: "Sign in to continue"}); len(r.Input) != 1 || r.Input[0].Type != component.LiveInputDialog {
		t.Fatalf("the answer did not reach the tool: %+v", r)
	}

	// The tool ends the view: gone for everyone, and the room is told.
	post(component.LiveFramePost{End: true})
	if ev := drain(); !ev.Ended || ev.JobID != "job-1" {
		t.Fatalf("end event = %+v", ev)
	}
	if code, _ := call(http.MethodGet, "/v1/rooms/room-1/jobs/job-1/live/frame", "jack", nil); code != http.StatusNotFound {
		t.Fatalf("ended view = %d, want 404", code)
	}

	// A view the tool stopped posting to is gone.
	post(component.LiveFramePost{Run: "run-1", Seq: 3, MimeType: "image/jpeg", Data: []byte("jpeg-3"), Width: 800, Height: 600})
	now = now.Add(liveIdleFor + time.Second)
	if code, _ := call(http.MethodGet, "/v1/rooms/room-1/jobs/job-1/live/frame", "jack", nil); code != http.StatusNotFound {
		t.Fatalf("idle view = %d, want 404", code)
	}
}
