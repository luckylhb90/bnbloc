package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// exerciseExpertUsage checks per-expert runs and distinct requesters, in
// the ctx tenant only.
func exerciseExpertUsage(t *testing.T, jobs interface {
	BridgeJobStore
	ExpertUsageReader
}, suffix string) {
	t.Helper()
	ctx := testCtx()
	now := time.Now().UnixMilli()
	reviewer, writer, unused := "cc-rev-"+suffix, "cc-wri-"+suffix, "cc-none-"+suffix
	put := func(i int, expert, requester string) {
		j := AgentJob{JobID: "job-usage-" + suffix + "-" + itoaBase(int64(i)), RoomID: "room-usage-" + suffix, MessageSeq: int64(i + 1),
			RequesterUserID: requester, OwnerUserID: "jack", Core: "acme", Provider: "codex", CreatedAtMs: now + int64(i), ExpertID: expert, ExpertVersion: "1.0.0"}
		if _, err := jobs.PutAgentJob(ctx, j); err != nil {
			t.Fatalf("put job: %v", err)
		}
	}
	put(0, reviewer, "maya")
	put(1, reviewer, "maya")
	put(2, reviewer, "bob")
	put(3, writer, "maya")
	put(4, "", "maya")
	other := AgentJob{JobID: "job-usage-other-" + suffix, RoomID: "room-usage-other-" + suffix, MessageSeq: 1, RequesterUserID: "zed", OwnerUserID: "zed", Provider: "codex", CreatedAtMs: now, ExpertID: reviewer}
	if _, err := jobs.PutAgentJob(tenantCtx("other-"+suffix), other); err != nil {
		t.Fatalf("put other tenant job: %v", err)
	}
	got, err := jobs.ExpertUsage(ctx, []string{reviewer, writer, unused, reviewer})
	if err != nil {
		t.Fatalf("ExpertUsage: %v", err)
	}
	if got[reviewer] != (ContentUsage{Runs: 3, Members: 2}) || got[writer] != (ContentUsage{Runs: 1, Members: 1}) {
		t.Fatalf("usage = %+v", got)
	}
	if _, ok := got[unused]; ok {
		t.Fatalf("unused expert counted: %+v", got[unused])
	}
}

func TestMemoryExpertUsage(t *testing.T) {
	exerciseExpertUsage(t, NewMemoryBridgeJobs(), "mem")
}

func TestMySQLExpertUsage(t *testing.T) {
	m, done := mysqlJobs(t)
	defer done()
	exerciseExpertUsage(t, m, itoaBase(time.Now().UnixNano()))
}

// exerciseMemberUses checks member-scope use counts and that a room expert
// is not counted as a second use of the room.
func exerciseMemberUses(t *testing.T, s ContentComponentStore, prefix string) {
	t.Helper()
	ctx := context.Background()
	pack, expert := prefix+"-pack", prefix+"-expert"
	for _, c := range []ContentComponent{
		{ID: pack, Kind: "bnbloc.skill.package/v1", Name: "kit"},
		{ID: expert, Kind: "bnbloc.expert.package/v1", Name: "reviewer"},
	} {
		c.TenantID, c.Owner, c.Scope, c.Status = prefix+"-t", "maya", ContentScopePersonal, ContentStatusActive
		if err := s.CreateContentComponent(ctx, c, contentVersion(c.ID, "1.0.0", "sha256/"+strings.Repeat("c", 64), 1)); err != nil {
			t.Fatalf("create %s: %v", c.ID, err)
		}
	}
	for _, u := range []ContentUse{
		{ScopeKind: ContentUseMember, ScopeID: "maya", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseMember, ScopeID: "bob", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseRoom, ScopeID: "room-1", ComponentID: pack, Kind: "bnbloc.skill.package/v1"},
		{ScopeKind: ContentUseRoom, ScopeID: "room-1", ComponentID: expert, Kind: "bnbloc.expert.package/v1"},
		{ScopeKind: ContentUseRoomExpert, ScopeID: "room-1", ComponentID: expert, Kind: "bnbloc.expert.package/v1"},
	} {
		u.CreatedBy, u.CreatedAtMs = "maya", 1
		if err := s.PutContentUse(ctx, u); err != nil {
			t.Fatalf("put use: %v", err)
		}
	}
	members, err := s.CountMemberUses(ctx, []string{pack, expert})
	if err != nil || members[pack] != 2 || members[expert] != 0 {
		t.Fatalf("member uses = %+v, %v", members, err)
	}
	uses, err := s.CountContentUses(ctx, []string{pack, expert})
	if err != nil || uses[pack] != 3 || uses[expert] != 1 {
		t.Fatalf("uses = %+v, %v; the room expert is not a second use", uses, err)
	}
	re, err := s.ListContentUses(ctx, ContentUseRoomExpert, "room-1")
	if err != nil || len(re) != 1 || re[0].ComponentID != expert {
		t.Fatalf("room expert = %+v, %v", re, err)
	}
}

func TestMemoryMemberUses(t *testing.T) {
	exerciseMemberUses(t, NewMemoryContentComponents(), "cc-mem")
}

func TestMySQLMemberUses(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	prefix := "cc-use-" + itoaBase(time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = b.DB.Exec(`DELETE FROM content_component_uses WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_component_versions WHERE component_id LIKE ?`, prefix+"%")
		_, _ = b.DB.Exec(`DELETE FROM content_components WHERE id LIKE ?`, prefix+"%")
		_ = b.DB.Close()
	})
	exerciseMemberUses(t, b.ContentComponents, prefix)
}
