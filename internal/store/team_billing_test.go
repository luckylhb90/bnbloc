package store

import (
	"errors"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Team dashboard (F1) and borrow billing (F3) store contracts, run on both
// backends (forEachBackend) with per-run tenants and ids.

func intp(v int) *int { return &v }

func TestContractQuotaRequestStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		quotaRequestCases(t, b.QuotaRequests, uniq("t-quota-"), uniq("room-q-"))
	})
}

func quotaRequestCases(t *testing.T, s QuotaRequestStore, tnt, room string) {
	t.Helper()
	ctx := tenantCtx(tnt)
	req := QuotaRequest{RequestID: "qr-1-" + room, RoomID: room, OwnerUserID: "alice", RequesterUserID: "bob",
		Provider: "core:codex", RequestedRuns: intp(5), Note: "more please", CreatedAtMs: 100}
	got, err := s.CreateQuotaRequest(ctx, req)
	if err != nil || got.Status != QuotaRequestPending || got.TenantID != tnt {
		t.Fatalf("create = %+v, %v", got, err)
	}
	dup := req
	dup.RequestID = "qr-2-" + room
	existing, err := s.CreateQuotaRequest(ctx, dup)
	if !errors.Is(err, ErrQuotaRequestPending) || existing.RequestID != req.RequestID {
		t.Fatalf("duplicate = %+v, %v; want the open request with ErrQuotaRequestPending", existing, err)
	}
	if _, err := s.GetQuotaRequest(tenantCtx(tnt+"-other"), req.RequestID); !errors.Is(err, ErrQuotaRequestNotFound) {
		t.Fatalf("cross-tenant get err = %v, want not found", err)
	}
	if n := len(must(s.ListQuotaRequests(ctx, QuotaRequestQuery{OwnerUserID: "alice", Status: QuotaRequestPending}))(t)); n != 1 {
		t.Fatalf("owner pending = %d, want 1", n)
	}
	if n := len(must(s.ListQuotaRequests(ctx, QuotaRequestQuery{RequesterUserID: "alice"}))(t)); n != 0 {
		t.Fatalf("alice as requester = %d, want 0", n)
	}
	// Only the owner may decide.
	if _, err := s.DecideQuotaRequest(ctx, QuotaDecision{RequestID: req.RequestID, OwnerUserID: "bob", Status: QuotaRequestApproved, AtMs: 200}); !errors.Is(err, ErrQuotaRequestNotFound) {
		t.Fatalf("decide by requester err = %v, want not found", err)
	}
	dec, err := s.DecideQuotaRequest(ctx, QuotaDecision{RequestID: req.RequestID, OwnerUserID: "alice", Status: QuotaRequestApproved, GrantedRuns: intp(3), AtMs: 200})
	if err != nil || dec.Status != QuotaRequestApproved || dec.GrantedRuns == nil || *dec.GrantedRuns != 3 || dec.DecidedBy != "alice" {
		t.Fatalf("approve = %+v, %v", dec, err)
	}
	if _, err := s.DecideQuotaRequest(ctx, QuotaDecision{RequestID: req.RequestID, OwnerUserID: "alice", Status: QuotaRequestDeclined, AtMs: 300}); !errors.Is(err, ErrQuotaRequestDecided) {
		t.Fatalf("decide twice err = %v, want decided", err)
	}
	// Compensation returns it to pending, then a decline sticks.
	if err := s.ReopenQuotaRequest(ctx, req.RequestID); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if r, _ := s.GetQuotaRequest(ctx, req.RequestID); r.Status != QuotaRequestPending || r.GrantedRuns != nil {
		t.Fatalf("reopened = %+v", r)
	}
	if _, err := s.DecideQuotaRequest(ctx, QuotaDecision{RequestID: req.RequestID, OwnerUserID: "alice", Status: QuotaRequestDeclined, AtMs: 400}); err != nil {
		t.Fatalf("decline: %v", err)
	}
	// A decided request frees the slot for a new one.
	if _, err := s.CreateQuotaRequest(ctx, dup); err != nil {
		t.Fatalf("create after decision: %v", err)
	}
	// A request's kind round-trips (more runs when it names none).
	if r, _ := s.GetQuotaRequest(ctx, dup.RequestID); r.Kind != QuotaRequestMoreRuns {
		t.Fatalf("kind = %q, want quota", r.Kind)
	}
	access := QuotaRequest{RequestID: "qr-3-" + room, RoomID: room, OwnerUserID: "dave", RequesterUserID: "carol",
		Provider: "core:codex", Kind: QuotaRequestAccess, CreatedAtMs: 500}
	if _, err := s.CreateQuotaRequest(ctx, access); err != nil {
		t.Fatalf("create access: %v", err)
	}
	if r, _ := s.GetQuotaRequest(ctx, access.RequestID); r.Kind != QuotaRequestAccess {
		t.Fatalf("access kind = %q", r.Kind)
	}
	// Only an approved request reopens; a taken id is refused.
	if err := s.ReopenQuotaRequest(ctx, dup.RequestID); !errors.Is(err, ErrQuotaRequestDecided) {
		t.Fatalf("reopen of a pending request = %v, want ErrQuotaRequestDecided", err)
	}
	if err := s.ReopenQuotaRequest(ctx, req.RequestID); !errors.Is(err, ErrQuotaRequestDecided) {
		t.Fatalf("reopen of a declined request = %v, want ErrQuotaRequestDecided", err)
	}
	if err := s.ReopenQuotaRequest(ctx, "qr-none-"+room); !errors.Is(err, ErrQuotaRequestNotFound) {
		t.Fatalf("reopen of an unknown request = %v, want ErrQuotaRequestNotFound", err)
	}
	taken := req
	taken.Provider = "core:claude"
	if _, err := s.CreateQuotaRequest(ctx, taken); !errors.Is(err, ErrQuotaRequestExists) {
		t.Fatalf("create under a taken id = %v, want ErrQuotaRequestExists", err)
	}
	// Newest first with the id breaking ties; the limit bounds the page.
	for _, id := range []string{"qr-t1-", "qr-t2-"} {
		r := req
		r.RequestID, r.Provider, r.CreatedAtMs = id+room, "core:"+id, 900
		if _, err := s.CreateQuotaRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list := must(s.ListQuotaRequests(ctx, QuotaRequestQuery{OwnerUserID: "alice"}))(t)
	if len(list) != 4 || list[0].RequestID != "qr-t2-"+room || list[1].RequestID != "qr-t1-"+room || list[3].RequestID != req.RequestID {
		t.Fatalf("order = %+v", list)
	}
	if page := must(s.ListQuotaRequests(ctx, QuotaRequestQuery{OwnerUserID: "alice", Limit: 1}))(t); len(page) != 1 || page[0].RequestID != "qr-t2-"+room {
		t.Fatalf("limit 1 = %+v", page)
	}
	if page := must(s.ListQuotaRequests(ctx, QuotaRequestQuery{OwnerUserID: "alice", Limit: 1 << 20}))(t); len(page) != 4 {
		t.Fatalf("limit beyond the cap = %d rows, want 4", len(page))
	}
	if none := must(s.ListQuotaRequests(tenantCtx(tnt+"-other"), QuotaRequestQuery{}))(t); none == nil || len(none) != 0 {
		t.Fatalf("empty list = %#v, want []", none)
	}
}

func TestContractBorrowUsageStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		borrowUsageCases(t, b.Grant, uniq("t-borrow-"), uniq("room-b-"))
	})
}

