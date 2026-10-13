package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/testmysql"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The cross-backend contract cases: every case runs against the memory
// backends and, when BNBLOC_TEST_MYSQL_DSN is set, the MySQL backends. A
// behaviour only one backend has is a bug in the other.

var (
	mysqlBackendsOnce sync.Once
	mysqlBackends     *Backends
	mysqlBackendsErr  error
)

// forEachBackend runs fn as a subtest per backend: fresh memory backends,
// and the MySQL backends when BNBLOC_TEST_MYSQL_DSN is set. The MySQL
// backends are opened once per test binary, in the binary's own database
// (testmysql.Shared: schema + migrations run once, no other package shares
// it); callers keep their ids unique (uniq) because the package's tests
// share that database.
func forEachBackend(t *testing.T, fn func(t *testing.T, b *Backends)) {
	t.Helper()
	mem, err := NewBackends("")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("memory", func(t *testing.T) { fn(t, mem) })
	if my := sharedMySQL(t); my != nil {
		t.Run("mysql", func(t *testing.T) { fn(t, my) })
	}
}

// sharedMySQL is the test binary's MySQL backends, or nil when
// BNBLOC_TEST_MYSQL_DSN is unset.
func sharedMySQL(t *testing.T) *Backends {
	t.Helper()
	mysqlBackendsOnce.Do(func() {
		dsn, err := testmysql.Shared()
		if err != nil || dsn == "" {
			mysqlBackendsErr = err
			return
		}
		mysqlBackends, mysqlBackendsErr = NewBackends(dsn)
	})
	if mysqlBackendsErr != nil {
		t.Fatalf("mysql backends: %v", mysqlBackendsErr)
	}
	return mysqlBackends
}

// newContractRoom creates a fresh room owned by owner in the ctx tenant.
func newContractRoom(t *testing.T, ctx context.Context, b *Backends, owner string) string {
	t.Helper()
	id := uniq("room-")
	if err := b.Room.CreateRoomWithOwner(ctx, Room{RoomID: id, Name: "r", CreatedBy: owner, CreatedAtMs: 1},
		Membership{RoomID: id, UserID: owner, Role: RoleOwner, JoinedAtMs: 1}); err != nil {
		t.Fatalf("create room: %v", err)
	}
	return id
}

// An update that writes the values a row already holds matched the row: it
// succeeds on both backends (MySQL used to report it as "not found").
func TestContractIdenticalUpdateSucceeds(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-same-")
		ctx := tenantCtx(tid)
		room := newContractRoom(t, ctx, b, "maya")

		name := "same"
		for i := 0; i < 2; i++ {
			if err := b.Room.UpdateRoom(ctx, room, RoomPatch{Name: &name}); err != nil {
				t.Fatalf("UpdateRoom #%d: %v", i, err)
			}
		}
		ch := RoomChannel{ChannelID: uniq("ch-"), RoomID: room, Name: "c", CreatedBy: "maya", CreatedAtMs: 1}
		if err := b.Channel.PutChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.Channel.RenameChannel(ctx, room, ch.ChannelID, "renamed"); err != nil {
				t.Fatalf("RenameChannel #%d: %v", i, err)
			}
		}
		if err := b.Tenant.PutTenant(ctx, Tenant{TenantID: tid, Name: "n"}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.Tenant.SetQuota(ctx, tid, 5, 1.5); err != nil {
				t.Fatalf("SetQuota #%d: %v", i, err)
			}
		}
		on := true
		for i := 0; i < 2; i++ {
			if _, err := b.Tenant.SetTenantPolicy(ctx, tid, TenantPolicyUpdate{AnonymousLinksDisabled: &on}); err != nil {
				t.Fatalf("SetTenantPolicy #%d: %v", i, err)
			}
		}
		gid := uniq("g-")
		if err := b.Grant.PutGrant(ctx, Grant{GrantID: gid, RoomID: room, TargetID: "shared-agent:x", OwnerUserID: "maya", GranteeUserID: "*", MaxConcurrent: 1, CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.Grant.RevokeGrant(ctx, gid, 7); err != nil {
				t.Fatalf("RevokeGrant #%d: %v", i, err)
			}
		}
		sched, err := b.Schedules.CreateSchedule(ctx, sampleSchedule(uniq("s-"), room, "maya", 0))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := b.Schedules.UpdateSchedule(ctx, sched); err != nil {
				t.Fatalf("UpdateSchedule #%d: %v", i, err)
			}
		}

		// A real miss still reports not found.
		if err := b.Room.UpdateRoom(ctx, uniq("nope-"), RoomPatch{Name: &name}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("UpdateRoom on a missing room = %v, want ErrRoomNotFound", err)
		}
		if err := b.Tenant.SetQuota(ctx, uniq("nope-"), 1, 1); !errors.Is(err, ErrTenantNotFound) {
			t.Fatalf("SetQuota on a missing tenant = %v, want ErrTenantNotFound", err)
		}
		missing := sched
		missing.ScheduleID = uniq("nope-")
		if err := b.Schedules.UpdateSchedule(ctx, missing); !errors.Is(err, ErrScheduleNotFound) {
			t.Fatalf("UpdateSchedule on a missing schedule = %v, want ErrScheduleNotFound", err)
		}
	})
}

