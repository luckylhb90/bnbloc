package store

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// testPeopleChannel pins the people-chat contract on one ChannelStore: lazy
// creation is idempotent and race-free (one channel per room), the kind is
// readable even for unknown ids, and the people chat lists beside ordinary
// channels with its kind so callers can filter it.
func testPeopleChannel(t *testing.T, s ChannelStore, ctx context.Context, room string) {
	t.Helper()
	if err := s.PutChannel(ctx, RoomChannel{ChannelID: "ch_def_" + room, RoomID: room, Name: "general", IsDefault: true, CreatedBy: "alice", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]RoomChannel, 8)
	errs := make([]error, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = s.EnsurePeopleChannel(ctx, room, "user"+strconv.Itoa(i), int64(10+i))
		}(i)
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil {
			t.Fatalf("ensure %d: %v", i, errs[i])
		}
		if got[i].ChannelID != got[0].ChannelID || !got[i].IsPeople() || got[i].IsDefault {
			t.Fatalf("ensure %d = %+v, want the one people channel %s", i, got[i], got[0].ChannelID)
		}
	}
	people := 0
	for _, c := range must(s.ListChannels(ctx, room))(t) {
		if c.IsPeople() {
			people++
		}
	}
	if people != 1 {
		t.Fatalf("listed %d people channels, want exactly 1", people)
	}
	if kind, found, err := s.ChannelKind(ctx, room, got[0].ChannelID); err != nil || !found || kind != ChannelKindPeople {
		t.Fatalf("kind(people) = %q,%v,%v", kind, found, err)
	}
	if kind, found, err := s.ChannelKind(ctx, room, "ch_def_"+room); err != nil || !found || kind != ChannelKindStandard {
		t.Fatalf("kind(default) = %q,%v,%v", kind, found, err)
	}
	if _, found, err := s.ChannelKind(ctx, room, "ch_missing"); err != nil || found {
		t.Fatalf("kind(missing) found=%v err=%v, want not found", found, err)
	}
	// Another room (and the same room id in another tenant) gets its own.
	other, err := s.EnsurePeopleChannel(ctx, room+"-2", "alice", 1)
	if err != nil || other.ChannelID == got[0].ChannelID {
		t.Fatalf("other room people channel = %+v,%v", other, err)
	}
	foreign, err := s.EnsurePeopleChannel(tenant.NewContext(context.Background(), "tenant-b-"+room), room, "alice", 1)
	if err != nil || foreign.ChannelID == got[0].ChannelID {
		t.Fatalf("foreign tenant people channel = %+v,%v", foreign, err)
	}
}

func TestMemoryPeopleChannel(t *testing.T) {
	testPeopleChannel(t, NewMemoryChannel(), chctx(), "room-1")
}

func TestMySQLPeopleChannel(t *testing.T) {
	dsn := mysqlTestDSN(t)
	b, err := NewBackends(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.DB.Close()
	testPeopleChannel(t, b.Channel, chctx(), "pc-"+strconv.FormatInt(time.Now().UnixNano(), 36))
}
