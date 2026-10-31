package realtimehttp

import (
	"context"
	"time"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// roomView is one room as one stream shows it: which of the room's events
// reach the stream and in which form. A room's own stream
// (/v1/rooms/{id}/events) has one for its room; a member's stream
// (/v1/my/events) one for the room the tab has open, replaced when it
// opens another. Every rule a room's events pass on their way to a person
// or an agent lives here, once.
type roomView struct {
	s      *Service
	ctx    context.Context // in the room's tenant
	roomID string
	userID string
	m      membership.Member
	human  bool
	// allChannels: every channel the reader may read; else channelID only
	// (plus room-wide and cross-channel kinds).
	allChannels bool
	channelID   string
	// lane, for a person's lanes view: full events of the open channels,
	// a coalesced channel_activity for the others.
	lane *laneStream
	// agents, for a view that is not a person's reading every channel:
	// the channels agents are not admitted in are skipped.
	agents  *agentFilter
	readers *channelAccess
}

// write passes one event through the view to send. revoked reports that
// the reader's access to the room ended with it (they were removed, the
// room was deleted, or a one-channel view lost its channel): the reader
// has been told, and the view is done.
func (v *roomView) write(ev wire.Event, send func(wire.Event)) (revoked bool) {
	v.readers.observe(ev)
	if wire.InternalKind(ev.Kind) {
		return false
	}
	if !v.allChannels && ev.ChannelID != "" && ev.ChannelID != v.channelID && !wire.CrossChannelKind(ev.Kind) {
		return false
	}
	if v.agents != nil && v.agents.skip(ev) {
		return false
	}
	if ev.ChannelID != "" && !v.readers.mayRead(ev.ChannelID) {
		// A one-channel view whose channel the reader no longer reads (they
		// left its section, or it moved into one) ends: its reader reopens
		// into the refusal.
		return !v.allChannels && ev.ChannelID == v.channelID
	}
	// A one-channel view that is not a person's was admitted to its channel
	// when it opened; a settings change can bar agents since: the view ends
	// (on a failed lookup too, fail closed).
	if ev.Kind == wire.EventChannelUpdated && !v.allChannels && !v.human {
		if barred, err := v.s.agentsBarred(v.ctx, v.roomID, v.channelID); err != nil || barred {
			return true
		}
	}
	// A preview reads the feed and the deliverables, not the room's agent
	// runs, tasks, live views or rosters (wire.PreviewedKind).
	if v.m.Preview && !wire.PreviewedKind(ev.Kind) {
		return false
	}
	if v.lane != nil {
		switch v.lane.lane(ev) {
		case wire.LaneNone:
			return false
		case wire.LaneActivity:
			if a, due := v.lane.note(ev, time.Now()); due {
				send(a)
			}
			return accessRevoked(ev, v.userID)
		}
	}
	send(ev)
	return accessRevoked(ev, v.userID)
}

// tick sends the lane's coalesced activity that is due.
func (v *roomView) tick(now time.Time, send func(wire.Event)) (sent bool) {
	if v.lane == nil {
		return false
	}
	v.lane.report(v.s.Hub.Metrics)
	v.lane.forgetOpen()
	for _, a := range v.lane.flush(now) {
		send(a)
		sent = true
	}
	return sent
}
