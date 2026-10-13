package realtimehttp

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The app events route is the gateway's: it streams the room's
// app_changed events only, each as wire.AppChanged and nothing more, and
// ends at the room's deletion. A member's signed call is refused.
func TestInternalAppEventsCarryOnlyAppChanges(t *testing.T) {
	const key = "internal-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	hub := realtime.NewHub(16, 8)
	svc := &Service{Hub: hub}
	srv := httptest.NewServer(svcauth.Middleware(key, svc.Handler()))
	defer srv.Close()

	open := func(subject string) *http.Response {
		t.Helper()
		req, err := svcauth.Key(key).NewRequest(context.Background(), http.MethodGet, srv.URL+"/internal/v1/rooms/room-1/app-events", nil, subject, "acme")
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := open("maya"); resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		t.Fatalf("a member's app events = %d, want 403", resp.StatusCode)
	}
	resp := open(svcauth.ServiceSubject("gateway"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app events = %d", resp.StatusCode)
	}
	lines := bufio.NewScanner(resp.Body)
	next := func() string {
		t.Helper()
		for lines.Scan() {
			if l := lines.Text(); l != "" && !strings.HasPrefix(l, ":") {
				return l
			}
		}
		return ""
	}
	deadline := time.Now().Add(5 * time.Second)
	for hub.SubscriberCount("room-1") == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.KindChatMessage, Payload: map[string]any{"text": "hello"}})
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventArtifactUpdated, Payload: wire.ArtifactUpdated{ArtifactID: "art_1", Title: "secret"}})
	// Another kind shaped like an app change is still another kind.
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventArtifactState, Payload: map[string]any{"appId": "art_1", "collection": "x", "id": "leak", "version": 9}})
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventAppChanged, Payload: map[string]any{"appId": "art_1", "collection": "votes", "id": "d1", "version": 2, "body": "smuggled"}})
	hub.Publish(wire.Event{RoomID: "room-2", Kind: wire.EventAppChanged, Payload: wire.AppChanged{AppID: "art_9", Collection: "votes", ID: "x", Version: 1}})
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventAppChanged, Payload: wire.AppChanged{AppID: "art_2", Collection: "c", ID: "d2", Version: 0}})
	if got, want := next(), "event: app_changed"; got != want {
		t.Fatalf("first line %q, want %q", got, want)
	}
	if got, want := next(), `data: {"appId":"art_1","collection":"votes","id":"d1","version":2}`; got != want {
		t.Fatalf("data %q, want %q", got, want)
	}
	if got := next(); got != "event: app_changed" {
		t.Fatalf("second event %q", got)
	}
	if got, want := next(), `data: {"appId":"art_2","collection":"c","id":"d2","version":0}`; got != want {
		t.Fatalf("data %q, want %q", got, want)
	}
	hub.Publish(wire.Event{RoomID: "room-1", Kind: wire.EventRoomDeleted})
	if got := next(); got != "" {
		t.Fatalf("after the room's deletion the stream went on: %q", got)
	}
}
