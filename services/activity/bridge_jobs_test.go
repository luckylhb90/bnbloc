package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// newJobServer builds an activity service backed by the in-memory job queue,
// with one queued job already claimed by "bridge-maya" (owner "maya").
func newJobServer(t *testing.T) (*httptest.Server, *capturePublisher, store.BridgeJobStore) {
	t.Helper()
	mem := membership.NewStatic()
	mem.Add("room-1", "maya")
	pub := &capturePublisher{}
	jobs := store.NewMemoryBridgeJobs()
	svc := &Service{Store: store.NewMemory(), Publisher: pub, Membership: mem, BridgeJobs: jobs}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)

	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", ChannelID: "chan-1", MessageSeq: 7, OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", MessageText: "go", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "bridge-maya", OwnerUserID: "maya", Status: store.BridgeOnline, LastSeenAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.ReplaceBridgeAgents(ctx, "bridge-maya", []store.BridgeAgent{{OwnerUserID: "maya", Core: "nimbus", Provider: "nimbus", LastSeenAtMs: 1}}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := jobs.ClaimAgentJobs(ctx, store.ClaimRequest{BridgeID: "bridge-maya", OwnerUserID: "maya", MaxJobs: 1, NowMs: 2}); err != nil || len(claimed) != 1 {
		t.Fatalf("setup claim = %d jobs, %v", len(claimed), err)
	}
	return srv, pub, jobs
}

