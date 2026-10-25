package store

import (
	"errors"
	"testing"
)

// The BridgeSendStore contract, over memory and MySQL: a send is queued only
// for a member with a live bridge of their own; its id is used once; the
// owner's claim takes it once (oldest first, never another owner's) and the
// owner holds it while it is fresh; expired ones are gone.
func TestContractBridgeSendStore(t *testing.T) {
	forEachBackend(t, func(t *testing.T, b *Backends) {
		r := newBJRig(b)
		sends := b.BridgeJobs.(BridgeSendStore)
		const now = 1_000_000_000
		put := func(id string, at int64) error {
			return sends.PutBridgeSend(r.ctx, BridgeSend{SendID: id, OwnerUserID: r.owner, Component: "connections", Method: "GET", URL: "https://api.example.com/v1/me"}, at)
		}
		if err := put(uniq("s-"), now); !errors.Is(err, ErrNoLiveBridge) {
			t.Fatalf("no bridge = %v", err)
		}
		r.beat(t, BridgeOnline, now)
		first, second := uniq("s-a-"), uniq("s-b-")
		if err := put(first, now); err != nil {
			t.Fatal(err)
		}
		if err := put(first, now); !errors.Is(err, ErrBridgeSendExists) {
			t.Fatalf("an id twice = %v", err)
		}
		if err := put(second, now+1); err != nil {
			t.Fatal(err)
		}
		if got, _ := sends.TakeBridgeSends(r.ctx, uniq("jack-"), "br-x", now+2); len(got) != 0 {
			t.Fatalf("another owner took %+v", got)
		}
		got, err := sends.TakeBridgeSends(r.ctx, r.owner, r.bridge, now+2)
		if err != nil || len(got) != 2 || got[0].SendID != first || got[0].TenantID != r.tid || got[0].TakenBy != r.bridge || got[0].URL != "https://api.example.com/v1/me" {
			t.Fatalf("take = %+v %v", got, err)
		}
		if again, _ := sends.TakeBridgeSends(r.ctx, r.owner, r.bridge, now+3); len(again) != 0 {
			t.Fatalf("taken twice: %+v", again)
		}
		if h, err := sends.HeldBridgeSend(r.ctx, first, r.owner, now+4); err != nil || h.Component != "connections" {
			t.Fatalf("held = %+v %v", h, err)
		}
		if _, err := sends.HeldBridgeSend(r.ctx, first, uniq("jack-"), now+4); !errors.Is(err, ErrBridgeSendNotFound) {
			t.Fatalf("held by another owner = %v", err)
		}
		if _, err := sends.HeldBridgeSend(r.ctx, first, r.owner, now+2+BridgeSendHeldMs); !errors.Is(err, ErrBridgeSendNotFound) {
			t.Fatalf("held past its time = %v", err)
		}
		late := uniq("s-c-")
		if err := put(late, now+5); err != nil {
			t.Fatal(err)
		}
		if got, _ := sends.TakeBridgeSends(r.ctx, r.owner, r.bridge, now+5+BridgeSendWaitMs); len(got) != 0 {
			t.Fatalf("an expired send was taken: %+v", got)
		}
	})
}
