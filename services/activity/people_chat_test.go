package activity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type kindsByChannel map[string]store.ChannelAdmission

func (k kindsByChannel) ChannelAdmission(_ context.Context, _, channelID string) (store.ChannelAdmission, bool, error) {
	if channelID == "broken" {
		return store.ChannelAdmission{}, false, errors.New("db down")
	}
	a, ok := k[channelID]
	return a, ok, nil
}

// An agent reply into the people chat is refused BEFORE it is stored: a
// stored reply (cursor advanced) that realtime then refuses to publish
// would vanish without an error. A failed kind read refuses too.
func TestAgentReplyRefusedInPeopleChatBeforeAppend(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	st := store.NewMemory()
	svc := &Service{Store: st, Publisher: realtime.HubPublisher{Hub: realtime.NewHub(64, 16)}, Membership: mem,
		Channels: kindsByChannel{"ch-people": {Kind: store.ChannelKindPeople}, "ch-general": {}, "ch-quiet": {NoAgents: true}}}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	reply := func(channel string) int {
		resp := post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "maya", wire.ReportMessagesInput{
			WorkspaceID: "room-1", AgentSessionID: "s1", ChannelID: channel,
			Messages: []wire.Message{{Seq: 1, Payload: map[string]any{"text": "hi", "provider": "codex"}}},
		})
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := reply("ch-people"); code != http.StatusBadRequest {
		t.Fatalf("people chat = %d, want 400", code)
	}
	// A channel whose settings bar agents refuses the reply the same way.
	if code := reply("ch-quiet"); code != http.StatusBadRequest {
		t.Fatalf("no-agents channel = %d, want 400", code)
	}
	if code := reply("broken"); code != http.StatusInternalServerError {
		t.Fatalf("failed kind read = %d, want 500", code)
	}
	// Nothing was stored: the session's first reply is still fresh.
	fresh, _, err := st.AppendMessages(tenant.NewContext(context.Background(), tenant.Default), "room-1", wire.ReportMessagesInput{
		WorkspaceID: "room-1", AgentSessionID: "s1",
		Messages: []wire.Message{{Seq: 1, Payload: map[string]any{"text": "hi"}}},
	})
	if err != nil || len(fresh) != 1 {
		t.Fatalf("a refused reply was stored: fresh = %d (%v)", len(fresh), err)
	}
	if code := reply("ch-general"); code != http.StatusOK {
		t.Fatalf("ordinary channel = %d, want 200", code)
	}
}
