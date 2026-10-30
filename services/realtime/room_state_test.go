package realtimehttp

import (
	"context"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type countingChannels struct {
	ChannelStore
	gets, sections int
}

func (c *countingChannels) GetChannel(ctx context.Context, roomID, channelID string) (store.RoomChannel, bool, error) {
	c.gets++
	return c.ChannelStore.GetChannel(ctx, roomID, channelID)
}

func (c *countingChannels) ListSections(ctx context.Context, roomID string) ([]store.RoomSection, error) {
	c.sections++
	return c.ChannelStore.ListSections(ctx, roomID)
}

// The room's state is read once and kept until the room's own event changes
// it or roomStateTTL passes; a miss and another tenant's room are never
// answered from it.
func TestRoomStateKeptUntilItsEvents(t *testing.T) {
	mem := store.NewMemoryChannel()
	ctx := dctx()
	if err := mem.PutChannel(ctx, store.RoomChannel{ChannelID: "ch_a", TenantID: tenant.Default, RoomID: "room-1", Name: "a", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	counting := &countingChannels{ChannelStore: mem}
	now := time.UnixMilli(1_000_000)
	c := newRoomStateCache(func() time.Time { return now })
	view := cachedChannels{ChannelStore: counting, c: c}
	get := func(id string) bool {
		t.Helper()
		_, found, err := view.GetChannel(ctx, "room-1", id)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	for i := 0; i < 3; i++ {
		get("ch_a")
		if _, err := view.ListSections(ctx, "room-1"); err != nil {
			t.Fatal(err)
		}
	}
	if counting.gets != 1 || counting.sections != 1 {
		t.Fatalf("reads = %d channel, %d sections; want 1 each", counting.gets, counting.sections)
	}
	// A miss is read every time (a channel made a moment ago must be found).
	get("ch_new")
	get("ch_new")
	if counting.gets != 3 {
		t.Fatalf("a missing channel was answered from memory (%d reads)", counting.gets)
	}
	// The room's event: read again.
	c.observe(ctx, wire.Event{RoomID: "room-1", Kind: wire.EventChannelUpdated})
	get("ch_a")
	if counting.gets != 4 {
		t.Fatalf("channel_updated did not drop the room's state (%d reads)", counting.gets)
	}
	// Another tenant's room of the same id is its own.
	other := tenant.NewContext(context.Background(), "acme")
	if _, found, _ := view.GetChannel(other, "room-1", "ch_a"); found {
		t.Fatal("another tenant's room answered from this one's state")
	}
	// The bound: after roomStateTTL the room is read again.
	now = now.Add(roomStateTTL)
	get("ch_a")
	if counting.gets != 6 {
		t.Fatalf("state older than its bound was kept (%d reads)", counting.gets)
	}
	// A thread's update drops that thread only.
	c.observe(ctx, wire.Event{RoomID: "room-1", Kind: wire.EventThreadUpdated, Payload: wire.ThreadUpdatedPayload{ThreadID: "ch_a"}})
	get("ch_a")
	if counting.gets != 7 {
		t.Fatalf("thread_updated did not drop the thread (%d reads)", counting.gets)
	}
}
