package store

import (
	"errors"
	"testing"
)

// AuditRecords returns only the caller tenant's borrow records, one room's or
// every room's (the org-wide trail), newest first with the row id breaking
// ties, so paging never drops records sharing a millisecond; every column
// round-trips, and an empty page is [].
func TestContractGrantStoreAuditRecords(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		g := b.Grant
		ca, cb := tenantCtx(uniq("t-aud-a-")), tenantCtx(uniq("t-aud-b-"))
		r1, r2, shared := uniq("room-1-"), uniq("room-2-"), uniq("shared-")

		// Tenant A: four records across three rooms, two sharing a
		// millisecond; tenant B one record in a room id A uses too.
		for _, rec := range []AuditRecord{
			{RoomID: r1, ActorUserID: "maya", OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: 10},
			{RoomID: r2, ActorUserID: "maya", OwnerUserID: "owner", Action: "start", Decision: "allow", AtMs: 30},
			{RoomID: shared, ActorUserID: "maya", OwnerUserID: "owner", Action: "turn", Decision: "deny", DenyReason: "quota", AtMs: 20},
			{RoomID: r1, ActorUserID: "maya", OwnerUserID: "owner", Action: "release", Decision: "allow", ChannelID: "ch-1", AtMs: 20},
		} {
			if err := g.AppendAudit(ca, rec); err != nil {
				t.Fatalf("A append: %v", err)
			}
		}
		if err := g.AppendAudit(cb, AuditRecord{RoomID: shared, ActorUserID: "jack", OwnerUserID: "owner", Action: "turn", Decision: "allow", AtMs: 99}); err != nil {
			t.Fatalf("B append: %v", err)
		}

		got := must(g.AuditRecords(ca, AuditRecordQuery{}))(t)
		if len(got) != 4 || got[0].AtMs != 30 || got[1].AtMs != 20 || got[2].AtMs != 20 || got[3].AtMs != 10 {
			t.Fatalf("A records = %+v, want at 30, 20, 20, 10", got)
		}
		// The tie at 20 orders by row id, newest first: the release came last.
		if got[1].Action != "release" || got[2].Action != "turn" || got[1].ID <= got[2].ID {
			t.Fatalf("tie order = %+v then %+v, want the later row first", got[1], got[2])
		}
		for _, r := range got {
			if r.ActorUserID == "jack" {
				t.Fatalf("tenant A leaked tenant B's record: %+v", r)
			}
		}
		if got[1].ChannelID != "ch-1" || got[2].DenyReason != "quota" {
			t.Fatalf("columns lost: %+v / %+v", got[1], got[2])
		}
		gotB := must(g.AuditRecords(cb, AuditRecordQuery{}))(t)
		if len(gotB) != 1 || gotB[0].ActorUserID != "jack" {
			t.Fatalf("B records = %+v, want jack's alone", gotB)
		}

		// One room.
		if room := must(g.AuditRecords(ca, AuditRecordQuery{RoomID: r1}))(t); len(room) != 2 || room[0].Action != "release" {
			t.Fatalf("room records = %+v", room)
		}

		// Paging by cursor, one record a page, returns every record once in
		// order, including both records of the shared millisecond.
		var paged []AuditRecord
		q := AuditRecordQuery{Limit: 1}
		for range 10 {
			page := must(g.AuditRecords(ca, q))(t)
			if len(page) == 0 {
				break
			}
			paged = append(paged, page...)
			c := page[len(page)-1].Cursor()
			q.After = &c
		}
		if len(paged) != 4 {
			t.Fatalf("paged = %+v, want all 4 records", paged)
		}
		for i := range paged {
			if paged[i].ID != got[i].ID {
				t.Fatalf("page order %d = %+v, want %+v", i, paged[i], got[i])
			}
		}
		// A cursor with no id keeps the records strictly older than its time.
		older := must(g.AuditRecords(ca, AuditRecordQuery{After: &AuditCursor{AtMs: 30, Source: AuditSourceBorrow}}))(t)
		if len(older) != 3 || older[0].AtMs != 20 {
			t.Fatalf("before 30 = %+v", older)
		}

		// An empty page is [], never nil.
		if none := must(g.AuditRecords(tenantCtx(uniq("t-aud-none-")), AuditRecordQuery{}))(t); none == nil || len(none) != 0 {
			t.Fatalf("empty page = %#v", none)
		}
		if _, err := g.AuditRecords(bg(), AuditRecordQuery{}); err == nil {
			t.Fatal("a read without a tenant must fail")
		}
	})
}

// A settled reservation's record reads back with every column: model, cost,
// tokens, reservation id and settle time (MySQL used to drop them).
func TestContractGrantStoreAuditRecordsFullColumns(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		g := b.Grant
		ctx := tenantCtx(uniq("t-aud-full-"))
		gid, room, rid := uniq("g-"), uniq("room-"), uniq("rsv-")
		if err := g.PutGrant(ctx, Grant{GrantID: gid, RoomID: room, TargetID: "shared-agent:x", OwnerUserID: "maya", GranteeUserID: "*", CreatedAtMs: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.ReserveRun(ctx, RunReservation{ReservationID: rid, GrantID: gid, RoomID: room, OwnerUserID: "maya", BorrowerUserID: "jack", Model: "m-req", ChannelID: "ch-9", AtMs: 100}); err != nil {
			t.Fatal(err)
		}
		cost, in, out := 0.25, int64(12), int64(3)
		if err := g.SettleRun(ctx, gid, rid, RunUsage{Model: "m-rep", Cost: &cost, InputTokens: &in, OutputTokens: &out}, 150); err != nil {
			t.Fatal(err)
		}
		recs := must(g.AuditRecords(ctx, AuditRecordQuery{RoomID: room}))(t)
		if len(recs) != 1 {
			t.Fatalf("records = %+v", recs)
		}
		r := recs[0]
		if r.GrantID != gid || r.ActorUserID != "jack" || r.OwnerUserID != "maya" || r.Action != "turn" || r.Decision != "allow" ||
			r.Model != "m-rep" || r.ChannelID != "ch-9" || r.AtMs != 100 || r.ReservationID != rid ||
			r.Cost == nil || *r.Cost != 0.25 || r.InputTokens == nil || *r.InputTokens != 12 || r.OutputTokens == nil || *r.OutputTokens != 3 ||
			r.SettledAtMs == nil || *r.SettledAtMs != 150 || r.ID == 0 {
			t.Fatalf("record = %+v", r)
		}
		if !errors.Is(g.SettleRun(ctx, gid, rid, RunUsage{}, 160), ErrReservationSettled) {
			t.Fatal("a settled reservation settled again")
		}
	})
}
