package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// newOwnershipServer builds an activity service with one QUEUED job owned by
// maya and maya's bridge registered + advertising the job's agent. Both maya
// and mallory are members of room-1.
func newOwnershipServer(t *testing.T, allowServeAll bool) (*httptest.Server, *capturePublisher, store.BridgeJobStore) {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	mem.Add("room-1", "mallory")
	pub := &capturePublisher{}
	jobs := store.NewMemoryBridgeJobs()
	svc := &Service{Store: store.NewMemory(), Publisher: pub, Membership: mem, BridgeJobs: jobs, AllowServeAllOwners: allowServeAll}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-q", RoomID: "room-1", OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", MessageText: "secret prompt", Status: store.AgentJobQueued, CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	heartbeat(t, srv, "maya", "bridge-maya", false).Body.Close()
	return srv, pub, jobs
}

func heartbeat(t *testing.T, srv *httptest.Server, user, bridgeID string, serveAll bool) *http.Response {
	t.Helper()
	return post(t, srv, "/v1/bridge/heartbeat", user, map[string]any{
		"bridgeId":       bridgeID,
		"serveAllOwners": serveAll,
		"agents":         []map[string]any{{"core": "nimbus", "provider": "nimbus", "targetId": "nimbus/nimbus"}},
	})
}

func claimJobs(t *testing.T, srv *httptest.Server, user, bridgeID string, serveAll bool) (int, []store.AgentJob) {
	t.Helper()
	resp := post(t, srv, "/v1/bridge/jobs/claim", user, map[string]any{"bridgeId": bridgeID, "maxJobs": 5, "serveAllOwners": serveAll})
	defer resp.Body.Close()
	var out struct {
		Jobs []store.AgentJob `json:"jobs"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Jobs
}

// BE-2: a heartbeat for a bridge id registered to another member must not
// re-own that bridge (which would hand over its advertised agents and claims).
func TestHeartbeatCannotReownAnotherMembersBridge(t *testing.T) {
	srv, _, jobs := newOwnershipServer(t, false)
	resp := heartbeat(t, srv, "mallory", "bridge-maya", false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("heartbeat steal status = %d, want 403", resp.StatusCode)
	}
	hb, err := jobs.LookupBridge(tenant.NewContext(context.Background(), tenant.Default), "bridge-maya")
	if err != nil || hb.OwnerUserID != "maya" {
		t.Fatalf("bridge after steal attempt = %+v, %v; want still owned by maya", hb, err)
	}
}

// BE-2: claiming through another member's bridge id is refused.
func TestClaimThroughForeignBridgeIsForbidden(t *testing.T) {
	srv, _, _ := newOwnershipServer(t, false)
	heartbeat(t, srv, "mallory", "bridge-mallory", false).Body.Close()
	status, got := claimJobs(t, srv, "mallory", "bridge-maya", false)
	if status != http.StatusForbidden || len(got) != 0 {
		t.Fatalf("claim via foreign bridge = %d %+v, want 403 and no jobs", status, got)
	}
	if status, _ := claimJobs(t, srv, "mallory", "bridge-unregistered", false); status != http.StatusForbidden {
		t.Fatalf("claim via unregistered bridge = %d, want 403", status)
	}
}

// BE-2: a body serveAllOwners flag is not an escalation. Without the server
// gate, mallory's bridge advertising the same agent still cannot take maya's
// queued prompt; the owner's own bridge can.
func TestServeAllFromBodyCannotStealJobs(t *testing.T) {
	srv, _, _ := newOwnershipServer(t, false)
	resp := heartbeat(t, srv, "mallory", "bridge-mallory", true)
	var hb struct {
		ServeAllOwners bool `json:"serveAllOwners"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hb)
	resp.Body.Close()
	if hb.ServeAllOwners {
		t.Fatal("heartbeat reported serveAllOwners honored without the server gate")
	}
	status, got := claimJobs(t, srv, "mallory", "bridge-mallory", true)
	if status != http.StatusOK || len(got) != 0 {
		t.Fatalf("ungated serve-all claim = %d %+v, want 200 and no jobs", status, got)
	}
	status, got = claimJobs(t, srv, "maya", "bridge-maya", false)
	if status != http.StatusOK || len(got) != 1 || got[0].JobID != "job-q" {
		t.Fatalf("owner claim = %d %+v, want job-q", status, got)
	}
}

// With the server-side gate (single-host testing) a serve-all bridge drives
// another member's job, as BNBLOC_BRIDGE_SERVE_ALL_OWNERS requires.
func TestServeAllHonoredOnlyWithServerGate(t *testing.T) {
	srv, _, _ := newOwnershipServer(t, true)
	heartbeat(t, srv, "mallory", "bridge-mallory", true).Body.Close()
	status, got := claimJobs(t, srv, "mallory", "bridge-mallory", true)
	if status != http.StatusOK || len(got) != 1 || got[0].JobID != "job-q" {
		t.Fatalf("gated serve-all claim = %d %+v, want job-q", status, got)
	}
}

// BE-7: a member cannot post a reply attributed to another member's agent.
func TestMessagesRejectForeignOwnerAttribution(t *testing.T) {
	srv, pub, _ := newOwnershipServer(t, false)
	in := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"role": "assistant", "text": "forged", "ownerUserId": "maya", "provider": "nimbus"}},
	}}
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged attribution status = %d, want 403", resp.StatusCode)
	}
	if len(pub.events) != 0 {
		t.Fatalf("published %d events for a forged reply, want 0", len(pub.events))
	}
}