func borrowUsageCases(t *testing.T, g GrantStore, tnt, room string) {
	t.Helper()
	ctx := tenantCtx(tnt)
	gid := "g-" + room
	if err := g.PutGrant(ctx, Grant{GrantID: gid, RoomID: room, TargetID: "shared-agent:codex", OwnerUserID: "alice",
		GranteeUserID: "*", MaxConcurrent: 1, RunQuota: intp(2), CreatedAtMs: 1}); err != nil {
		t.Fatalf("PutGrant: %v", err)
	}
	cost := 0.5
	inTok, outTok := int64(1200), int64(34)
	for _, rec := range []AuditRecord{
		{RoomID: room, GrantID: gid, ActorUserID: "bob", OwnerUserID: "alice", Action: "turn", Decision: "allow", AtMs: 1000, Cost: &cost,
			InputTokens: &inTok, OutputTokens: &outTok, Model: "m-1"},
		{RoomID: room, GrantID: gid, ActorUserID: "jack", OwnerUserID: "alice", Action: "turn", Decision: "allow", AtMs: 2000},
		{RoomID: room, GrantID: gid, ActorUserID: "alice", OwnerUserID: "alice", Action: "turn", Decision: "allow", AtMs: 1500}, // owner's own turn
		{RoomID: room, GrantID: gid, ActorUserID: "bob", OwnerUserID: "alice", Action: "turn", Decision: "deny", DenyReason: "quota_exhausted", AtMs: 1600},
		{RoomID: room, GrantID: gid, ActorUserID: "bob", OwnerUserID: "alice", Action: "start", Decision: "allow", AtMs: 1700},
		{RoomID: room, GrantID: gid, ActorUserID: "bob", OwnerUserID: "alice", Action: "turn", Decision: "allow", AtMs: 9000}, // out of range
	} {
		if err := g.AppendAudit(ctx, rec); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	turns := must(g.BorrowTurns(ctx, BorrowTurnQuery{FromMs: 0, ToMs: 5000, OwnerUserID: "alice"}))(t)
	if len(turns) != 2 || turns[0].ActorUserID != "bob" || turns[1].ActorUserID != "jack" {
		t.Fatalf("owner turns = %+v, want bob then jack", turns)
	}
	if turns[0].Cost == nil || *turns[0].Cost != 0.5 || turns[1].Cost != nil {
		t.Fatalf("costs = %v / %v, want 0.5 and absent", turns[0].Cost, turns[1].Cost)
	}
	// G2: token usage round-trips; an unreported turn stays absent.
	if turns[0].InputTokens == nil || *turns[0].InputTokens != 1200 || turns[0].OutputTokens == nil || *turns[0].OutputTokens != 34 ||
		turns[0].Model != "m-1" || turns[1].InputTokens != nil || turns[1].OutputTokens != nil {
		t.Fatalf("tokens = %+v / %+v, want 1200/34 (m-1) and absent", turns[0], turns[1])
	}
	if n := len(must(g.BorrowTurns(ctx, BorrowTurnQuery{FromMs: 0, ToMs: 5000, BorrowerUserID: "bob"}))(t)); n != 1 {
		t.Fatalf("bob turns = %d, want 1", n)
	}
	if other := must(g.BorrowTurns(tenantCtx(tnt+"-other"), BorrowTurnQuery{FromMs: 0, ToMs: 5000}))(t); other == nil || len(other) != 0 {
		t.Fatalf("cross-tenant turns = %#v, want []", other)
	}
	// Turns of one millisecond keep their append order (the row id).
	for _, actor := range []string{"zed-1", "zed-2", "zed-3"} {
		if err := g.AppendAudit(ctx, AuditRecord{RoomID: room, GrantID: gid, ActorUserID: actor, OwnerUserID: "alice", Action: "turn", Decision: "allow", AtMs: 4000}); err != nil {
			t.Fatal(err)
		}
	}
	tied := must(g.BorrowTurns(ctx, BorrowTurnQuery{FromMs: 4000, ToMs: 4001}))(t)
	if len(tied) != 3 || tied[0].ActorUserID != "zed-1" || tied[2].ActorUserID != "zed-3" {
		t.Fatalf("tied turns = %+v, want append order", tied)
	}

	// Raising keeps usage; unlimited and revoked grants refuse.
	if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: "rsv-" + gid, GrantID: gid, RoomID: room, OwnerUserID: "alice", BorrowerUserID: "bob", AtMs: 5}); err != nil {
		t.Fatalf("ReserveRun: %v", err)
	}
	raised, err := g.RaiseRunQuota(ctx, gid, 3, 5)
	if err != nil || raised.RunQuota == nil || *raised.RunQuota != 5 || raised.RunsUsed != 1 {
		t.Fatalf("raise = %+v, %v; want quota 5 runsUsed 1", raised, err)
	}
	if _, err := g.RaiseRunQuota(ctx, "missing-"+gid, 1, 5); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("raise missing err = %v", err)
	}
	unlimited := "u-" + room
	_ = g.PutGrant(ctx, Grant{GrantID: unlimited, RoomID: room, TargetID: "shared-agent:claude", OwnerUserID: "alice", GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1})
	if _, err := g.RaiseRunQuota(ctx, unlimited, 1, 5); !errors.Is(err, ErrRunQuotaUnlimited) {
		t.Fatalf("raise unlimited err = %v", err)
	}
	_ = g.RevokeGrant(ctx, gid, 10)
	if _, err := g.RaiseRunQuota(ctx, gid, 1, 5); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("raise revoked err = %v", err)
	}
}

