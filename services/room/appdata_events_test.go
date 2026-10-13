package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// recordingPublisher keeps every event published.
type recordingPublisher struct {
	mu  sync.Mutex
	evs []wire.Event
}

func (p *recordingPublisher) Publish(_ context.Context, ev wire.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, ev)
}

// Every write announces itself as a transient app_changed event carrying
// only the app, collection, id and version (0: deleted; no collection:
// wiped), never the document.
func TestAppDataWritesAnnounceMetadataOnly(t *testing.T) {
	b, err := store.NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	e := newAppEnv(t, b)
	pub := &recordingPublisher{}
	e.svc.Publisher = pub
	doc := e.insert(jack, "votes", map[string]any{"choice": "secret-ballot"}, false)
	id := doc["_id"].(string)
	if r := e.do(jack, http.MethodPatch, "/c/votes/"+id, map[string]any{"merge": map[string]any{"choice": "changed-ballot"}}); r.status != http.StatusOK {
		t.Fatalf("patch = %d %s", r.status, r.body)
	}
	if r := e.do(jack, http.MethodPut, "/c/tally/t1", map[string]any{"doc": map[string]any{"n": 1}}, "If-None-Match: *"); r.status != http.StatusCreated {
		t.Fatalf("put = %d %s", r.status, r.body)
	}
	if r := e.do(jack, http.MethodDelete, "/c/votes/"+id, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.status, r.body)
	}
	if r := e.do(maya, http.MethodDelete, "/data", nil); r.status != http.StatusOK {
		t.Fatalf("wipe = %d %s", r.status, r.body)
	}
	// A refused write announces nothing.
	if r := e.do(zed, http.MethodDelete, "/data", nil); r.status != http.StatusForbidden {
		t.Fatalf("wipe by a non-owner = %d %s", r.status, r.body)
	}
	want := []wire.AppChanged{
		{AppID: e.app, Collection: "votes", ID: id, Version: 1},
		{AppID: e.app, Collection: "votes", ID: id, Version: 2},
		{AppID: e.app, Collection: "tally", ID: "t1", Version: 1},
		{AppID: e.app, Collection: "votes", ID: id, Version: 0},
		{AppID: e.app},
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.evs) != len(want) {
		t.Fatalf("published %d events, want %d: %+v", len(pub.evs), len(want), pub.evs)
	}
	for i, ev := range pub.evs {
		if ev.Kind != wire.EventAppChanged || ev.RoomID != e.room || ev.ChannelID != "" {
			t.Fatalf("event %d = %s in %s/%s", i, ev.Kind, ev.RoomID, ev.ChannelID)
		}
		got, ok := ev.Payload.(wire.AppChanged)
		if !ok || got != want[i] {
			t.Fatalf("event %d payload = %#v, want %#v", i, ev.Payload, want[i])
		}
		raw, _ := json.Marshal(ev)
		if bytes.Contains(raw, []byte("ballot")) {
			t.Fatalf("event %d carries content: %s", i, raw)
		}
	}
}