// BE-7: an unattributed reply is attributed to the authenticated caller, and
// the caller's own attribution passes.
func TestMessagesAttributeToCaller(t *testing.T) {
	srv, pub, _ := newOwnershipServer(t, false)
	in := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"role": "assistant", "text": "a"}},
		{Seq: 2, Payload: map[string]any{"role": "assistant", "text": "b", "ownerUserId": "mallory"}},
	}}
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(pub.events) != 1 {
		t.Fatalf("status = %d events = %d, want 200 and 1 event", resp.StatusCode, len(pub.events))
	}
	for _, m := range pub.events[0].Payload.(wire.ReportMessagesInput).Messages {
		if owner := m.Payload.(map[string]any)["ownerUserId"]; owner != "mallory" {
			t.Fatalf("message %d owner = %v, want mallory", m.Seq, owner)
		}
	}
}

// BE-7: serve-all attribution to another room member requires the server gate.
func TestMessagesForeignOwnerAllowedOnlyUnderGate(t *testing.T) {
	srv, pub, _ := newOwnershipServer(t, true)
	in := wire.ReportMessagesInput{WorkspaceID: "room-1", AgentSessionID: "s1", Messages: []wire.Message{
		{Seq: 1, Payload: map[string]any{"role": "assistant", "text": "a", "ownerUserId": "maya"}},
	}}
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(pub.events) != 1 {
		t.Fatalf("gated attribution status = %d events = %d, want 200 and 1", resp.StatusCode, len(pub.events))
	}
	// Even under the gate, attribution is limited to room members.
	in.Messages[0] = wire.Message{Seq: 2, Payload: map[string]any{"text": "x", "ownerUserId": "outsider"}}
	resp = post(t, srv, "/v1/rooms/room-1/agent-activity/messages", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-member attribution status = %d, want 403", resp.StatusCode)
	}
}

// BE-7: session state is attributed the same way.
func TestSessionStateRejectsForeignOwner(t *testing.T) {
	srv, pub, _ := newOwnershipServer(t, false)
	in := wire.ReportSessionStateInput{WorkspaceID: "room-1", AgentSessionID: "s1", OwnerUserID: "maya", State: map[string]any{"phase": "running"}}
	resp := post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || len(pub.events) != 0 {
		t.Fatalf("forged session-state = %d events=%d, want 403 and none", resp.StatusCode, len(pub.events))
	}
	in.OwnerUserID = ""
	resp = post(t, srv, "/v1/rooms/room-1/agent-activity/session-state", "mallory", in)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(pub.events) != 1 {
		t.Fatalf("own session-state = %d events=%d, want 200 and 1", resp.StatusCode, len(pub.events))
	}
	if got := pub.events[0].Payload.(wire.ReportSessionStateInput).OwnerUserID; got != "mallory" {
		t.Fatalf("session-state owner = %q, want mallory", got)
	}
}

// BE-17: request bodies are capped; an oversized body is refused with 413.
func TestOversizedBodiesAreRejected(t *testing.T) {
	srv, _, _ := newOwnershipServer(t, false)
	send := func(path string, size int) int {
		body := `{"workspaceId":"room-1","pad":"` + strings.Repeat("x", size) + `"}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader([]byte(body)))
		req.Header.Set("X-Bnbloc-User", "maya")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := send("/v1/rooms/room-1/agent-activity/session-state", maxBodyBytes+1); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized session-state = %d, want 413", got)
	}
	if got := send("/v1/bridge/heartbeat", maxBodyBytes+1); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized heartbeat = %d, want 413", got)
	}
	if got := send("/v1/rooms/room-1/agent-activity/messages", maxMessagesBodyBytes+1); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized messages = %d, want 413", got)
	}
}

// BE-2 with the bridge's real id shape (bridge-<owner>-<tenant>): the owner's
// bridge heartbeats and claims; another member reusing that id is refused, in
// both normal and serve-all modes.
func TestRealBridgeIDsKeepOwnership(t *testing.T) {
	for _, gate := range []bool{false, true} {
		srv, _, _ := newOwnershipServer(t, gate)
		if resp := heartbeat(t, srv, "maya", "bridge-maya-default", gate); resp.StatusCode != http.StatusOK {
			t.Fatalf("gate=%v owner heartbeat = %d", gate, resp.StatusCode)
		} else {
			resp.Body.Close()
		}
		if resp := heartbeat(t, srv, "mallory", "bridge-maya-default", gate); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("gate=%v foreign heartbeat on owner's id = %d, want 403", gate, resp.StatusCode)
		} else {
			resp.Body.Close()
		}
		if status, _ := claimJobs(t, srv, "mallory", "bridge-maya-default", gate); status != http.StatusForbidden {
			t.Fatalf("gate=%v foreign claim on owner's id = %d, want 403", gate, status)
		}
		if status, got := claimJobs(t, srv, "maya", "bridge-maya-default", gate); status != http.StatusOK || len(got) != 1 {
			t.Fatalf("gate=%v owner claim = %d %+v, want the job", gate, status, got)
		}
	}
}