func TestTeamAgentsOnBothBackends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		teamAgentCases(t, b.BridgeJobs, b.TeamAgents(), uniq("t-team-"), uniq(""))
	})
}

func teamAgentCases(t *testing.T, jobs BridgeJobStore, team TeamAgentReader, tnt, sfx string) {
	t.Helper()
	if team == nil {
		t.Fatal("bridge-job store has no TeamAgentReader")
	}
	ctx := tenantCtx(tnt)
	now := time.Now().UnixMilli()
	alice, bob := "alice-"+sfx, "bob-"+sfx
	live, stale := "bridge-live-"+sfx, "bridge-stale-"+sfx
	if err := jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: live, OwnerUserID: alice, Status: BridgeOnline, LastSeenAtMs: now}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := jobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: stale, OwnerUserID: bob, Status: BridgeOnline, LastSeenAtMs: now - 10*BridgeOfflineAfterMs}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	_ = jobs.ReplaceBridgeAgents(ctx, live, []BridgeAgent{{OwnerUserID: alice, Core: "c1", Provider: "codex", TargetID: "c1/local:codex", LastSeenAtMs: now}})
	_ = jobs.ReplaceBridgeAgents(ctx, stale, []BridgeAgent{{OwnerUserID: bob, Core: "c1", Provider: "claude", TargetID: "c1/local:claude", LastSeenAtMs: now}})

	agents := must(team.TenantTeamAgents(ctx, "", now))(t)
	if len(agents) != 2 {
		t.Fatalf("tenant agents = %+v, want 2", agents)
	}
	for _, a := range agents {
		if (a.OwnerUserID == alice) != a.Online {
			t.Fatalf("agent %+v: online should be true only for the live bridge", a)
		}
	}
	if own := must(team.TenantTeamAgents(ctx, bob, now))(t); len(own) != 1 || own[0].OwnerUserID != bob || own[0].LastSeenAtMs != now-10*BridgeOfflineAfterMs {
		t.Fatalf("bob's agents = %+v", own)
	}
	if other := must(team.TenantTeamAgents(tenantCtx(tnt+"-other"), "", now))(t); len(other) != 0 {
		t.Fatalf("cross-tenant agents = %+v", other)
	}

	since := now - 60_000
	mk := func(id string, created int64, owner string) AgentJob {
		return AgentJob{JobID: id + "-" + sfx, RoomID: "room-" + sfx, MessageSeq: created, RequesterUserID: bob, OwnerUserID: owner,
			Core: "c1", Provider: "codex", CreatedAtMs: created, UpdatedAtMs: created}
	}
	for _, j := range []AgentJob{mk("old-queued", since-3_600_000, alice), mk("old-done", since-3_600_000+1, alice), mk("recent", now-1000, alice), mk("bobs", now-500, bob)} {
		if _, err := jobs.PutAgentJob(ctx, j); err != nil {
			t.Fatalf("PutAgentJob: %v", err)
		}
	}
	// Move old-done to a terminal state through the worker path.
	claimed, _ := jobs.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: live, OwnerUserID: alice, MaxJobs: 10, NowMs: now, LeaseMs: 60_000})
	for _, j := range claimed {
		if j.JobID == "old-done-"+sfx {
			if _, err := jobs.UpdateAgentJobStatus(ctx, JobStatusUpdate{JobID: j.JobID, BridgeID: live, Status: AgentJobReplied, NowMs: now}); err != nil {
				t.Fatalf("complete: %v", err)
			}
		}
	}
	got := map[string]bool{}
	for _, j := range must(team.TenantTeamJobs(ctx, alice, since))(t) {
		got[j.JobID] = true
	}
	if !got["old-queued-"+sfx] || !got["recent-"+sfx] || got["old-done-"+sfx] || got["bobs-"+sfx] {
		t.Fatalf("alice's jobs = %v, want the still-active old job and the recent one only", got)
	}
}

