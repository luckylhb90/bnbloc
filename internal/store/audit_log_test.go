package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// seedAuditLog appends the same scenario to any extension + grant store pair:
// two tenants, one tenantless platform row, and timestamp ties both within
// and across the two trails.
func seedAuditLog(t *testing.T, ext ExtensionAuditStore, grants GrantStore, tA, tB, actor string, base int64) {
	t.Helper()
	for _, rec := range []ExtAuditRecord{
		{Kind: "member", Name: "u1", Action: "add", Actor: actor, TenantID: tA, Outcome: "ok", AtMs: base + 30},
		{Kind: "session", Name: "f1", Action: "revoke", Actor: actor, TenantID: tA, Outcome: "revoked", AtMs: base + 20},
		{Kind: "member", Name: "u2", Action: "role", Actor: actor, TenantID: tA, Outcome: "ok", AtMs: base + 20},
		{Kind: "plugin", Name: "p", Action: "install", Actor: actor, TenantID: "", Outcome: "allow", AtMs: base + 25},
		{Kind: "member", Name: "u9", Action: "add", Actor: actor, TenantID: tB, Outcome: "ok", AtMs: base + 26},
	} {
		if err := ext.AppendExtAudit(context.Background(), rec); err != nil {
			t.Fatalf("AppendExtAudit: %v", err)
		}
	}
	for _, rec := range []AuditRecord{
		{RoomID: "r1", ActorUserID: actor, OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: base + 20},
		{RoomID: "r1", ActorUserID: actor, OwnerUserID: "owner", Action: "turn", Decision: "deny", DenyReason: "quota", AtMs: base + 10},
	} {
		if err := grants.AppendAudit(tenantCtx(tA), rec); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	if err := grants.AppendAudit(tenantCtx(tB), AuditRecord{RoomID: "r1", ActorUserID: actor, OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: base + 40}); err != nil {
		t.Fatalf("AppendAudit B: %v", err)
	}
}

// pageAll walks every page of size n and returns the rows in order.
func pageAll(t *testing.T, q AuditLogQuery, n int, readers ...AuditLogReader) []AuditLogRow {
	t.Helper()
	var all []AuditLogRow
	q.Limit = n
	for i := 0; i < 50; i++ {
		rows, next, err := MergeAuditLog(context.Background(), q, readers...)
		if err != nil {
			t.Fatalf("MergeAuditLog: %v", err)
		}
		all = append(all, rows...)
		if next == nil {
			return all
		}
		q.After = next
	}
	t.Fatal("pagination did not terminate")
	return nil
}

func auditKeys(rows []AuditLogRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Kind+"/"+r.Action+"/"+r.Outcome)
	}
	return out
}

// checkAuditScenario asserts merge order, tenant isolation, and cursor
// paging for the seeded scenario (base-relative timestamps).
func checkAuditScenario(t *testing.T, tA, tB, actor string, readers ...AuditLogReader) {
	t.Helper()
	// Tenant A: own rows only — never tenant B's, never platform rows. Ties
	// at the same at_ms order extension (rank 0, newest id first) before
	// borrow (rank 1).
	want := []string{
		"member/add/ok",          // +30
		"member/role/ok",         // +20 extension, later id
		"session/revoke/revoked", // +20 extension, earlier id
		"borrow/turn/allow",      // +20 borrow
		"borrow/turn/deny",       // +10
	}
	q := AuditLogQuery{TenantID: tA, Actor: actor}
	for _, n := range []int{1, 2, 3, 100} {
		got := auditKeys(pageAll(t, q, n, readers...))
		if len(got) != len(want) {
			t.Fatalf("page size %d: got %v, want %v", n, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("page size %d: order %v, want %v", n, got, want)
			}
		}
	}
	for _, r := range pageAll(t, q, 2, readers...) {
		if r.TenantID != tA {
			t.Fatalf("tenant %s view leaked row of tenant %q: %+v", tA, r.TenantID, r)
		}
	}

	// Platform (all tenants): every row, including the tenantless one.
	all := pageAll(t, AuditLogQuery{AllTenants: true, Actor: actor}, 3, readers...)
	if len(all) != 8 {
		t.Fatalf("all-tenant rows = %d (%v), want 8", len(all), auditKeys(all))
	}
	for i := 1; i < len(all); i++ {
		if auditBefore(all[i].Cursor(), all[i-1].Cursor()) {
			t.Fatalf("all-tenant rows out of order at %d: %v", i, auditKeys(all))
		}
	}

	// Kind filter: borrow only skips the extension trail and vice versa.
	if got := pageAll(t, AuditLogQuery{TenantID: tA, Actor: actor, Kinds: []string{"borrow"}}, 10, readers...); len(got) != 2 {
		t.Fatalf("kind=borrow rows = %v", auditKeys(got))
	}
	if got := pageAll(t, AuditLogQuery{TenantID: tA, Actor: actor, Kinds: []string{"member", "session"}}, 10, readers...); len(got) != 3 {
		t.Fatalf("kind=member,session rows = %v", auditKeys(got))
	}
	// Outcome filter applies to the borrow decision too.
	if got := pageAll(t, AuditLogQuery{TenantID: tA, Actor: actor, Outcome: "deny"}, 10, readers...); len(got) != 1 || got[0].Kind != "borrow" {
		t.Fatalf("outcome=deny rows = %v", auditKeys(got))
	}
	// Unscoped query is fail-closed.
	if got := pageAll(t, AuditLogQuery{Actor: actor}, 10, readers...); len(got) != 0 {
		t.Fatalf("unscoped query returned %v", auditKeys(got))
	}
	_ = tB
}

func TestAuditLogMergeOrderAndIsolation(t *testing.T) {
	ext, grants := NewMemoryExtensionAudit(), NewMemoryGrant()
	seedAuditLog(t, ext, grants, "ta", "tb", "admin", 1000)
	checkAuditScenario(t, "ta", "tb", "admin", ext, grants)

	// from/to bound the time range (to exclusive).
	got := pageAll(t, AuditLogQuery{TenantID: "ta", FromMs: 1020, ToMs: 1030}, 10, ext, grants)
	if len(got) != 3 {
		t.Fatalf("range rows = %v", auditKeys(got))
	}
}

func TestMySQLAuditLog(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatalf("NewBackends: %v", err)
	}
	defer b.DB.Close()
	suffix := itoaBase(time.Now().UnixNano())
	tA, tB, actor := "it-audit-a-"+suffix, "it-audit-b-"+suffix, "it-auditor-"+suffix
	seedAuditLog(t, b.ExtAudit, b.Grant, tA, tB, actor, time.Now().UnixMilli())
	checkAuditScenario(t, tA, tB, actor, b.AuditLogReaders()...)
}
