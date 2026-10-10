package realtimehttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// BE-5 + BE-6 end to end: the shared signed publisher reaches realtime's
// guarded internal route, and the event is persisted under the publisher's
// tenant (not 'default'), so a non-default tenant's agent replies appear in
// its own history. An unsigned publish is refused before it fans out.
func TestSignedInternalPublishPersistsUnderPublisherTenant(t *testing.T) {
	const key = "internal-key"
	t.Setenv("BNBLOC_INTERNAL_KEY", key)
	hub := realtime.NewHub(16, 8)
	chat := store.NewMemoryChat()
	svc := &Service{Hub: hub, Chat: chat}
	srv := httptest.NewServer(svcauth.Middleware(key, svc.Handler()))
	defer srv.Close()

	pub := realtime.NewHTTPPublisher(srv.URL, "activity")
	pub.Publish(tenant.NewContext(context.Background(), "acme"), wire.Event{RoomID: "room-1", Kind: "agent_messages", Payload: map[string]any{"userId": "alice"}})

	history := func(tid string) int {
		msgs, err := chat.RoomHistory(tenant.NewContext(context.Background(), tid), "room-1", "", 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(msgs)
	}
	if got := history("acme"); got != 1 {
		t.Fatalf("acme history = %d, want 1", got)
	}
	if got := history(tenant.Default); got != 0 {
		t.Fatalf("default-tenant history = %d, want 0 (reply filed under the wrong tenant)", got)
	}

	// A forged, unsigned publish never reaches the hub.
	sub := hub.SubscribeLive("room-1")
	defer hub.Unsubscribe("room-1", sub)
	resp, err := http.Post(srv.URL+"/internal/v1/rooms/room-1/publish", "application/json",
		bytes.NewReader([]byte(`{"kind":"chat_message","payload":{"userId":"mallory","recipient":{"ownerId":"bob","provider":"codex"}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned publish status = %d, want 401", resp.StatusCode)
	}
	select {
	case ev := <-sub.Events():
		t.Fatalf("forged event delivered: %+v", ev)
	default:
	}
}