func TestAgentUsageOnBothBackends(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		agentUsageCases(t, b.Activity, b.AgentUsage(), uniq("t-usage-"), uniq(""))
	})
}

func agentUsageCases(t *testing.T, act ActivityStore, usage AgentUsageReader, tnt, sfx string) {
	t.Helper()
	if usage == nil {
		t.Fatal("activity store has no AgentUsageReader")
	}
	ctx := tenantCtx(tnt)
	room := "room-u-" + sfx
	reply := func(seq int64, owner string, metrics map[string]any, at int64) wire.Message {
		p := map[string]any{"role": "assistant", "text": "ok", "ownerUserId": owner, "provider": "codex"}
		if metrics != nil {
			p["metrics"] = metrics
		}
		return wire.Message{Seq: seq, Payload: p, CreatedAtUnixMs: at}
	}
	in := wire.ReportMessagesInput{WorkspaceID: room, AgentSessionID: "s-" + sfx, Messages: []wire.Message{
		reply(1, "alice", map[string]any{"inputTokens": 100.0, "outputTokens": 10.0, "costUsd": 0.25, "reported": true}, 5000),
		reply(2, "alice", map[string]any{"inputTokens": 50.0, "outputTokens": 5.0, "reported": true}, 6000),
		reply(3, "alice", map[string]any{"inputTokens": 999.0, "reported": false}, 6000), // not reported
		reply(4, "alice", nil, 6000), // no metrics
		reply(5, "alice", map[string]any{"inputTokens": 7.0, "costUsd": 1.0, "reported": true}, 100), // before range
		reply(6, "bob", map[string]any{"inputTokens": 1.0, "outputTokens": 1.0, "reported": true}, 6000),
		// A negative report is a reported turn that burns nothing: it can
		// never lower the owner's (or the organization's) budget burn.
		reply(7, "alice", map[string]any{"inputTokens": -1000.0, "outputTokens": -50.0, "costUsd": -5.0, "reported": true}, 6000),
		reply(8, "alice", map[string]any{}, 6000),                                     // empty metrics: not reported
		reply(9, "Alice", map[string]any{"inputTokens": 3.0, "reported": true}, 6000), // another subject
	}}
	if _, _, err := act.AppendMessages(ctx, room, in); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}
	rows := must(usage.AgentUsage(ctx, "alice", 1000))(t)
	if len(rows) != 1 {
		t.Fatalf("alice usage = %+v, want one row", rows)
	}
	u := rows[0]
	if u.Provider != "codex" || u.ReportedTurns != 3 || u.InputTokens != 150 || u.OutputTokens != 15 || u.CostUsd != 0.25 || u.CostReportedBy != 1 {
		t.Fatalf("alice usage = %+v", u)
	}
	if rows := must(usage.AgentUsage(ctx, "Alice", 1000))(t); len(rows) != 1 || rows[0].InputTokens != 3 || rows[0].OwnerUserID != "Alice" {
		t.Fatalf("Alice usage = %+v, want only her own turn (subjects are case-sensitive)", rows)
	}
	if all := must(usage.AgentUsage(ctx, "", 1000))(t); len(all) != 3 {
		t.Fatalf("tenant usage = %+v, want alice + Alice + bob", all)
	}
	if other := must(usage.AgentUsage(tenantCtx(tnt+"-other"), "", 0))(t); len(other) != 0 {
		t.Fatalf("cross-tenant usage = %+v", other)
	}
}
