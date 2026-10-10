package store

import (
	"testing"
)

// TenantAuditRecords must return only the caller-tenant's audit rows, spanning
// every room in that tenant, newest-first. Two tenants that append audit rows
// (including a reused room id) must never see each other's records; a silent
// leak here would expose one org's audit trail to another.
func TestTenantAuditRecordsIsolatesTenantsAcrossRooms(t *testing.T) {
	s := NewMemoryGrant()
	ca, cb := tenantCtx(tenantA), tenantCtx(tenantB)

	// Tenant A: three records across two rooms (one room id "shared" is reused
	// by tenant B below).
	for _, rec := range []AuditRecord{
		{RoomID: "room-1", ActorUserID: "alice", OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: 10},
		{RoomID: "room-2", ActorUserID: "alice", OwnerUserID: "owner", Action: "start", Decision: "allow", AtMs: 30},
		{RoomID: "shared", ActorUserID: "alice", OwnerUserID: "owner", Action: "turn", Decision: "deny", DenyReason: "quota", AtMs: 20},
	} {
		if err := s.AppendAudit(ca, rec); err != nil {
			t.Fatalf("A append: %v", err)
		}
	}
	// Tenant B: one record in a room id that collides with tenant A's.
	if err := s.AppendAudit(cb, AuditRecord{RoomID: "shared", ActorUserID: "bob", OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: 99}); err != nil {
		t.Fatalf("B append: %v", err)
	}

	// Tenant A sees exactly its three rows, newest-first, across both rooms.
	got := s.TenantAuditRecords(ca, 0, 0)
	if len(got) != 3 {
		t.Fatalf("A TenantAuditRecords = %d rows, want 3", len(got))
	}
	if got[0].AtMs != 30 || got[1].AtMs != 20 || got[2].AtMs != 10 {
		t.Fatalf("A records not newest-first: %d, %d, %d", got[0].AtMs, got[1].AtMs, got[2].AtMs)
	}
	rooms := map[string]bool{}
	for _, r := range got {
		if r.ActorUserID == "bob" {
			t.Fatalf("tenant A leaked tenant B row: %+v", r)
		}
		rooms[r.RoomID] = true
	}
	if !rooms["room-1"] || !rooms["room-2"] || !rooms["shared"] {
		t.Fatalf("A records missing a room: %v", rooms)
	}

	// Tenant B sees only its single row despite the reused "shared" room id.
	gotB := s.TenantAuditRecords(cb, 0, 0)
	if len(gotB) != 1 || gotB[0].ActorUserID != "bob" {
		t.Fatalf("B TenantAuditRecords = %+v, want single bob row", gotB)
	}

	// before-cursor keeps only records strictly older than the timestamp.
	cur := s.TenantAuditRecords(ca, 0, 30)
	if len(cur) != 2 || cur[0].AtMs != 20 || cur[1].AtMs != 10 {
		t.Fatalf("A before=30 = %+v, want at_ms 20 then 10", cur)
	}
}
