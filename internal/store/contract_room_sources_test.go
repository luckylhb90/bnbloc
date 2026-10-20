package store

import (
	"errors"
	"testing"
)

// A room asks for a source once, waiting; the admin's decision sets its
// scope and who decided, a withdrawal empties it again and the ask (its
// purpose, its alias key) stays. Asks are listed newest first per tenant;
// opt-ins are set and cleared idempotently per source; both go with the
// room.
func TestContractRoomSourceStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		s := b.RoomSources
		ctx := tenantCtx(uniq("t-src-"))
		reader, other := newContractRoom(t, ctx, b, "maya"), newContractRoom(t, ctx, b, "jack")
		ask := RoomSourceGrant{RoomID: reader, Source: "runs", ComponentID: "cc_1", ComponentName: "outcome-review", ComponentVersion: "1.0.0",
			Purpose: "Judge outcomes.", AliasKey: "k1", Scope: "all", AskedBy: "maya", AskedAtMs: 10, DecidedBy: "x", DecidedAtMs: 9}
		if err := s.AskRoomSource(ctx, ask); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRoomSourceGrant(ctx, reader, "runs")
		if err != nil || got.Scope != "" || got.DecidedBy != "" || got.DecidedAtMs != 0 || got.Purpose != "Judge outcomes." || got.AliasKey != "k1" || got.ComponentName != "outcome-review" {
			t.Fatalf("a new ask = %+v, %v (waiting, its decision ignored)", got, err)
		}
		if err := s.AskRoomSource(ctx, ask); !errors.Is(err, ErrRoomSourceAsked) {
			t.Fatalf("a second ask = %v", err)
		}
		if err := s.AskRoomSource(ctx, RoomSourceGrant{RoomID: other, Source: "runs", ComponentID: "cc_1", ComponentName: "outcome-review",
			ComponentVersion: "1.0.0", Purpose: "p", AliasKey: "k2", AskedBy: "jack", AskedAtMs: 20}); err != nil {
			t.Fatal(err)
		}
		if list, err := s.RoomSourceGrants(ctx); err != nil || len(list) != 2 || list[0].RoomID != other || list[1].RoomID != reader {
			t.Fatalf("asks = %+v, %v (newest first)", list, err)
		}
		if list, _ := s.RoomSourceGrants(tenantCtx(uniq("t-x-"))); len(list) != 0 {
			t.Fatalf("another tenant lists %v", list)
		}
		g, err := s.DecideRoomSource(ctx, reader, "runs", "opted-in", "olga", 30)
		if err != nil || g.Scope != "opted-in" || g.DecidedBy != "olga" || g.DecidedAtMs != 30 || g.AliasKey != "k1" {
			t.Fatalf("grant = %+v, %v", g, err)
		}
		if g, err = s.DecideRoomSource(ctx, reader, "runs", "", "olga", 40); err != nil || g.Scope != "" || g.Purpose != "Judge outcomes." {
			t.Fatalf("withdraw = %+v, %v", g, err)
		}
		if _, err := s.DecideRoomSource(ctx, reader, "mail", "all", "olga", 50); !errors.Is(err, ErrRoomSourceNotFound) {
			t.Fatalf("deciding a source never asked = %v", err)
		}
		if _, err := s.GetRoomSourceGrant(tenantCtx(uniq("t-x-")), reader, "runs"); !errors.Is(err, ErrRoomSourceNotFound) {
			t.Fatalf("another tenant read the ask: %v", err)
		}

		in := RoomSourceOptIn{RoomID: other, Source: "runs", OptedBy: "jack", OptedAtMs: 5}
		for range 2 {
			if err := s.SetRoomSourceOptIn(ctx, in, true); err != nil {
				t.Fatal(err)
			}
		}
		if list, err := s.RoomSourceOptIns(ctx, "runs"); err != nil || len(list) != 1 || list[0].RoomID != other || list[0].OptedBy != "jack" || list[0].OptedAtMs != 5 {
			t.Fatalf("opt-ins = %+v, %v", list, err)
		}
		if list, _ := s.RoomSourceOptIns(ctx, "mail"); len(list) != 0 {
			t.Fatalf("another source's opt-ins = %v", list)
		}
		for range 2 {
			if err := s.SetRoomSourceOptIn(ctx, in, false); err != nil {
				t.Fatal(err)
			}
		}
		if list, _ := s.RoomSourceOptIns(ctx, "runs"); len(list) != 0 {
			t.Fatalf("opted out, still listed: %v", list)
		}
		_ = s.SetRoomSourceOptIn(ctx, in, true)
		for _, room := range []string{reader, other} {
			if err := b.Room.DeleteRoom(ctx, room); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.GetRoomSourceGrant(ctx, reader, "runs"); !errors.Is(err, ErrRoomSourceNotFound) {
			t.Fatalf("an ask outlived its room: %v", err)
		}
		if list, _ := s.RoomSourceOptIns(ctx, "runs"); len(list) != 0 {
			t.Fatalf("an opt-in outlived its room: %v", list)
		}
	})
}
