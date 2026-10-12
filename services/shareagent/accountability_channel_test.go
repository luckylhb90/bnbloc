package shareagenthttp

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// A recorded borrow carries the originating channel into the audit trail, so an
// admin can see which topic drove each borrow turn.
func TestRecordBorrowCarriesChannel(t *testing.T) {
	svc, gs := newService(t)
	seedAuthorizedBorrow(t, gs, tenant.Default)
	code, body := recordBorrow(t, svc, tenant.Default, 0, "ch_x")
	if code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("record = %d %v", code, body)
	}
	recs, err := gs.AuditRecords(tctx(tenant.Default), "room-1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("no audit records after borrow")
	}
	if recs[0].ChannelID != "ch_x" {
		t.Fatalf("audit channel = %q, want ch_x", recs[0].ChannelID)
	}
}

// A denied borrow (org quota exhausted) also records the channel.
func TestDeniedBorrowCarriesChannel(t *testing.T) {
	svc, gs := newService(t)
	tenants := store.NewMemoryTenant()
	_ = tenants.PutTenant(tctx(tenant.Default), store.Tenant{TenantID: tenant.Default, Name: "D", Status: store.TenantActive})
	if err := tenants.SetQuota(tctx(tenant.Default), tenant.Default, 1, 0); err != nil { // run ceiling 1
		t.Fatal(err)
	}
	svc.Tenants = tenants
	seedAuthorizedBorrow(t, gs, tenant.Default)

	// First borrow recorded (channel ch_a)...
	if code, body := recordBorrow(t, svc, tenant.Default, 0, "ch_a"); code != http.StatusOK || body["recorded"] != true {
		t.Fatalf("first borrow = %d %v", code, body)
	}
	// ...second denied on the same channel: the deny audit carries ch_a too.
	code, body := recordBorrow(t, svc, tenant.Default, 0, "ch_a")
	if code != http.StatusForbidden || body["denyReason"] != "org_quota_exhausted" {
		t.Fatalf("denied borrow = %d %v", code, body)
	}
	recs, err := gs.AuditRecords(tctx(tenant.Default), "room-1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 2 {
		t.Fatalf("audit records = %d, want >= 2", len(recs))
	}
	// The deny record is identified by its decision: the fixed test clock
	// gives the two records equal timestamps, so ordering is not meaningful.
	var deny *store.AuditRecord
	for i := range recs {
		if recs[i].Decision == "deny" {
			deny = &recs[i]
		}
	}
	if deny == nil {
		t.Fatalf("no deny record in %+v", recs)
	}
	if deny.Action != "turn" || deny.ChannelID != "ch_a" {
		t.Fatalf("deny record = %+v, want turn/ch_a", deny)
	}
}