// A job transition must reach the room that owns the job. Publishing without the
// stored room id strands the event and the composer never learns the turn ended.
func TestJobStatusPublishesToOwningRoom(t *testing.T) {
	srv, pub, _ := newJobServer(t)
	resp := post(t, srv, "/v1/bridge/jobs/job-1/status", "maya", map[string]any{"bridgeId": "bridge-maya", "status": "replied"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(pub.events) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.RoomID != "room-1" || ev.ChannelID != "chan-1" || ev.Kind != "agent_job_updated" {
		t.Fatalf("event = %+v, want room-1/chan-1 agent_job_updated", ev)
	}
	job, ok := ev.Payload.(wire.AgentJob)
	if !ok || job.MessageSeq != 7 || job.Status != string(store.AgentJobReplied) {
		t.Fatalf("payload = %+v, want the replied job for seq 7", ev.Payload)
	}
}

// Only the member who registered a bridge may report its jobs. Otherwise any
// room member knowing a bridgeId could drive another member's job to terminal.
func TestJobStatusRejectsForeignBridgeOwner(t *testing.T) {
	srv, pub, jobs := newJobServer(t)
	resp := post(t, srv, "/v1/bridge/jobs/job-1/status", "intruder", map[string]any{"bridgeId": "bridge-maya", "status": "failed"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if len(pub.events) != 0 {
		t.Fatalf("published %d events on rejection, want 0", len(pub.events))
	}
	got, err := jobs.ListAgentJobs(tenant.NewContext(context.Background(), tenant.Default), "room-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != store.AgentJobClaimed {
		t.Fatalf("job status = %+v, want still claimed", got)
	}
}

// A running turn renews its lease so no other bridge re-claims it mid-flight.
func TestJobLeaseRenewalExtendsOwnLease(t *testing.T) {
	srv, _, jobs := newJobServer(t)
	resp := post(t, srv, "/v1/bridge/jobs/job-1/lease", "maya", map[string]any{"bridgeId": "bridge-maya"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		LeaseExpiresAtMs int64 `json:"leaseExpiresAtMs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.LeaseExpiresAtMs <= 0 {
		t.Fatalf("leaseExpiresAtMs = %d, want a future lease", body.LeaseExpiresAtMs)
	}

	other := post(t, srv, "/v1/bridge/jobs/job-1/lease", "intruder", map[string]any{"bridgeId": "bridge-maya"})
	defer other.Body.Close()
	if other.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign lease renewal status = %d, want 403", other.StatusCode)
	}
	_ = jobs
}

// An unknown status must be refused rather than written through to the queue.
func TestJobStatusRejectsUnknownStatus(t *testing.T) {
	srv, _, _ := newJobServer(t)
	resp := post(t, srv, "/v1/bridge/jobs/job-1/status", "maya", map[string]any{"bridgeId": "bridge-maya", "status": "banana"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// A job nothing ever claimed must fail on its own, notify its room, and leave a
// visible message. Without the message the turn ends silently: the member only
// ever sees their own prompt and no explanation.
func TestSweepFailsQueuedJobAndNotifiesRoom(t *testing.T) {
	mem := membership.NewStatic()
	pub := &capturePublisher{}
	jobs := store.NewMemoryBridgeJobs()
	svc := &Service{Store: store.NewMemory(), Publisher: pub, Membership: mem, BridgeJobs: jobs}
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	if _, err := jobs.PutAgentJob(ctx, store.AgentJob{JobID: "stuck", RoomID: "room-9", ChannelID: "chan-9", MessageSeq: 1, OwnerUserID: "maya", Core: "c", Provider: "nimbus", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}

	svc.SweepAgentJobs(ctx)

	var job wire.AgentJob
	var failureText string
	var tag map[string]any
	for _, ev := range pub.events {
		if ev.RoomID != "room-9" {
			t.Fatalf("event published to %q, want room-9", ev.RoomID)
		}
		switch p := ev.Payload.(type) {
		case wire.AgentJob:
			job = p
		case wire.ReportMessagesInput:
			if len(p.Messages) == 1 {
				if m, ok := p.Messages[0].Payload.(map[string]any); ok {
					failureText, _ = m["text"].(string)
					tag = m
				}
			}
		}
	}
	if job.Status != string(store.AgentJobFailed) || job.FailureReason != string(store.FailureNoBridgeOnline) {
		t.Fatalf("swept job = %+v, want failed/no_bridge_online", job)
	}
	if !strings.Contains(failureText, "offline") {
		t.Fatalf("room message = %q, want a visible offline explanation", failureText)
	}
	// The notice names its job and request, so the feed nests it.
	if tag["jobId"] != "stuck" || tag["sourceSeq"] != int64(1) {
		t.Fatalf("notice tag = %v/%v; want stuck/1", tag["jobId"], tag["sourceSeq"])
	}
}

// Cancelling a live job must stop its worker too: after cancel the bridge that
// still holds the lease can no longer drive the job to replied, otherwise a
// cancelled turn would post its answer anyway.
func TestCancelStopsRunningJobAndLocksOutWorker(t *testing.T) {
	srv, pub, _ := newJobServer(t)
	resp := post(t, srv, "/v1/rooms/room-1/agent-jobs/job-1/cancel", "maya", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", resp.StatusCode)
	}
	var cancelled wire.AgentJob
	for _, ev := range pub.events {
		if j, ok := ev.Payload.(wire.AgentJob); ok {
			cancelled = j
		}
	}
	if cancelled.Status != string(store.AgentJobCancelled) || cancelled.RoomID != "room-1" {
		t.Fatalf("published job = %+v, want cancelled in room-1", cancelled)
	}

	late := post(t, srv, "/v1/bridge/jobs/job-1/status", "maya", map[string]any{"bridgeId": "bridge-maya", "status": "replied"})
	defer late.Body.Close()
	if late.StatusCode != http.StatusConflict {
		t.Fatalf("worker report after cancel = %d, want 409", late.StatusCode)
	}
}

// Cancellation is a room action, so a non-member must not be able to stop
// another room's job.
func TestCancelRejectsNonMember(t *testing.T) {
	srv, _, jobs := newJobServer(t)
	resp := post(t, srv, "/v1/rooms/room-1/agent-jobs/job-1/cancel", "intruder", map[string]any{})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	got, err := jobs.ListAgentJobs(tenant.NewContext(context.Background(), tenant.Default), "room-1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status == store.AgentJobCancelled {
		t.Fatalf("job = %+v, want untouched", got)
	}
}

// The sweeper runs on a background context with no tenant, exactly as the daemon
// does. Its failure message must still be written: the activity store is
// tenant-scoped, so writing in the caller's (empty) scope fails tenant.Require
// and the room is left with no explanation at all.
func TestSweptFailureMessageIsWrittenWithoutCallerTenant(t *testing.T) {
	mem := membership.NewStatic()
	mem.Add("room-9", "maya")
	activityStore := &tenantRecordingActivity{Memory: store.NewMemory()}
	jobs := store.NewMemoryBridgeJobs()
	pub := &capturePublisher{}
	svc := &Service{Store: activityStore, Publisher: pub, Membership: mem, BridgeJobs: jobs}
	seed := tenant.NewContext(context.Background(), tenant.Default)
	if _, err := jobs.PutAgentJob(seed, store.AgentJob{JobID: "stuck-durable", RoomID: "room-9", MessageSeq: 1, OwnerUserID: "maya", Core: "c", Provider: "nimbus", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}

	// No tenant on this context, matching the daemon's sweep loop.
	svc.SweepAgentJobs(context.Background())

	// Durable when a reader in the job's tenant can see it, which is what a
	// member reloading the room does.
	if got := activityStore.appended["room-9"]; got != tenant.Default {
		t.Fatalf("sweep stored the failure message in tenant %q, want %q", got, tenant.Default)
	}
	var sawMessage bool
	for _, ev := range pub.events {
		if ev.Kind == "agent_messages" {
			sawMessage = true
		}
	}
	if !sawMessage {
		t.Fatal("sweep published no visible failure message")
	}
}

// GET /v1/my/agents reports the member's desktop app apart from its agents:
// a running app with no agent core is connected, an app seen before but not
// now is installed-and-closed, and a member whose app never connected has
// lastSeenAtMs 0.
func TestMyAgentsReportsDesktop(t *testing.T) {
	jobs := store.NewMemoryBridgeJobs()
	svc := &Service{Store: store.NewMemory(), Publisher: &capturePublisher{}, Membership: membership.NewStatic(), BridgeJobs: jobs}
	srv := httptest.NewServer(svc.Handler())
	t.Cleanup(srv.Close)
	ctx := tenant.NewContext(context.Background(), tenant.Default)
	now := time.Now().UnixMilli()
	for _, hb := range []store.BridgeHeartbeat{
		{BridgeID: "bridge-maya", OwnerUserID: "maya", Status: store.BridgeOnline, LastSeenAtMs: now},
		{BridgeID: "bridge-jack-old", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: now - 3_600_000},
		{BridgeID: "bridge-jack", OwnerUserID: "jack", Status: store.BridgeOffline, LastSeenAtMs: now - 60_000},
	} {
		if err := jobs.PutBridgeHeartbeat(ctx, hb); err != nil {
			t.Fatal(err)
		}
	}
	get := func(user string) (online bool, lastSeen int64, agents int) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/my/agents", nil)
		req.Header.Set("X-Bnbloc-User", user)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", user, resp.StatusCode)
		}
		var body struct {
			Agents  []json.RawMessage `json:"agents"`
			Desktop struct {
				Online       bool  `json:"online"`
				LastSeenAtMs int64 `json:"lastSeenAtMs"`
			} `json:"desktop"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Desktop.Online, body.Desktop.LastSeenAtMs, len(body.Agents)
	}
	if online, seen, n := get("maya"); !online || seen != now || n != 0 {
		t.Fatalf("maya (app running, no core) = online %v seen %d agents %d", online, seen, n)
	}
	if online, seen, _ := get("jack"); online || seen != now-60_000 {
		t.Fatalf("jack (app closed) = online %v seen %d, want offline at the latest heartbeat", online, seen)
	}
	if online, seen, _ := get("alice"); online || seen != 0 {
		t.Fatalf("alice (never connected) = online %v seen %d", online, seen)
	}
}

// tenantRecordingActivity records the tenant each room's messages were
// appended in.
type tenantRecordingActivity struct {
	*store.Memory
	appended map[string]string
}

func (a *tenantRecordingActivity) AppendMessages(ctx context.Context, roomID string, in wire.ReportMessagesInput) ([]wire.Message, int64, error) {
	fresh, next, err := a.Memory.AppendMessages(ctx, roomID, in)
	if err == nil {
		if a.appended == nil {
			a.appended = map[string]string{}
		}
		a.appended[roomID] = tenant.FromContext(ctx)
	}
	return fresh, next, err
}