// A caller-chosen id never reaches a row of another room or tenant: keys that
// carry no tenant are written insert-only.
func TestContractRePutIntoAnotherRoomOrTenantIsRefused(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ca, cb := tenantCtx(uniq("t-a-")), tenantCtx(uniq("t-b-"))
		roomA := newContractRoom(t, ca, b, "maya")
		roomA2 := newContractRoom(t, ca, b, "maya")
		roomB := newContractRoom(t, cb, b, "jack")

		// Artifacts: the id is the whole key.
		artID := uniq("art-")
		art, err := b.Artifacts.PutArtifact(ca, Artifact{ArtifactID: artID, RoomID: roomA, Kind: "markdown", Title: "first", CreatedBy: "maya"})
		if err != nil {
			t.Fatal(err)
		}
		again, err := b.Artifacts.PutArtifact(ca, Artifact{ArtifactID: artID, RoomID: roomA, Kind: "markdown", Title: "second", CreatedBy: "maya"})
		if err != nil || again.Title != art.Title {
			t.Fatalf("idempotent re-put = %+v, %v; want the stored artifact unchanged", again, err)
		}
		if _, err := b.Artifacts.PutArtifact(ca, Artifact{ArtifactID: artID, RoomID: roomA2, Kind: "markdown", Title: "hijack"}); !errors.Is(err, ErrArtifactIDTaken) {
			t.Fatalf("re-put into another room = %v, want ErrArtifactIDTaken", err)
		}
		if _, err := b.Artifacts.PutArtifact(cb, Artifact{ArtifactID: artID, RoomID: roomB, Kind: "markdown", Title: "hijack"}); !errors.Is(err, ErrArtifactIDTaken) {
			t.Fatalf("re-put from another tenant = %v, want ErrArtifactIDTaken", err)
		}
		if got, err := b.Artifacts.GetArtifact(ca, artID); err != nil || got.Title != "first" || got.RoomID != roomA {
			t.Fatalf("stored artifact after refused re-puts = %+v, %v", got, err)
		}

		// Room members: (room_id, user_id) carries no tenant.
		if err := b.Room.AddMember(cb, Membership{RoomID: roomA, UserID: "mallory", Role: RoleOwner, JoinedAtMs: 2}); !errors.Is(err, ErrRoomNotFound) {
			t.Fatalf("AddMember into another tenant's room = %v, want ErrRoomNotFound", err)
		}
		if _, ok := must2(b.Room.GetMember(ca, roomA, "mallory"))(t); ok {
			t.Fatal("a refused AddMember left a membership behind")
		}
		// A re-add changes the role only; the join time stays.
		if err := b.Room.AddMember(ca, Membership{RoomID: roomA, UserID: "jack", Role: RoleMember, JoinedAtMs: 10}); err != nil {
			t.Fatal(err)
		}
		if err := b.Room.AddMember(ca, Membership{RoomID: roomA, UserID: "jack", Role: RoleVisitor, JoinedAtMs: 20}); err != nil {
			t.Fatal(err)
		}
		if m, _ := must2(b.Room.GetMember(ca, roomA, "jack"))(t); m.Role != RoleVisitor || m.JoinedAtMs != 10 {
			t.Fatalf("re-added member = %+v, want visitor joined at 10", m)
		}

		// Channels: channel_id is the whole key.
		ch := RoomChannel{ChannelID: uniq("ch-"), RoomID: roomA, Name: "a", CreatedBy: "maya", CreatedAtMs: 1}
		if err := b.Channel.PutChannel(ca, ch); err != nil {
			t.Fatal(err)
		}
		hijack := RoomChannel{ChannelID: ch.ChannelID, RoomID: roomB, Name: "hijack", CreatedBy: "jack", CreatedAtMs: 2}
		if err := b.Channel.PutChannel(cb, hijack); !errors.Is(err, ErrChannelExists) {
			t.Fatalf("PutChannel with another tenant's id = %v, want ErrChannelExists", err)
		}
		if got, ok := must2(b.Channel.GetChannel(ca, roomA, ch.ChannelID))(t); !ok || got.Name != "a" {
			t.Fatalf("channel after refused put = %+v %v", got, ok)
		}
	})
}

