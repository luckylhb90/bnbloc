package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// After the drain the report waits for the streams to be told of every
// stored message: a delta follows its message by up to the server's
// interval (one second), more on a loaded host, so a fixed wait would count
// a late delta as never sent. It stops when nothing more was told for
// activityQuiet (deltas stopped coming), or after activitySettle.
const (
	activityQuiet  = 2 * time.Second
	activitySettle = 10 * time.Second
)

// activityStats aggregates every activity stream.
type activityStats struct {
	mu     sync.Mutex
	states int
	deltas int
	// seen are the (member, room, seq) a delta named.
	seen map[string]bool
}

func newActivityStats() *activityStats { return &activityStats{seen: map[string]bool{}} }

func (s *activityStats) add(f func(s *activityStats)) {
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

// note counts a stream's unread event (unread_state, unread_delta) and
// reports whether it was one.
func (s *activityStats) note(stream int, kind string, data []byte) bool {
	switch kind {
	case wire.UnreadStateKind:
		s.add(func(s *activityStats) { s.states++ })
		return true
	case wire.UnreadDeltaKind:
		var ev struct {
			Payload wire.UnreadDeltaPayload `json:"payload"`
		}
		if json.Unmarshal(data, &ev) != nil {
			return true
		}
		s.add(func(s *activityStats) {
			s.deltas++
			for room, d := range ev.Payload.Rooms {
				for _, seq := range d.Seqs {
					s.seen[fmt.Sprintf("%d\x00%s\x00%d", stream, room, seq)] = true
				}
			}
		})
		return true
	}
	return false
}

// ActivityStreamReport covers the unread the streams carry (each is
// scoped to its member's rooms, as a web tab is).
type ActivityStreamReport struct {
	States int `json:"states"`
	Deltas int `json:"deltas"`
	// Told counts the new messages the streams were told of, one per
	// stream and message; Expected is, for every stream, every stored send
	// of the rooms it carries (each is told of every message of them, its
	// member's own included). A stream that reopened is sent the state
	// instead, so Told falls short of Expected only by what reopening
	// covered.
	Told     int `json:"told"`
	Expected int `json:"expected"`
}

func unreadReport(streams []*stream, stats *activityStats, rooms []*room) ActivityStreamReport {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	stored := map[string]int{}
	for _, r := range rooms {
		stored[r.id] = int(r.stored.Load())
	}
	rep := ActivityStreamReport{States: stats.states, Deltas: stats.deltas, Told: len(stats.seen)}
	for _, st := range streams {
		for _, id := range st.rooms {
			rep.Expected += stored[id]
		}
	}
	return rep
}
