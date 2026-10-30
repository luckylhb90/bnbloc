package realtimehttp

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room changing owner (a replica stopping, crashing or joining): for a
// moment the replicas' views disagree, and the one leaving may still store a
// message whose live event reaches nobody (its streams have moved). The new
// owner reads the room's newest rows for a while after it starts serving the
// room and delivers any it did not deliver itself (Hub.DeliverCommitted), so
// no stream misses a message across the change
// (docs/production-deployment.md#high-availability).

const (
	// handoverWatch is how long a room is watched after this replica starts
	// serving it: well past the time the views take to agree (two missed
	// probes of a second, a few seconds at most).
	handoverWatch = 6 * time.Second
	// handoverEvery is how often the watch reads the room's newest rows.
	handoverEvery = 300 * time.Millisecond
)

// handovers are the rooms this replica serves and has watched since it
// started serving them.
type handovers struct {
	mu    sync.Mutex
	rooms map[string]time.Time // tenant + room -> when this replica started serving it
}

// noteServing starts the watch of a room the first time this replica serves
// it (a stream opens, a message is posted) since it started or since the
// room last moved away (forgetServed).
// It reports whether the room is within its watch: a stream that resumes
// then is told to read the difference (two owners may have written the room
// in turns, so the client's last sequence does not prove it has every
// earlier one).
func (s *Service) noteServing(ctx context.Context, roomID string) (handingOver bool) {
	if s.Chat == nil || s.Peers == nil || s.Peers.set == nil {
		return false // one replica: no other owner ever writes the room
	}
	key := tenant.FromContext(ctx) + "\x00" + roomID
	now := time.Now()
	s.handovers.mu.Lock()
	if s.handovers.rooms == nil {
		s.handovers.rooms = map[string]time.Time{}
	}
	if since, ok := s.handovers.rooms[key]; ok {
		s.handovers.mu.Unlock()
		return now.Sub(since) < handoverWatch
	}
	s.handovers.rooms[key] = now
	s.handovers.mu.Unlock()
	go s.watchHandover(tenant.NewContext(context.Background(), tenant.FromContext(ctx)), roomID)
	return true
}

// forgetServed drops the rooms this replica no longer owns: serving one
// again starts a new watch.
func (s *Service) forgetServed(owned func(roomID string) bool) {
	s.handovers.mu.Lock()
	defer s.handovers.mu.Unlock()
	for key := range s.handovers.rooms {
		if _, roomID, ok := strings.Cut(key, "\x00"); ok && !owned(roomID) {
			delete(s.handovers.rooms, key)
		}
	}
}

// watchHandover reads the room's rows newer than its durable max at the
// start, for handoverWatch, delivering those the hub did not.
func (s *Service) watchHandover(ctx context.Context, roomID string) {
	cursor, err := s.Chat.MaxRoomSeq(ctx, roomID)
	if err != nil {
		return
	}
	deadline := time.Now().Add(handoverWatch)
	for time.Now().Before(deadline) {
		time.Sleep(handoverEvery)
		rows, err := s.Chat.RoomFeed(ctx, roomID, cursor, 200)
		if err != nil {
			continue
		}
		for _, m := range rows {
			cursor = max(cursor, m.Seq)
			ev := wire.Event{Sequence: m.Seq, RoomID: roomID, ChannelID: m.ChannelID, Kind: m.Kind, Payload: json.RawMessage(m.Payload), AtUnixMs: m.CreatedAtMs}
			if s.Hub.DeliverCommitted(ev) {
				log.Printf("realtime: room=%s seq=%d: delivered a message the room's previous owner stored", roomID, m.Seq)
			}
		}
	}
}