// EnsureMember is insert-only: an existing membership keeps its role.
func TestContractEnsureMemberNeverChangesARole(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-ens-")
		ctx := tenantCtx(tid)
		got, created, err := b.Tenant.EnsureMember(ctx, TenantMember{TenantID: tid, UserID: "maya", Role: OrgRoleAdmin, JoinedAtMs: 1})
		if err != nil || !created || got.Role != OrgRoleAdmin {
			t.Fatalf("first EnsureMember = %+v %v %v", got, created, err)
		}
		got, created, err = b.Tenant.EnsureMember(ctx, TenantMember{TenantID: tid, UserID: "maya", Role: OrgRoleMember, JoinedAtMs: 2})
		if err != nil || created || got.Role != OrgRoleAdmin || got.JoinedAtMs != 1 {
			t.Fatalf("second EnsureMember = %+v %v %v, want the stored admin row", got, created, err)
		}
		if n, err := b.Tenant.MemberCount(ctx, tid); err != nil || n != 1 {
			t.Fatalf("MemberCount = %d, %v", n, err)
		}
	})
}

// Subjects are case-sensitive (and accent-sensitive): a case or accent
// variant is another subject on both backends. MySQL's default collation
// used to fold them, so "Maya" could edit "maya"'s message there and not in
// memory.
func TestContractCaseVariantIdsAreDistinct(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		tid := uniq("t-case-")
		ctx := tenantCtx(tid)
		room := newContractRoom(t, ctx, b, "maya")
		if must(b.Room.IsMember(ctx, room, "Maya"))(t) {
			t.Fatal("Maya is a member of maya's room")
		}
		if err := b.Room.AddMember(ctx, Membership{RoomID: room, UserID: "Maya", Role: RoleVisitor, JoinedAtMs: 2}); err != nil {
			t.Fatal(err)
		}
		if m, _ := must2(b.Room.GetMember(ctx, room, "maya"))(t); m.Role != RoleOwner {
			t.Fatalf("adding Maya changed maya's membership: %+v", m)
		}
		if n := must(b.Room.MemberCount(ctx, room))(t); n != 2 {
			t.Fatalf("MemberCount = %d, want 2 (maya and Maya)", n)
		}

		if err := b.Chat.AppendRoomMessage(ctx, RoomMessage{TenantID: tid, RoomID: room, Seq: 1, Kind: "chat_message", Author: "josé",
			Payload: json.RawMessage(`{"text":"hola"}`), CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		for _, other := range []string{"José", "jose"} {
			if ok, err := b.Chat.EditRoomMessage(ctx, room, 1, other, "hijack", 2); err != nil || ok {
				t.Fatalf("edit by %q = %v, %v; want refused (not the author)", other, ok, err)
			}
			if ok, err := b.Chat.DeleteRoomMessage(ctx, room, 1, other, 2); err != nil || ok {
				t.Fatalf("delete by %q = %v, %v; want refused (not the author)", other, ok, err)
			}
		}
		if ok, err := b.Chat.EditRoomMessage(ctx, room, 1, "josé", "edited", 3); err != nil || !ok {
			t.Fatalf("edit by the author = %v, %v", ok, err)
		}

		if err := b.Tenant.PutMember(ctx, TenantMember{TenantID: tid, UserID: "maya", Role: OrgRoleAdmin, JoinedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := b.Tenant.GetMember(ctx, tid, "MAYA"); err != nil || ok {
			t.Fatalf("GetMember(MAYA) = %v, %v; want not a member", ok, err)
		}
		if _, created, err := b.Tenant.EnsureMember(ctx, TenantMember{TenantID: tid, UserID: "Maya", Role: OrgRoleMember, JoinedAtMs: 2}); err != nil || !created {
			t.Fatalf("EnsureMember(Maya) = %v, %v; want a new membership", created, err)
		}
		if m, _, _ := b.Tenant.GetMember(ctx, tid, "maya"); m.Role != OrgRoleAdmin {
			t.Fatalf("maya's org role = %q, want org_admin kept", m.Role)
		}
	})
}

// A guarded job write answers with the same sentinel on both backends: an
// unknown job is ErrAgentJobNotFound, a job another bridge holds (or one
// already terminal) is ErrAgentJobLeaseMismatch.
func TestContractJobWriteSentinels(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-job-"))
		owner, bridge, room := uniq("o-"), uniq("br-"), uniq("room-")
		job, err := b.BridgeJobs.PutAgentJob(ctx, AgentJob{JobID: uniq("job-"), RoomID: room, MessageSeq: 1, RequesterUserID: owner, OwnerUserID: owner,
			Core: "c", Provider: "p", Status: AgentJobQueued, CreatedAtMs: 1, UpdatedAtMs: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.BridgeJobs.PutBridgeHeartbeat(ctx, BridgeHeartbeat{BridgeID: bridge, OwnerUserID: owner, Status: BridgeOnline, LastSeenAtMs: 10}); err != nil {
			t.Fatal(err)
		}
		if err := b.BridgeJobs.ReplaceBridgeAgents(ctx, bridge, []BridgeAgent{{BridgeID: bridge, OwnerUserID: owner, Core: "c", Provider: "p", TargetID: "c/p", LastSeenAtMs: 10}}); err != nil {
			t.Fatal(err)
		}
		if got, err := b.BridgeJobs.ClaimAgentJobs(ctx, ClaimRequest{BridgeID: bridge, OwnerUserID: owner, MaxJobs: 1, NowMs: 20, LeaseMs: 60000}); err != nil || len(got) != 1 {
			t.Fatalf("claim = %d jobs, %v", len(got), err)
		}
		missing := uniq("nope-")
		if _, err := b.BridgeJobs.UpdateAgentJobStatus(ctx, JobStatusUpdate{JobID: missing, BridgeID: bridge, Status: AgentJobRunning, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("update of an unknown job = %v, want ErrAgentJobNotFound", err)
		}
		if err := b.BridgeJobs.RenewAgentJobLease(ctx, LeaseRenewal{JobID: missing, BridgeID: bridge, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("renew of an unknown job = %v, want ErrAgentJobNotFound", err)
		}
		if _, err := b.BridgeJobs.CancelAgentJob(ctx, CancelRequest{JobID: missing, RoomID: room, NowMs: 30}); !errors.Is(err, ErrAgentJobNotFound) {
			t.Fatalf("cancel of an unknown job = %v, want ErrAgentJobNotFound", err)
		}
		if _, err := b.BridgeJobs.UpdateAgentJobStatus(ctx, JobStatusUpdate{JobID: job.JobID, BridgeID: "other", Status: AgentJobRunning, NowMs: 30}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("update by another bridge = %v, want ErrAgentJobLeaseMismatch", err)
		}
		// A renewal in the same millisecond rewrites the same lease: it matched.
		for i := 0; i < 2; i++ {
			if err := b.BridgeJobs.RenewAgentJobLease(ctx, LeaseRenewal{JobID: job.JobID, BridgeID: bridge, NowMs: 40, LeaseMs: 1000}); err != nil {
				t.Fatalf("renew #%d in the same millisecond: %v", i, err)
			}
		}
		if _, err := b.BridgeJobs.CancelAgentJob(ctx, CancelRequest{JobID: job.JobID, RoomID: room, NowMs: 50}); err != nil {
			t.Fatal(err)
		}
		if _, err := b.BridgeJobs.CancelAgentJob(ctx, CancelRequest{JobID: job.JobID, RoomID: room, NowMs: 60}); !errors.Is(err, ErrAgentJobLeaseMismatch) {
			t.Fatalf("cancel of a terminal job = %v, want ErrAgentJobLeaseMismatch", err)
		}
	})
}

// Two reporters racing on one session publish each reply once: the reply is
// fresh only for the append that stored it.
func TestContractConcurrentAppendPublishesOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		ctx := tenantCtx(uniq("t-race-"))
		room, session := uniq("room-"), uniq("s-")
		batch := func(seqs ...int64) wire.ReportMessagesInput {
			in := wire.ReportMessagesInput{WorkspaceID: room, AgentSessionID: session}
			for _, s := range seqs {
				in.Messages = append(in.Messages, wire.Message{Seq: s, Payload: map[string]any{"role": "assistant", "text": "r"}, CreatedAtUnixMs: 1})
			}
			return in
		}
		const reporters = 6
		var wg sync.WaitGroup
		published := make(chan int64, reporters*3)
		errs := make(chan error, reporters)
		for i := 0; i < reporters; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fresh, _, err := b.Activity.AppendMessages(ctx, room, batch(1, 2, 3))
				if err != nil {
					errs <- err
					return
				}
				for _, m := range fresh {
					published <- m.Seq
				}
			}()
		}
		wg.Wait()
		close(published)
		close(errs)
		for err := range errs {
			t.Fatalf("AppendMessages: %v", err)
		}
		count := map[int64]int{}
		for s := range published {
			count[s]++
		}
		for _, s := range []int64{1, 2, 3} {
			if count[s] != 1 {
				t.Fatalf("seq %d published %d times, want once (all: %v)", s, count[s], count)
			}
		}
		// A duplicate inside one report is fresh once.
		fresh, cursor, err := b.Activity.AppendMessages(ctx, room, batch(4, 4))
		if err != nil || len(fresh) != 1 || cursor != 4 {
			t.Fatalf("duplicate in one report = %d fresh, cursor %d, %v; want 1, 4", len(fresh), cursor, err)
		}
	})
}
