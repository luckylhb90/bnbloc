package notify

import (
	"context"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

type countingPrefs struct {
	store.NotificationPrefsStore
	reads int
}

func (c *countingPrefs) GetNotificationPrefs(ctx context.Context, userID string) (store.NotificationPrefs, bool, error) {
	c.reads++
	return c.NotificationPrefsStore.GetNotificationPrefs(ctx, userID)
}

// The producer reads a member's preferences once per prefsTTL; a change saved
// here takes effect at once; a recipient inside the collapse window is not
// read at all.
func TestPrefsReadOncePerWindow(t *testing.T) {
	f := newFixture(t)
	f.install(t, "n1", fakeNotifier(t, nil).URL+"/hook", "")
	counting := &countingPrefs{NotificationPrefsStore: f.mem}
	cache := newPrefsCache(counting, f.clock.Now)
	f.emitter.Prefs = cache
	ctx := tctx()
	for i := 0; i < 3; i++ {
		if _, _, err := cache.GetNotificationPrefs(ctx, "bob"); err != nil {
			t.Fatal(err)
		}
	}
	if counting.reads != 1 {
		t.Fatalf("reads within the window = %d, want 1", counting.reads)
	}
	f.emitter.forgetPrefs(ctx, "bob")
	_, _, _ = cache.GetNotificationPrefs(ctx, "bob")
	f.clock.t = f.clock.t.Add(prefsTTL + time.Second)
	_, _, _ = cache.GetNotificationPrefs(ctx, "bob")
	if counting.reads != 3 {
		t.Fatalf("reads after forget and expiry = %d, want 3", counting.reads)
	}

	// Collapsed: the second channel message of the minute reads nothing.
	ev := func(key string) Event {
		return Event{Type: wire.NotificationChannelMessage, RoomID: "r1", Recipients: []string{"carol"}, Actor: "alice", Title: "alice posted", DedupKey: key}
	}
	if ok, err := f.emitter.Emit(ctx, ev("msg:r1:1:all")); err != nil || !ok {
		t.Fatalf("first emit = %v %v", ok, err)
	}
	before := counting.reads
	if ok, _ := f.emitter.Emit(ctx, ev("msg:r1:2:all")); ok {
		t.Fatal("a second channel message inside the minute was queued")
	}
	if counting.reads != before {
		t.Fatalf("a collapsed recipient's preferences were read (%d reads)", counting.reads-before)
	}
}
