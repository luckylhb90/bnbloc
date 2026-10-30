package realtimehttp

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// One write per message (docs/prd/2026-10-30-message-path-performance.md
// §4.2): a post's channel-follow mark (posting joins the channel and makes
// "all" its default level, C4, C18) is written behind, off the request, and
// only when it changes something: a member's first post in a channel since
// this owner started, or every post (coalesced per markFlushEvery) where slow
// mode needs its time. With the bus's claims (Service.Claims) slow mode is
// one claim per member and channel that every replica shares (the member's
// next post may reach any of them); without, it is decided from this
// replica's memory of each member's last post, read from the store only on
// a miss while slow mode is on (after a restart or an ownership move).

const markFlushEvery = 2 * time.Second

type postKey struct{ tenant, room, channel, user string }

type postMarks struct {
	svc *Service // the service whose posts these are (set on first claim)

	mu      sync.Mutex
	last    map[postKey]int64 // last post, for slow mode
	pending map[postKey]int64 // marks to write
	// marked are the (member, channel) pairs whose mark this owner wrote: a
	// later post changes nothing the mark decides (joined, posted) unless
	// slow mode needs its time after a restart.
	marked  map[postKey]bool
	started bool
}

// claim decides a post under slow mode (gap ms, 0 = off) and records it: the
// wait in ms when the member posted too recently, else 0 with the post
// remembered and its mark queued.
func (pm *postMarks) claim(ctx context.Context, svc *Service, roomID, channelID, userID string, atMs, gap int64) (int64, error) {
	k := postKey{tenant.FromContext(ctx), roomID, channelID, userID}
	if gap > 0 && svc.Claims != nil {
		// Replicas without owners: the member's next post may reach any
		// replica, so the gap is one claim they all share.
		ok, wait, err := svc.Claims.Claim(k.tenant+"\x00"+roomID+"\x00"+channelID+"\x00"+userID, time.Duration(gap)*time.Millisecond)
		if err == nil {
			if !ok {
				return max(wait.Milliseconds(), 1), nil
			}
			pm.record(svc, k, atMs, gap)
			return 0, nil
		}
		log.Printf("realtime: slow mode claim room=%s channel=%s: %v (deciding from this replica's memory)", roomID, channelID, err)
	}
	pm.mu.Lock()
	pm.svc = svc
	last, known := pm.last[k]
	pm.mu.Unlock()
	if gap > 0 && !known {
		// Not posted here since this owner started: the store's mark says.
		follows, err := svc.channelStore().ChannelFollows(ctx, roomID, userID)
		if err != nil {
			return 0, err
		}
		for _, f := range follows {
			if f.ChannelID == channelID {
				last = f.LastPostedAtMs
			}
		}
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if cur, ok := pm.last[k]; ok && cur > last {
		last = cur // a concurrent post of the same member won
	}
	if gap > 0 && last > 0 && atMs-last < gap {
		return last + gap - atMs, nil
	}
	pm.recordLocked(svc, k, atMs, gap)
	return 0, nil
}

// record remembers a post and queues its mark.
func (pm *postMarks) record(svc *Service, k postKey, atMs, gap int64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.recordLocked(svc, k, atMs, gap)
}

func (pm *postMarks) recordLocked(svc *Service, k postKey, atMs, gap int64) {
	pm.svc = svc
	if pm.last == nil {
		pm.last, pm.pending, pm.marked = map[postKey]int64{}, map[postKey]int64{}, map[postKey]bool{}
	}
	pm.last[k] = atMs
	if gap > 0 || !pm.marked[k] {
		pm.pending[k] = atMs
		pm.marked[k] = true
	}
	if len(pm.marked) > 200000 {
		pm.marked = map[postKey]bool{} // a bound: forgetting costs one more upsert
	}
	if len(pm.last) > 200000 {
		pm.last = map[postKey]int64{} // a bound: forgetting costs a store read under slow mode
	}
	if !pm.started {
		pm.started = true
		go pm.run()
	}
}

// run writes the pending marks every markFlushEvery, for the process's life.
func (pm *postMarks) run() {
	for range time.Tick(markFlushEvery) {
		pm.flush()
	}
}

// flush writes every pending mark (posting joins: the follow row and its
// last post); a failed write is logged and dropped, a later post writes it
// again.
func (pm *postMarks) flush() {
	pm.mu.Lock()
	batch, svc := pm.pending, pm.svc
	pm.pending = map[postKey]int64{}
	pm.mu.Unlock()
	if svc == nil || len(batch) == 0 {
		return
	}
	chans := svc.channelStore()
	if chans == nil {
		return
	}
	for k, at := range batch {
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), k.tenant), 10*time.Second)
		if _, err := chans.ClaimChannelPost(ctx, k.room, k.channel, k.user, at, 0); err != nil {
			log.Printf("realtime: room=%s channel=%s: post mark for %s not written: %v", k.room, k.channel, k.user, err)
		}
		cancel()
	}
}

// forget drops what this owner knows of a member's mark of a channel they
// followed, left or re-marked (wire.EventChannelFollow): their next post
// there writes it again (posting joins, C18).
func (pm *postMarks) forget(ctx context.Context, ev wire.Event) {
	p, _ := wire.DecodePayload[wire.ChannelFollowPayload](ev.Payload)
	pm.mu.Lock()
	delete(pm.marked, postKey{tenant.FromContext(ctx), ev.RoomID, p.ChannelID, p.UserID})
	pm.mu.Unlock()
}

// forgetRemote is forget for another replica's event (the hub's bus), which
// carries no tenant: the member's mark is dropped under every tenant (room
// ids are global).
func (pm *postMarks) forgetRemote(ev wire.Event) {
	p, _ := wire.DecodePayload[wire.ChannelFollowPayload](ev.Payload)
	pm.mu.Lock()
	for k := range pm.marked {
		if k.room == ev.RoomID && k.channel == p.ChannelID && k.user == p.UserID {
			delete(pm.marked, k)
		}
	}
	pm.mu.Unlock()
}

// Shutdown is what a stopping replica finishes before it exits (registered
// with obs.OnShutdown): the follow marks written behind, and the
// notifications of what it observed.
func (s *Service) Shutdown(ctx context.Context) {
	if s.viewsStop != nil {
		close(s.viewsStop)
	}
	s.postMarks.flush()
	if s.Notifications != nil {
		s.Notifications.Observer.Drain(ctx)
	}
}
